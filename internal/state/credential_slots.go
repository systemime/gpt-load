package state

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// CredentialSlots 统计每个凭证的实时在途占用(按模型槽位权重加权, 见 SlotWeight),
// 并按全局 cap 做准入控制。cap 来源于 credential_concurrency_limit 运行时设置;
// 0 表示不限制。计数器而非信号量: cap 变更只改阈值, 在途请求自然排空,
// 无重建与释放配对问题。acquire/release 必须传同一权重, 否则计数漂移。
type CredentialSlots struct {
	capacity atomic.Int64 // 全局默认 cap, <=0 表示不限制
	mu       sync.RWMutex
	counters map[uint]*atomic.Int64
	waiting  map[uint]*atomic.Int64
	limits   map[uint]int64 // 每凭证覆盖: >0 生效, 覆盖全局 cap
	notify   chan struct{}  // Release 时关闭并换新, 等待者借此唤醒
}

func NewCredentialSlots(capacity int) *CredentialSlots {
	slots := &CredentialSlots{
		counters: make(map[uint]*atomic.Int64),
		waiting:  make(map[uint]*atomic.Int64),
		limits:   make(map[uint]int64),
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

// SetLimits 整表替换每凭证 cap 覆盖(仅保留 >0 项)。注册表在
// ReplaceCredentials 时同步, 与凭证增删一致。
func (slots *CredentialSlots) SetLimits(limits map[uint]int64) {
	if slots == nil {
		return
	}
	filtered := make(map[uint]int64, len(limits))
	for id, limit := range limits {
		if limit > 0 {
			filtered[id] = limit
		}
	}
	slots.mu.Lock()
	slots.limits = filtered
	slots.mu.Unlock()
}

// limitFor 返回该凭证生效的 cap: 覆盖 > 0 时覆盖全局, 否则用全局值。
func (slots *CredentialSlots) limitFor(id uint) int64 {
	if slots == nil {
		return 0
	}
	slots.mu.RLock()
	override := slots.limits[id]
	slots.mu.RUnlock()
	if override > 0 {
		return override
	}
	return slots.capacity.Load()
}

// BudgetFor 返回该凭证的预算档 = 生效 cap + 固定浮动; 不限时为极大值。
func (slots *CredentialSlots) BudgetFor(id uint) int64 {
	limit := slots.limitFor(id)
	if limit <= 0 {
		return math.MaxInt64
	}
	return limit + CredentialQueueSlack
}

// EffectiveLimit 返回该凭证生效 cap, 供展示。
func (slots *CredentialSlots) EffectiveLimit(id uint) int64 {
	return slots.limitFor(id)
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

// normalizeSlotWeight 权重下限 1, 防御 0/负值造成的计数不增。
func normalizeSlotWeight(weight int64) int64 {
	if weight < 1 {
		return 1
	}
	return weight
}

// TryAcquire 在途占用未达 cap 时按 weight 占位。不限制时恒成功。
func (slots *CredentialSlots) TryAcquire(id uint, weight int64) bool {
	if slots == nil {
		return true
	}
	limit := slots.limitFor(id)
	if limit <= 0 {
		slots.counter(id).Add(normalizeSlotWeight(weight))
		return true
	}
	return slots.TryAcquireWithin(id, weight, limit)
}

// TryAcquireWithin 在途占用未达显式 limit 时按 weight 占位, limit<=0 视为不限制。
// 用于 cap+slack 的组预算档位。
func (slots *CredentialSlots) TryAcquireWithin(id uint, weight int64, limit int64) bool {
	if slots == nil {
		return true
	}
	weight = normalizeSlotWeight(weight)
	if limit <= 0 {
		slots.counter(id).Add(weight)
		return true
	}
	counter := slots.counter(id)
	for {
		current := counter.Load()
		if current+weight > limit {
			return false
		}
		if counter.CompareAndSwap(current, current+weight) {
			return true
		}
	}
}

// AcquireWithinWait 等待该凭证在途占用降到 limit-weight 之下并占位, 直到超时或 done 关闭。
// 返回 false 表示未等到。唤醒为广播+重试, 唤醒顺序是先醒先得的 FIFO 近似,
// ponytail: 无全局票号, 高并发同凭证等待下可能乱序, 需要严格 FIFO 再加票闸。
func (slots *CredentialSlots) AcquireWithinWait(
	id uint,
	weight int64,
	limit int64,
	timeout time.Duration,
	done <-chan struct{},
) bool {
	if slots == nil {
		return true
	}
	if slots.TryAcquireWithin(id, weight, limit) {
		return true
	}
	waiter := slots.waiterCount(id)
	waiter.Add(1)
	defer waiter.Add(-1)
	deadline := time.After(timeout)
	for {
		// 先取通道再尝试: 尝试失败后 select 期间发生的释放必然关闭本通道,
		// 消除"检查与登记之间"的丢失唤醒窗口。
		notify := slots.notifyChannel()
		if slots.TryAcquireWithin(id, weight, limit) {
			return true
		}
		select {
		case <-notify:
		case <-deadline:
			return false
		case <-done:
			return false
		}
	}
}

// AcquireAnyWithin 在候选集内任一凭证在途占用降到预算档内时按 weight 占位并返回该凭证,
// 直到超时或 done 关闭。每次唤醒都重新评估全部候选(优先 preferred, 其次在途最小),
// 不锁定入队时刻的选择。等待者计数登记在全部候选上, 深度守卫按单凭证最大等待数计。
func (slots *CredentialSlots) AcquireAnyWithin(
	ids []uint,
	preferred uint,
	weight int64,
	timeout time.Duration,
	done <-chan struct{},
) (uint, bool) {
	if slots == nil || len(ids) == 0 {
		return 0, false
	}
	if id, ok := slots.tryAcquireAnyWithin(ids, preferred, weight); ok {
		return id, true
	}
	waiters := make([]*atomic.Int64, 0, len(ids))
	for _, id := range ids {
		waiters = append(waiters, slots.waiterCount(id))
		waiters[len(waiters)-1].Add(1)
	}
	defer func() {
		for _, waiter := range waiters {
			waiter.Add(-1)
		}
	}()
	deadline := time.After(timeout)
	for {
		// 先取通道再尝试, 同 AcquireWithinWait 的丢失唤醒修复。
		notify := slots.notifyChannel()
		if id, ok := slots.tryAcquireAnyWithin(ids, preferred, weight); ok {
			return id, true
		}
		select {
		case <-notify:
		case <-deadline:
			return 0, false
		case <-done:
			return 0, false
		}
	}
}

// tryAcquireAnyWithin 按优先序尝试候选: preferred 最先, 其余按传入顺序。
// 顺序由调用方决定(网关按综合负载分排序), 等待唤醒后复用同一顺序。
// 返回成功占位的凭证; 全部达 limit 时 ok=false。
func (slots *CredentialSlots) tryAcquireAnyWithin(ids []uint, preferred uint, weight int64) (uint, bool) {
	if preferred != 0 {
		if slots.TryAcquireWithin(preferred, weight, slots.BudgetFor(preferred)) {
			return preferred, true
		}
	}
	for _, id := range ids {
		if id == 0 || id == preferred {
			continue
		}
		if slots.TryAcquireWithin(id, weight, slots.BudgetFor(id)) {
			return id, true
		}
	}
	return 0, false
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

// ForceAcquire 无视 cap 直接按 weight 占位。仅在调度器判定全池达 cap、
// 豁免放行候选时使用, 保证 cap 永不制造不可用。
func (slots *CredentialSlots) ForceAcquire(id uint, weight int64) {
	if slots == nil {
		return
	}
	slots.counter(id).Add(normalizeSlotWeight(weight))
}

// Release 按 weight 归还槽位并唤醒等待者。计数器缺失或已归零时静默忽略;
// 钳制在 0, 防御配对错误导致的负数漂移。
func (slots *CredentialSlots) Release(id uint, weight int64) {
	if slots == nil {
		return
	}
	slots.mu.RLock()
	counter := slots.counters[id]
	slots.mu.RUnlock()
	if counter == nil {
		return
	}
	weight = normalizeSlotWeight(weight)
	for {
		current := counter.Load()
		if current <= 0 {
			return
		}
		updated := current - weight
		if updated < 0 {
			updated = 0
		}
		if counter.CompareAndSwap(current, updated) {
			slots.mu.Lock()
			slots.broadcastLocked()
			slots.mu.Unlock()
			return
		}
	}
}

// Inflight 返回该凭证当前在途占用(加权槽位数, 非请求数)。
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

// Available 报告该凭证是否有容纳 weight 的空闲额度, 供调度器过滤候选。
func (slots *CredentialSlots) Available(id uint, weight int64) bool {
	if slots == nil {
		return true
	}
	limit := slots.limitFor(id)
	if limit <= 0 {
		return true
	}
	return slots.Inflight(id)+normalizeSlotWeight(weight) <= limit
}
