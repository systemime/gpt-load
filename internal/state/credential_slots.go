package state

import (
	"sync"
	"sync/atomic"
	"time"
)

// CredentialSlots 统计每个凭证的实时在途请求数, 并按全局 cap 做准入控制。
// cap 来源于 credential_concurrency_limit 运行时设置; 0 表示不限制。
// 计数器而非信号量: cap 变更只改阈值, 在途请求自然排空, 无重建与释放配对问题。
type CredentialSlots struct {
	capacity atomic.Int64 // <=0 表示不限制
	mu       sync.RWMutex
	counters map[uint]*atomic.Int64
	waiting  map[uint]*atomic.Int64
	notify   chan struct{} // Release 时关闭并换新, 等待者借此唤醒
}

func NewCredentialSlots(capacity int) *CredentialSlots {
	slots := &CredentialSlots{
		counters: make(map[uint]*atomic.Int64),
		waiting:  make(map[uint]*atomic.Int64),
		notify:   make(chan struct{}),
	}
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
	return slots.counterOrWaiting(id, slots.counters)
}

func (slots *CredentialSlots) waiterCount(id uint) *atomic.Int64 {
	return slots.counterOrWaiting(id, slots.waiting)
}

func (slots *CredentialSlots) counterOrWaiting(id uint, table map[uint]*atomic.Int64) *atomic.Int64 {
	slots.mu.RLock()
	counter := table[id]
	slots.mu.RUnlock()
	if counter != nil {
		return counter
	}
	slots.mu.Lock()
	defer slots.mu.Unlock()
	if counter := table[id]; counter != nil {
		return counter
	}
	counter = &atomic.Int64{}
	table[id] = counter
	return counter
}

func (slots *CredentialSlots) notifyChannel() <-chan struct{} {
	slots.mu.RLock()
	defer slots.mu.RUnlock()
	return slots.notify
}

func (slots *CredentialSlots) broadcastLocked() {
	close(slots.notify)
	slots.notify = make(chan struct{})
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
	return slots.TryAcquireWithin(id, capacity)
}

// TryAcquireWithin 在途数未达显式 limit 时占一个槽位, limit<=0 视为不限制。
// 用于 cap+slack 的组预算档位。
func (slots *CredentialSlots) TryAcquireWithin(id uint, limit int64) bool {
	if slots == nil {
		return true
	}
	if limit <= 0 {
		slots.counter(id).Add(1)
		return true
	}
	counter := slots.counter(id)
	for {
		current := counter.Load()
		if current >= limit {
			return false
		}
		if counter.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

// AcquireWithinWait 等待该凭证在途降到 limit 之下并占位, 直到超时或 done 关闭。
// 返回 false 表示未等到。唤醒为广播+重试, 唤醒顺序是先醒先得的 FIFO 近似,
// ponytail: 无全局票号, 高并发同凭证等待下可能乱序, 需要严格 FIFO 再加票闸。
func (slots *CredentialSlots) AcquireWithinWait(
	id uint,
	limit int64,
	timeout time.Duration,
	done <-chan struct{},
) bool {
	if slots == nil {
		return true
	}
	if slots.TryAcquireWithin(id, limit) {
		return true
	}
	waiter := slots.waiterCount(id)
	waiter.Add(1)
	defer waiter.Add(-1)
	deadline := time.After(timeout)
	for {
		if slots.TryAcquireWithin(id, limit) {
			return true
		}
		notify := slots.notifyChannel()
		select {
		case <-notify:
		case <-deadline:
			return false
		case <-done:
			return false
		}
	}
}

// Waiting 返回该凭证当前排队等待准入的请求数。
func (slots *CredentialSlots) Waiting(id uint) int64 {
	if slots == nil {
		return 0
	}
	slots.mu.RLock()
	waiter := slots.waiting[id]
	slots.mu.RUnlock()
	if waiter == nil {
		return 0
	}
	return waiter.Load()
}

// ForceAcquire 无视 cap 直接占一个槽位。仅在调度器判定全池达 cap、
// 豁免放行候选时使用, 保证 cap 永不制造不可用。
func (slots *CredentialSlots) ForceAcquire(id uint) {
	if slots == nil {
		return
	}
	slots.counter(id).Add(1)
}

// Release 归还一个槽位并唤醒等待者。计数器缺失或已归零时静默忽略, 防止负数。
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
			slots.mu.Lock()
			slots.broadcastLocked()
			slots.mu.Unlock()
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
