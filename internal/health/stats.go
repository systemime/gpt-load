package health

import (
	"sync"
	"time"
)

const (
	statsBucketCount = 5
	statsBucketWidth = time.Minute
	StatsWindow      = time.Duration(statsBucketCount) * statsBucketWidth
)

type CredentialStats struct {
	Success             uint64
	Failure             uint64
	Problem             uint64
	ConsecutiveFailure  uint64
	ConsecutiveProblem  uint64
	LastFailureCategory FailureCategory
	LastStatusCode      int
	// LastFailureSummary 是上游返回的原始文案(已脱敏), 管理面据此显示具体原因。
	LastFailureSummary string
}

type StatsStore struct {
	mu      sync.Mutex
	windows map[uint]*credentialStatsWindow
}

type statsBucket struct {
	minute  int64
	valid   bool
	success uint64
	failure uint64
	problem uint64
}

type credentialStatsWindow struct {
	buckets             [statsBucketCount]statsBucket
	consecutiveFailure  uint64
	consecutiveProblem  uint64
	lastEventAt         time.Time
	lastEventRecorded   bool
	lastFailureCategory FailureCategory
	lastStatusCode      int
	lastFailureSummary  string
}

type statsEvent uint8

const (
	statsEventSuccess statsEvent = iota
	statsEventFailure
	statsEventProblem
)

func NewStatsStore() *StatsStore {
	return &StatsStore{windows: make(map[uint]*credentialStatsWindow)}
}

func (store *StatsStore) RecordSuccess(credentialID uint, at time.Time) {
	store.record(credentialID, statsEventSuccess, FailureCategoryOK, 0, "", at)
}

func (store *StatsStore) RecordFailure(
	credentialID uint,
	category FailureCategory,
	statusCode int,
	summary string,
	at time.Time,
) {
	store.record(credentialID, statsEventFailure, category, statusCode, summary, at)
}

func (store *StatsStore) RecordProblem(
	credentialID uint,
	category FailureCategory,
	statusCode int,
	summary string,
	at time.Time,
) {
	store.record(credentialID, statsEventProblem, category, statusCode, summary, at)
}

func (store *StatsStore) record(
	credentialID uint,
	event statsEvent,
	category FailureCategory,
	statusCode int,
	summary string,
	at time.Time,
) {
	if credentialID == 0 {
		return
	}

	minute := at.UnixNano() / int64(statsBucketWidth)
	slot := statsBucketSlot(minute)

	store.mu.Lock()
	defer store.mu.Unlock()

	window := store.windows[credentialID]
	if window == nil {
		window = &credentialStatsWindow{}
		store.windows[credentialID] = window
	}

	bucket := &window.buckets[slot]
	if !bucket.valid || minute > bucket.minute {
		*bucket = statsBucket{minute: minute, valid: true}
	} else if minute < bucket.minute {
		return
	}

	if event == statsEventSuccess {
		bucket.success++
	} else {
		bucket.problem++
		if event == statsEventFailure {
			bucket.failure++
		}
	}

	if window.lastEventRecorded && at.Before(window.lastEventAt) {
		return
	}
	window.lastEventAt = at
	window.lastEventRecorded = true
	if event == statsEventSuccess {
		window.consecutiveFailure = 0
		window.consecutiveProblem = 0
		// 成功只清零连击计数, 保留最近一次失败的原因: 冷却生效后仍会有
		// 在途请求成功落库, 若在此清空类别/文案, 冷却展示就会丢失真实
		// 原因(429 配额原文)退化成"未明确归类"。该字段只用于冷却/黑名单
		// 条目展示, 健康凭证不渲染, 保留无副作用。
		return
	}
	if event == statsEventFailure {
		window.consecutiveFailure++
	}
	window.consecutiveProblem++
	window.lastFailureCategory = category
	window.lastStatusCode = statusCode
	window.lastFailureSummary = summary
}

func (store *StatsStore) Reset(credentialID uint) {
	if credentialID == 0 {
		return
	}

	store.mu.Lock()
	delete(store.windows, credentialID)
	store.mu.Unlock()
}

func (store *StatsStore) ClearProblemState(credentialID uint) {
	if credentialID == 0 {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	window := store.windows[credentialID]
	if window == nil {
		return
	}
	window.consecutiveFailure = 0
	window.consecutiveProblem = 0
	window.lastFailureCategory = FailureCategoryOK
	window.lastStatusCode = 0
	window.lastFailureSummary = ""
}

func (store *StatsStore) Snapshot(credentialID uint, now time.Time) CredentialStats {
	if credentialID == 0 {
		return CredentialStats{}
	}

	currentMinute := now.UnixNano() / int64(statsBucketWidth)
	store.mu.Lock()
	defer store.mu.Unlock()

	window := store.windows[credentialID]
	if window == nil {
		return CredentialStats{}
	}

	stats := CredentialStats{
		ConsecutiveFailure:  window.consecutiveFailure,
		ConsecutiveProblem:  window.consecutiveProblem,
		LastFailureCategory: window.lastFailureCategory,
		LastStatusCode:      window.lastStatusCode,
		LastFailureSummary:  window.lastFailureSummary,
	}
	firstMinute := currentMinute - (statsBucketCount - 1)
	for _, bucket := range window.buckets {
		if !bucket.valid || bucket.minute < firstMinute || bucket.minute > currentMinute {
			continue
		}
		stats.Success += bucket.success
		stats.Failure += bucket.failure
		stats.Problem += bucket.problem
	}
	return stats
}

func statsBucketSlot(minute int64) int {
	slot := minute % statsBucketCount
	if slot < 0 {
		slot += statsBucketCount
	}
	return int(slot)
}
