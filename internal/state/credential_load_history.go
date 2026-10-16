package state

import (
	"sync"
	"sync/atomic"
)

// 负载页折线图数据: 1s 采样环形缓冲, 5 分钟窗。控制面后台循环写入,
// 负载端点展示 —— 与 Slots/UsageTracker 同一归属(注册表内嵌, 双端共享)。
const (
	LoadHistoryCapacity   = 300
	LoadHistoryIntervalMS = 1000
)

type LoadHistorySample struct {
	AtMS     int64 `json:"at_ms"`
	Inflight int64 `json:"inflight"`
	Queued   int64 `json:"queued"`
	// 无感转移的两个结果口径(次/秒): 网关在请求结束时按
	// "该请求内的转移次数 × 最终成败"结算, 采样取累计值差值。
	// 转移后请求成功 → 成功数; 失败/被取消 → 失败数。
	SilentSuccess int64 `json:"silent_success"`
	SilentFailed  int64 `json:"silent_failed"`
}

type loadHistory struct {
	mu          sync.Mutex
	ring        [LoadHistoryCapacity]LoadHistorySample
	written     uint64
	success     atomic.Int64
	failed      atomic.Int64
	lastSuccess int64
	lastFailed  int64
}

// CountSilentTransferOutcomes 网关在请求终态时结算其无感转移次数。
func (r *CredentialRegistry) CountSilentTransferOutcomes(success, failed int64) {
	if r == nil || r.loadHistory == nil {
		return
	}
	if success > 0 {
		r.loadHistory.success.Add(success)
	}
	if failed > 0 {
		r.loadHistory.failed.Add(failed)
	}
}

// SilentTransferOutcomes 返回累计的(成功, 失败)转移次数, 供测试与诊断。
func (r *CredentialRegistry) SilentTransferOutcomes() (int64, int64) {
	if r == nil || r.loadHistory == nil {
		return 0, 0
	}
	return r.loadHistory.success.Load(), r.loadHistory.failed.Load()
}

// RecordLoadSample 追加一个采样点; 两个转移字段按累计值差值填写。
func (r *CredentialRegistry) RecordLoadSample(atMS int64, inflight, queued int64) {
	if r == nil || r.loadHistory == nil {
		return
	}
	h := r.loadHistory
	success := h.success.Load()
	failed := h.failed.Load()
	h.mu.Lock()
	defer h.mu.Unlock()
	index := int(h.written % LoadHistoryCapacity)
	h.ring[index] = LoadHistorySample{
		AtMS: atMS, Inflight: inflight, Queued: queued,
		SilentSuccess: success - h.lastSuccess,
		SilentFailed:  failed - h.lastFailed,
	}
	h.lastSuccess = success
	h.lastFailed = failed
	h.written++
}

// LoadHistorySnapshot 返回按时间升序的已填充采样点; 无数据时空数组非 nil,
// 保证端点 JSON 是 [] 而不是 null。
func (r *CredentialRegistry) LoadHistorySnapshot() []LoadHistorySample {
	if r == nil || r.loadHistory == nil {
		return []LoadHistorySample{}
	}
	h := r.loadHistory
	h.mu.Lock()
	defer h.mu.Unlock()
	count := int(h.written)
	if count == 0 {
		return []LoadHistorySample{}
	}
	if count > LoadHistoryCapacity {
		count = LoadHistoryCapacity
	}
	samples := make([]LoadHistorySample, 0, count)
	start := int(h.written) - count
	for i := start; i < int(h.written); i++ {
		samples = append(samples, h.ring[i%LoadHistoryCapacity])
	}
	return samples
}
