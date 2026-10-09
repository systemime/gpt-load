package state

import (
	"sync"
	"sync/atomic"
)

// CredentialSlots 统计每个凭证的实时在途请求数, 并按全局 cap 做准入控制。
// cap 来源于 credential_concurrency_limit 运行时设置; 0 表示不限制。
// 计数器而非信号量: cap 变更只改阈值, 在途请求自然排空, 无重建与释放配对问题。
type CredentialSlots struct {
	capacity atomic.Int64 // <=0 表示不限制
	mu       sync.RWMutex
	counters map[uint]*atomic.Int64
}

func NewCredentialSlots(capacity int) *CredentialSlots {
	slots := &CredentialSlots{counters: make(map[uint]*atomic.Int64)}
	slots.capacity.Store(int64(capacity))
	return slots
}

// Configure 更新全局 cap。容量是准入阈值而非资源池, 变更即时生效,
// 已在途的请求继续按旧计数运行至归还。
func (slots *CredentialSlots) Configure(capacity int) {
	if slots == nil {
		return
	}
	slots.capacity.Store(int64(capacity))
}

func (slots *CredentialSlots) Capacity() int {
	if slots == nil {
		return 0
	}
	return int(slots.capacity.Load())
}

func (slots *CredentialSlots) counter(id uint) *atomic.Int64 {
	slots.mu.RLock()
	counter := slots.counters[id]
	slots.mu.RUnlock()
	if counter != nil {
		return counter
	}
	slots.mu.Lock()
	defer slots.mu.Unlock()
	if counter := slots.counters[id]; counter != nil {
		return counter
	}
	counter = &atomic.Int64{}
	slots.counters[id] = counter
	return counter
}

// TryAcquire 在途数未达 cap 时占一个槽位。不限制时恒成功。
func (slots *CredentialSlots) TryAcquire(id uint) bool {
	if slots == nil {
		return true
	}
	capacity := slots.capacity.Load()
	if capacity <= 0 {
		slots.counter(id).Add(1)
		return true
	}
	counter := slots.counter(id)
	for {
		current := counter.Load()
		if current >= capacity {
			return false
		}
		if counter.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

// Release 归还一个槽位。计数器缺失或已归零时静默忽略, 防止负数。
func (slots *CredentialSlots) Release(id uint) {
	if slots == nil {
		return
	}
	slots.mu.RLock()
	counter := slots.counters[id]
	slots.mu.RUnlock()
	if counter == nil {
		return
	}
	for {
		current := counter.Load()
		if current <= 0 {
			return
		}
		if counter.CompareAndSwap(current, current-1) {
			return
		}
	}
}

// Inflight 返回该凭证当前在途请求数。
func (slots *CredentialSlots) Inflight(id uint) int64 {
	if slots == nil {
		return 0
	}
	slots.mu.RLock()
	counter := slots.counters[id]
	slots.mu.RUnlock()
	if counter == nil {
		return 0
	}
	return counter.Load()
}

// Available 报告该凭证是否仍在准入阈值内, 供调度器过滤候选。
func (slots *CredentialSlots) Available(id uint) bool {
	if slots == nil {
		return true
	}
	capacity := slots.capacity.Load()
	if capacity <= 0 {
		return true
	}
	return slots.Inflight(id) < capacity
}
