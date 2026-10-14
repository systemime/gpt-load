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
		scores, scoresActive := iterator.usageScores(eligible)
		// 非亲和候选软排除: inflight 已过 cap-2 档(阶段2高载)的凭证不再接受
		// 新会话/无亲和分配, 亲和钉住由下方 preferred 覆盖继续放行到 cap。
		selection := eligible
		if relaxed := excludeHighLoadCredentials(iterator, eligible); len(relaxed) > 0 {
			selection = relaxed
		}
		first := selection[0]
		for _, candidate := range selection[1:] {
			if scoresActive {
				if scores[candidate.meta.ID] < scores[first.meta.ID] {
					first = candidate
					continue
				}
				if scores[candidate.meta.ID] > scores[first.meta.ID] {
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

// usageScores 计算候选集内每凭证的综合负载分 = (在途/cap) × P,
// P = 该凭证 5h token 消耗 / 池内最大 5h token 消耗(裸 token, 官方积分口径不透明不采用)。
// 分数越小越优先; 全零消耗或均衡开关关闭时返回 false 回落公平序。
// 只看 5h: 这是厂商的第一层额度窗口, 周维度由停用/权重管理。
func (iterator *Iterator) usageScores(eligible []weightedCredential) (map[uint]float64, bool) {
	if iterator.credentialUsage == nil || !iterator.usageBalance || len(eligible) < 2 {
		return nil, false
	}
	ids := make([]uint, 0, len(eligible))
	for _, candidate := range eligible {
		ids = append(ids, candidate.meta.ID)
	}
	return UsageScores(ids, iterator.credentialSlots, iterator.credentialUsage)
}

// UsageScores 是综合负载分的唯一实现, 网关超发区排序与调度器共用,
// 保证阶段2(cap-2~cap)与阶段3(cap~cap+2)用同一把尺子。全零消耗返回 false。
func UsageScores(ids []uint, slots *state.CredentialSlots, usage CredentialUsageView) (map[uint]float64, bool) {
	usages := make(map[uint]state.CredentialUsage, len(ids))
	var maxTokens5h float64
	for _, id := range ids {
		value := usage.Usage(id)
		usages[id] = value
		if float64(value.Tokens5h) > maxTokens5h {
			maxTokens5h = float64(value.Tokens5h)
		}
	}
	if maxTokens5h <= 0 {
		return nil, false
	}
	scores := make(map[uint]float64, len(ids))
	for _, id := range ids {
		pressure := float64(usages[id].Tokens5h) / maxTokens5h
		if slots == nil {
			scores[id] = pressure
			continue
		}
		limit := slots.EffectiveLimit(id)
		if limit <= 0 {
			scores[id] = pressure
			continue
		}
		scores[id] = (float64(slots.Inflight(id)) / float64(limit)) * pressure
	}
	return scores, true
}

// excludeHighLoadCredentials 返回未越过高载线(inflight <= cap-2)的候选;
// 空结果表示全部高载(此时调用方回退全集)。无槽位/不限发场景返回 nil。
func excludeHighLoadCredentials(iterator *Iterator, eligible []weightedCredential) []weightedCredential {
	if iterator.credentialSlots == nil {
		return nil
	}
	var relaxed []weightedCredential
	for _, candidate := range eligible {
		id := candidate.meta.ID
		limit := iterator.credentialSlots.EffectiveLimit(id)
		if limit < 3 || iterator.credentialSlots.Inflight(id) <= limit-2 {
			// cap<=2 时 cap-2 无实际容量意义, 视为未高载。
			relaxed = append(relaxed, candidate)
		}
	}
	return relaxed
}
