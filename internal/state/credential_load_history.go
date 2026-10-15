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
	// SilentTransfers 是该采样间隔内"无感转移"次数(换下一个凭证重试),
	// 由网关在 AdvancePriority 处累计, 采样时取累计值差值。
	SilentTransfers int64 `json:"silent_transfers"`
}

type loadHistory struct {
	mu      sync.Mutex
	ring    [LoadHistoryCapacity]LoadHistorySample
	written uint64
	silent  atomic.Int64
	last    int64
}

// CountSilentTransfer 网关侧累计一次跨凭证无感转移。
func (r *CredentialRegistry) CountSilentTransfer() {
	if r == nil || r.loadHistory == nil {
		return
	}
	r.loadHistory.silent.Add(1)
}

// SilentTransfers 返回累计无感转移次数。
func (r *CredentialRegistry) SilentTransfers() int64 {
	if r == nil || r.loadHistory == nil {
		return 0
	}
	return r.loadHistory.silent.Load()
}

// RecordLoadSample 追加一个采样点; silent 字段由本方法按累计值差值填写。
func (r *CredentialRegistry) RecordLoadSample(atMS int64, inflight, queued int64) {
	if r == nil || r.loadHistory == nil {
		return
	}
	h := r.loadHistory
	total := h.silent.Load()
	h.mu.Lock()
	defer h.mu.Unlock()
	index := int(h.written % LoadHistoryCapacity)
	h.ring[index] = LoadHistorySample{
		AtMS: atMS, Inflight: inflight, Queued: queued,
		SilentTransfers: total - h.last,
	}
	h.last = total
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
