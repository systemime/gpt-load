package gateway

import (
	"github.com/gin-gonic/gin"

	"gpt-load/internal/affinity"
	"gpt-load/internal/protocol"
	"gpt-load/internal/scheduler"
	"gpt-load/internal/state"
	"gpt-load/internal/telemetry"
)

type requestAffinity struct {
	key                   affinity.Key
	observation           affinity.Observation
	preferredCredentialID uint
	continuityKey         string
	kind                  string
}

// configureCredentialSlots 把快照里的全局 cap 应用到在途准入计数器。
// 与亲和缓存同款模式: 每请求调用, 阈值存储为原子值, 开销可忽略。
func (handler *Handler) configureCredentialSlots(snapshot *state.ConfigSnapshot) {
	if handler == nil || handler.credentialSlots == nil || snapshot == nil {
		return
	}
	handler.credentialSlots.Configure(snapshot.Settings.CredentialConcurrencyLimit)
}

// admitBeyondCap 处理全池达 cap 的豁免候选(阶段 2 排队准入)。
// 每次评估都在全部候选内重选(不锁定进入时刻的选择):
//   - 单凭证预算 = cap + CredentialQueueSlack;
//   - 任一候选在途 < 预算 → 立即占位(优先 preferred 亲和, 其次在途最小),
//     选中凭证与原 selection 不同时用作用域迭代器重建完整 Selection;
//   - 全部候选 ≥ 预算 → 排队, 等待任意 Release 广播唤醒后重跑上述选择;
//   - 单凭证排队深度超过预算两倍(防雪崩), 或等待超时, 429 + Retry-After。
//
// 返回 (selection, ref, true) 表示已持有槽位可继续执行;
// ok=false 表示请求已写回拒绝响应, 调用方直接结束。
func (handler *Handler) admitBeyondCap(
	ginContext *gin.Context,
	recorder *requestRecorder,
	snapshot *state.ConfigSnapshot,
	allowedCredentialRefs map[uint]state.CredentialRef,
	baseQuery scheduler.Query,
	preferredCredentialID uint,
	selection scheduler.Selection,
) (scheduler.Selection, state.CredentialRef, bool) {
	ref := allowedCredentialRefs[selection.CredentialID]
	slots := handler.credentialSlots
	capacity := slots.Capacity()
	if capacity <= 0 {
		slots.ForceAcquire(selection.CredentialID)
		return selection, ref, true
	}
	budgetPerCredential := int64(capacity) + state.CredentialQueueSlack
	ids := make([]uint, 0, len(allowedCredentialRefs))
	maxWaiting := int64(0)
	for id := range allowedCredentialRefs {
		if id == 0 {
			continue
		}
		ids = append(ids, id)
		if waiting := slots.Waiting(id); waiting > maxWaiting {
			maxWaiting = waiting
		}
	}
	queueEnabled := snapshot.Settings.CredentialQueueEnabled
	if !queueEnabled {
		// 排队关闭: 预算内重选, 全满则直接放行原候选(永不因 cap 拒绝)。
		id, ok := slots.AcquireAnyWithin(ids, preferredCredentialID, budgetPerCredential, 0, neverDone)
		if !ok {
			slots.ForceAcquire(selection.CredentialID)
			return selection, ref, true
		}
		return handler.selectionForBudgetCredential(snapshot, baseQuery, allowedCredentialRefs, id, selection, ref)
	}
	if maxWaiting >= budgetPerCredential*2 {
		// 防雪崩: 深度超限即拒绝, 不再堆等待者。
		handler.rejectQueued(ginContext, recorder, snapshot)
		return scheduler.Selection{}, state.CredentialRef{}, false
	}
	id, ok := slots.AcquireAnyWithin(
		ids, preferredCredentialID, budgetPerCredential,
		snapshot.Settings.CredentialQueueTimeout, ginContext.Request.Context().Done(),
	)
	if !ok {
		handler.rejectQueued(ginContext, recorder, snapshot)
		return scheduler.Selection{}, state.CredentialRef{}, false
	}
	return handler.selectionForBudgetCredential(snapshot, baseQuery, allowedCredentialRefs, id, selection, ref)
}

// selectionForBudgetCredential 把预算档选中的凭证还原为完整 Selection
// (channel/model/目标映射随凭证走)。作用域限定到单凭证的新迭代器复用
// 调度器全部解析逻辑; 重建失败回落原 selection 并强制占位, 不拒绝。
func (handler *Handler) selectionForBudgetCredential(
	snapshot *state.ConfigSnapshot,
	baseQuery scheduler.Query,
	allowedCredentialRefs map[uint]state.CredentialRef,
	credentialID uint,
	fallback scheduler.Selection,
	fallbackRef state.CredentialRef,
) (scheduler.Selection, state.CredentialRef, bool) {
	if credentialID == fallback.CredentialID {
		return fallback, fallbackRef, true
	}
	ref, allowed := allowedCredentialRefs[credentialID]
	if !allowed {
		handler.credentialSlots.ForceAcquire(fallback.CredentialID)
		return fallback, fallbackRef, true
	}
	scoped := baseQuery
	scoped.AllowedCredentialIDs = map[uint]struct{}{credentialID: {}}
	scoped.AllowedCredentialRefs = map[uint]state.CredentialRef{credentialID: ref}
	scoped.PreferredCredentialID = credentialID
	// 预算槽位已由调用方持有, 作用域内单候选无需重复过滤。
	scoped.CredentialSlots = nil
	scopedIterator := scheduler.New(snapshot, handler.registry, scoped)
	selection, err := scopedIterator.Next()
	if err != nil || selection.CredentialID != credentialID {
		handler.credentialSlots.ForceAcquire(fallback.CredentialID)
		return fallback, fallbackRef, true
	}
	return selection, ref, true
}

// neverDone 永不关闭的 done 通道, 用于排队关闭时的即时占位调用。
var neverDone <-chan struct{} = make(chan struct{})

func (handler *Handler) rejectQueued(
	ginContext *gin.Context,
	recorder *requestRecorder,
	snapshot *state.ConfigSnapshot,
) {
	setCooldownRetryAfter(ginContext, handler.now().Add(snapshot.Settings.CredentialQueueTimeout), handler.now())
	handler.completeReason(ginContext, recorder, reasonUpstreamRateLimited)
}

func (handler *Handler) resolveRequestAffinity(
	snapshot *state.ConfigSnapshot,
	accessKeyID uint,
	clientProtocol protocol.Protocol,
	prefix []byte,
	allowedCredentialRefs map[uint]state.CredentialRef,
	promptCacheKey string,
) requestAffinity {
	if handler == nil || snapshot == nil {
		return requestAffinity{}
	}
	key := affinity.DeriveKey(
		handler.encryption,
		accessKeyID,
		clientProtocol,
		prefix,
	)
	// 执行层私有 replay scope 仍由提示词派生，不把客户端缓存分组当作会话身份。
	result := requestAffinity{continuityKey: string(key), kind: telemetry.AffinityPromptPrefix}
	if promptCacheKey != "" {
		key = affinity.DerivePromptCacheKey(handler.encryption, accessKeyID, clientProtocol, promptCacheKey)
		result.kind = telemetry.AffinityPromptCacheKey
	}
	if handler.affinityCache == nil ||
		!handler.affinityCache.Configure(
			snapshot.Revision,
			snapshot.Settings.AffinityCapacity,
			snapshot.Settings.AffinityTTL,
		) {
		return result
	}
	if !key.Valid() {
		return result
	}
	result.key = key
	observation := handler.affinityCache.Lookup(key)
	resolved := result
	resolved.observation = observation
	if !observation.Found() {
		return resolved
	}
	target := observation.Target
	group, exists := snapshot.Groups[target.GroupID]
	if !exists || !group.AffinityEnabled {
		return resolved
	}
	ref, allowed := allowedCredentialRefs[target.CredentialID]
	if !allowed || ref.GroupID != target.GroupID ||
		ref.IdentityGeneration != target.IdentityGeneration {
		return resolved
	}
	resolved.preferredCredentialID = target.CredentialID
	return resolved
}

func (handler *Handler) recordAffinitySuccess(
	request requestAffinity,
	selection scheduler.Selection,
	ref state.CredentialRef,
) {
	if handler == nil || handler.affinityCache == nil || !request.key.Valid() ||
		!selection.Group.AffinityEnabled {
		return
	}
	handler.affinityCache.RecordSuccess(
		request.key,
		request.observation,
		affinity.Target{
			GroupID: selection.GroupID, CredentialID: selection.CredentialID,
			IdentityGeneration: ref.IdentityGeneration,
		},
	)
}
