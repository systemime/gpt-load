package scheduler

import (
	"time"

	"gpt-load/internal/state"
)

// ChargeReplay 为已允许的显式同凭据重放取得名额，不放松普通重试的 tried 去重。
func (iterator *Iterator) ChargeReplay(selection Selection, ref state.CredentialRef) bool {
	if ref.ID != selection.CredentialID || ref.GroupID != selection.GroupID {
		return false
	}
	var charged bool
	consume := func(metas []state.CredentialMeta) {
		for _, meta := range metas {
			if meta.ID != ref.ID || meta.IdentityGeneration != ref.IdentityGeneration {
				continue
			}
			if selection.UpstreamModelID != nil && modelCooldownUntil(meta.ModelCooldowns, *selection.UpstreamModelID, iterator.operation, iterator.now()).After(iterator.now()) {
				continue
			}
			weight := effectiveWeight(selection.Group.WeightManual, meta.WeightManual)
			if weight > 0 {
				_, charged = iterator.selectCredential([]weightedCredential{{meta: meta, weight: weight}}, ref.ID)
			}
		}
	}
	groups := []uint{ref.GroupID}
	if source, ok := iterator.credentials.(interface {
		WithCredentialCandidates([]uint, func(uint) bool, time.Time, func([]state.CredentialMeta))
	}); ok {
		source.WithCredentialCandidates(groups, nil, iterator.now(), consume)
	} else {
		consume(iterator.credentials.CollectCredentialCandidates(groups, nil, iterator.now()))
	}
	return charged
}

// selectCredential 只操作内存；真实 Registry 调用方同时持有凭据读锁。
func (iterator *Iterator) selectCredential(candidates []weightedCredential, preferred uint) (state.CredentialMeta, bool) {
	var selected state.CredentialMeta
	var found bool
	iterator.progress.WithLock(func(ledger *state.SchedulingLedger) {
		eligible := candidates[:0]
		baseline := ledger.Watermark
		hasBaseline := false
		for _, candidate := range candidates {
			member := ledger.Members[candidate.meta.ID]
			if member == nil || member.GroupID != candidate.meta.GroupID ||
				member.IdentityGeneration != candidate.meta.IdentityGeneration {
				continue
			}
			eligible = append(eligible, candidate)
			if !member.Pending && (!hasBaseline || member.Progress.Compare(baseline) > 0) {
				baseline, hasBaseline = member.Progress, true
			}
		}
		if len(eligible) == 0 || ledger.Sequence == ^uint64(0) {
			return
		}
		// 同批成员共用已有成员的领先进度，不能继承落后成员的历史欠额。
		for _, candidate := range eligible {
			member := ledger.Members[candidate.meta.ID]
			// 旧请求可以继续记账，但不能用过期候选池完成新配置的入场校准。
			if member.Pending && (!ledger.GroupsKnown || ledger.Groups[member.GroupID]) &&
				iterator.snapshot != nil && iterator.snapshot.Revision >= ledger.GroupRevision {
				member.Admit(baseline)
			}
		}
		less := func(left, right weightedCredential) bool {
			a, b := ledger.Members[left.meta.ID], ledger.Members[right.meta.ID]
			if order := a.Progress.Compare(b.Progress); order != 0 {
				return order < 0
			}
			if a.LastSelected != b.LastSelected {
				return a.LastSelected < b.LastSelected
			}
			return a.ID < b.ID
		}
		pressure, pressureActive := iterator.usagePressures(eligible)
		first := eligible[0]
		for _, candidate := range eligible[1:] {
			if pressureActive {
				if pressure[candidate.meta.ID] < pressure[first.meta.ID] {
					first = candidate
					continue
				}
				if pressure[candidate.meta.ID] > pressure[first.meta.ID] {
					continue
				}
			}
			if less(candidate, first) {
				first = candidate
			}
		}
		preferredFound := false
		for _, candidate := range eligible {
			if candidate.meta.ID == preferred {
				first, preferredFound = candidate, true
				break
			}
		}
		if !preferredFound && ledger.LastMember == first.meta.ID && len(eligible) > 1 && ledger.Consecutive >= 100 {
			var alternative weightedCredential
			for _, candidate := range eligible {
				if candidate.meta.ID != first.meta.ID && (alternative.meta.ID == 0 || less(candidate, alternative)) {
					alternative = candidate
				}
			}
			limit := uint64(99 + (first.weight+alternative.weight-1)/alternative.weight)
			if ledger.Consecutive >= limit {
				first = alternative
			}
		}
		member := ledger.Members[first.meta.ID]
		next, ok := member.Progress.Advance(uint64(first.weight))
		if !ok {
			return
		}
		member.Progress = next
		ledger.Sequence++
		member.LastSelected = ledger.Sequence
		ledger.Started = true
		if next.Compare(ledger.Watermark) > 0 {
			ledger.Watermark = next
		}
		if ledger.LastMember != member.ID {
			ledger.LastMember, ledger.Consecutive = member.ID, 1
		} else if ledger.Consecutive < 99+state.MaxWeight*state.MaxWeight {
			// 超过所有合法阈值后无需继续增长；亲和和单候选仍可持续分配。
			ledger.Consecutive++
		}
		selected, found = first.meta, true
	})
	return selected, found
}

// usagePressures 计算候选集内每凭证的用量压力 = max(各窗口/各指标的实际份额/目标份额),
// 目标份额取有效权重占比。任一维度有数据即激活; 全零返回 false 回落公平序。
// token 指标使用官方积分口径(模型系数 + 缓存命中折扣 + 非高峰半价),
// 缓存热凭证积分份额低, 均衡器自然偏向缓存命中率高的一侧。
// preferred(亲和)优先级在其之上, 已钉住的会话不受用量均衡影响。
func (iterator *Iterator) usagePressures(eligible []weightedCredential) (map[uint]float64, bool) {
	if iterator.credentialUsage == nil || !iterator.usageBalance || len(eligible) < 2 {
		return nil, false
	}
	usages := make(map[uint]state.CredentialUsage, len(eligible))
	var totalWeight, requests5h, credits5h, requests7d, credits7d float64
	for _, candidate := range eligible {
		usage := iterator.credentialUsage.Usage(candidate.meta.ID)
		usages[candidate.meta.ID] = usage
		totalWeight += float64(candidate.weight)
		requests5h += float64(usage.Requests5h)
		credits5h += usage.Credits5h
		requests7d += float64(usage.Requests7d)
		credits7d += usage.Credits7d
	}
	if requests5h <= 0 && credits5h <= 0 && requests7d <= 0 && credits7d <= 0 {
		return nil, false
	}
	share := func(part float64, total float64, target float64) float64 {
		if total <= 0 || target <= 0 {
			return 0
		}
		return (part / total) / target
	}
	pressure := make(map[uint]float64, len(eligible))
	for _, candidate := range eligible {
		usage := usages[candidate.meta.ID]
		target := float64(candidate.weight) / totalWeight
		value := share(float64(usage.Requests5h), requests5h, target)
		if next := share(usage.Credits5h, credits5h, target); next > value {
			value = next
		}
		if next := share(float64(usage.Requests7d), requests7d, target); next > value {
			value = next
		}
		if next := share(usage.Credits7d, credits7d, target); next > value {
			value = next
		}
		pressure[candidate.meta.ID] = value
	}
	return pressure, true
}
