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

// admitBeyondCap 处理全池达 cap 的豁免候选(阶段 2 排队准入):
//   - 单凭证预算 = cap + CredentialQueueSlack;
//   - 组预算内先到先得直接放行;
//   - 超出组预算的请求在本凭证上排队等待(等的就是原凭证槽位, 亲和缓存无损);
//   - 排队深度超过组预算两倍, 或等待超时, 返回 429 + Retry-After。
//
// 返回 false 表示请求已写回拒绝响应, 调用方直接结束。
func (handler *Handler) admitBeyondCap(
	ginContext *gin.Context,
	recorder *requestRecorder,
	snapshot *state.ConfigSnapshot,
	allowedCredentialRefs map[uint]state.CredentialRef,
	selection scheduler.Selection,
) bool {
	slots := handler.credentialSlots
	capacity := slots.Capacity()
	if capacity <= 0 {
		slots.ForceAcquire(selection.CredentialID)
		return true
	}
	budgetPerCredential := int64(capacity) + state.CredentialQueueSlack
	queueEnabled := snapshot.Settings.CredentialQueueEnabled
	queued := int64(0)
	members := 0
	for id := range allowedCredentialRefs {
		if id == 0 {
			continue
		}
		members++
		queued += slots.Waiting(id)
	}
	groupBudget := int64(members) * budgetPerCredential
	if !queueEnabled || queued >= groupBudget*2 {
		// 防雪崩: 深度超限即拒绝, 不再堆等待者。
		handler.rejectQueued(ginContext, recorder, snapshot)
		return false
	}
	timeout := snapshot.Settings.CredentialQueueTimeout
	if !slots.AcquireWithinWait(
		selection.CredentialID, budgetPerCredential, timeout, ginContext.Request.Context().Done(),
	) {
		handler.rejectQueued(ginContext, recorder, snapshot)
		return false
	}
	return true
}

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
