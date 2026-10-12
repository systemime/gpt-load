package state

import "sync"

// CredentialUsage 记录单个凭证在两个滚动窗口内的消耗,
// 供用量均衡(阶段 3)计算相对份额压力。Credits 按官方积分公式
// (token×系数/10000, 非高峰半价)从 usage_stats 换算, 与厂商计费同口径。
type CredentialUsage struct {
	Requests5h int64
	Tokens5h   int64
	Requests7d int64
	Tokens7d   int64
	Credits5h  float64
	Credits7d  float64
}

// CredentialUsageTracker 是进程内的凭证用量快照表, 由控制面后台循环
// 从 request_logs 聚合刷新, 网关调度读取。重启冷启动为空, 一次刷新即恢复。
type CredentialUsageTracker struct {
	mu    sync.RWMutex
	usage map[uint]CredentialUsage
}

func NewCredentialUsageTracker() *CredentialUsageTracker {
	return &CredentialUsageTracker{usage: make(map[uint]CredentialUsage)}
}

// Refresh 整表替换为最新聚合结果。
func (tracker *CredentialUsageTracker) Refresh(usage map[uint]CredentialUsage) {
	if tracker == nil {
		return
	}
	tracker.mu.Lock()
	tracker.usage = usage
	tracker.mu.Unlock()
}

// Usage 返回该凭证当前窗口用量, 无记录时为零值(新凭证视为最低压力)。
func (tracker *CredentialUsageTracker) Usage(id uint) CredentialUsage {
	if tracker == nil {
		return CredentialUsage{}
	}
	tracker.mu.RLock()
	usage := tracker.usage[id]
	tracker.mu.RUnlock()
	return usage
}

// SnapshotAll 返回整表副本, 供负载端点展示。
func (tracker *CredentialUsageTracker) SnapshotAll() map[uint]CredentialUsage {
	if tracker == nil {
		return map[uint]CredentialUsage{}
	}
	tracker.mu.RLock()
	defer tracker.mu.RUnlock()
	snapshot := make(map[uint]CredentialUsage, len(tracker.usage))
	for id, usage := range tracker.usage {
		snapshot[id] = usage
	}
	return snapshot
}
