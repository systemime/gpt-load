package state

import (
	"sync"

	"gpt-load/internal/platform/epochms"
)

// 厂商 5h 额度窗口锚点: 每凭证记录 {窗口锚点, 最近活跃}。
// 语义(运营约定):
//   - 锚点为空: 窗口未激活, 已用 token 记 0, 凭证收到首个请求时以该时刻开窗;
//   - 锚点存在: 窗口起点 = 锚点 + 5h*n, 即"沿用前一天累计的时段划分";
//   - 每天 07:00(Asia/Shanghai) 检查: 空闲 ≥5h 则清空锚点, 等下一次分配重新开窗。
//
// 锚点通过运行时 checkpoint 持久化, 重启不丢。
type credentialWindowState struct {
	anchorMS   int64
	activityMS int64
}

type credentialWindowAnchors struct {
	mu     sync.Mutex
	states map[uint]*credentialWindowState
}

func (w *credentialWindowAnchors) state(credentialID uint) *credentialWindowState {
	if state, ok := w.states[credentialID]; ok {
		return state
	}
	state := &credentialWindowState{}
	w.states[credentialID] = state
	return state
}

// BeginWindowIfNone 锚点为空时以 atMS 开窗, 返回是否新建了窗口。
// 网关在把请求分配给该凭证时调用。
func (r *CredentialRegistry) BeginWindowIfNone(credentialID uint, atMS int64) bool {
	if r == nil || r.windowAnchors == nil {
		return false
	}
	w := r.windowAnchors
	w.mu.Lock()
	defer w.mu.Unlock()
	state := w.state(credentialID)
	state.activityMS = atMS
	if state.anchorMS != 0 {
		return false
	}
	state.anchorMS = atMS
	return true
}

// WindowAnchors 返回全部非空锚点快照, 供用量对齐与 07:00 检查。
func (r *CredentialRegistry) WindowAnchors() map[uint]int64 {
	if r == nil || r.windowAnchors == nil {
		return nil
	}
	w := r.windowAnchors
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[uint]int64, len(w.states))
	for id, state := range w.states {
		if state.anchorMS != 0 {
			out[id] = state.anchorMS
		}
	}
	return out
}

// ResetIdleWindowAnchors 执行每日 07:00 检查: 空闲 ≥5h 的凭证清空锚点。
// 返回被重置的凭证 ID 列表。
func (r *CredentialRegistry) ResetIdleWindowAnchors(nowMS int64) []uint {
	if r == nil || r.windowAnchors == nil {
		return nil
	}
	w := r.windowAnchors
	w.mu.Lock()
	defer w.mu.Unlock()
	reset := make([]uint, 0)
	for id, state := range w.states {
		if state.anchorMS == 0 {
			continue
		}
		if state.activityMS == 0 || nowMS-state.activityMS >= epochms.MillisecondsPerHour*5 {
			state.anchorMS = 0
			reset = append(reset, id)
		}
	}
	return reset
}

// CaptureWindowAnchors checkpoint 用: 导出全部锚点与活跃时间。
func (r *CredentialRegistry) CaptureWindowAnchors() map[uint][2]int64 {
	if r == nil || r.windowAnchors == nil {
		return nil
	}
	w := r.windowAnchors
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[uint][2]int64, len(w.states))
	for id, state := range w.states {
		if state.anchorMS == 0 && state.activityMS == 0 {
			continue
		}
		out[id] = [2]int64{state.anchorMS, state.activityMS}
	}
	return out
}

// RestoreWindowAnchors checkpoint 恢复。
func (r *CredentialRegistry) RestoreWindowAnchors(states map[uint][2]int64) {
	if r == nil || r.windowAnchors == nil || len(states) == 0 {
		return
	}
	w := r.windowAnchors
	w.mu.Lock()
	defer w.mu.Unlock()
	for id, pair := range states {
		state := w.state(id)
		state.anchorMS = pair[0]
		state.activityMS = pair[1]
	}
}
