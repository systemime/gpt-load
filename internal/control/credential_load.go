package control

import (
	"github.com/gin-gonic/gin"

	"gpt-load/internal/state"

	"gpt-load/internal/platform/response"
)

// 凭证负载数据端点: 实时在途(准入计数器) + 1h/5h/24h 分布 + 5h 异常分类。
// 与 gpt-load-watch 脚本同一口径, 供管理端「凭证负载」页面轮询。

type credentialLoadWindowStats struct {
	Requests            int64 `json:"requests"`
	Success             int64 `json:"success"`
	AffinityHits        int64 `json:"affinity_hits"`
	UncachedInputTokens int64 `json:"uncached_input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
}

type credentialLoadErrorStats struct {
	RateLimited int64 `json:"rate_limited"`
	Connect     int64 `json:"connect"`
	Timeout     int64 `json:"timeout"`
	Upstream    int64 `json:"upstream"`
	Canceled    int64 `json:"canceled"`
	Scheduled   int64 `json:"scheduled"`
	Other       int64 `json:"other"`
}

type credentialLoadItem struct {
	ID       uint                                 `json:"id"`
	Name     string                               `json:"name"`
	Status   string                               `json:"status"`
	Inflight int64                                `json:"inflight"`
	Queued   int64                                `json:"queued"`
	Usage    credentialLoadUsage                  `json:"usage"`
	Pressure float64                              `json:"pressure"`
	Windows  map[string]credentialLoadWindowStats `json:"windows"`
	Errors5h credentialLoadErrorStats             `json:"errors_5h"`
}

type credentialLoadUsage struct {
	Requests5h int64 `json:"requests_5h"`
	Tokens5h   int64 `json:"tokens_5h"`
	Requests7d int64 `json:"requests_7d"`
	Tokens7d   int64 `json:"tokens_7d"`
}

type credentialLoadResponse struct {
	ObservedAtMS int64                `json:"observed_at_ms"`
	Cap          int                  `json:"cap"`
	Credentials  []credentialLoadItem `json:"credentials"`
	LocalRejects map[string]int64     `json:"local_rejects"`
}

var credentialLoadErrorCategories = map[string]string{
	"upstream_rate_limited":   "rate_limited",
	"upstream_connect_failed": "connect",
	"upstream_host_error":     "connect",
	"upstream_timeout":        "timeout",
	"upstream_protocol_error": "upstream",
	"upstream_sse_error":      "upstream",
	"upstream_error":          "upstream",
	"client_canceled":         "canceled",
	"no_available_candidate":  "scheduled",
}

func (stats *credentialLoadErrorStats) add(category string, count int64) {
	switch category {
	case "rate_limited":
		stats.RateLimited += count
	case "connect":
		stats.Connect += count
	case "timeout":
		stats.Timeout += count
	case "upstream":
		stats.Upstream += count
	case "canceled":
		stats.Canceled += count
	case "scheduled":
		stats.Scheduled += count
	default:
		stats.Other += count
	}
}

func (server *Server) handleCredentialLoad(c *gin.Context) {
	service := server.service
	observedAt, err := safeEpochMilliseconds(service.now())
	if err != nil {
		writeServiceError(c, "credential_load", err)
		return
	}

	names, err := service.credentialNames(c)
	if err != nil {
		writeServiceError(c, "credential_load", err)
		return
	}
	windows, localRejects, err := service.credentialLoadWindows(c, observedAt)
	if err != nil {
		writeServiceError(c, "credential_load", err)
		return
	}
	errorCounts, err := service.credentialLoadErrors(c, observedAt)
	if err != nil {
		writeServiceError(c, "credential_load", err)
		return
	}

	slots := service.registry.Slots()
	capacity := slots.Capacity()
	if snapshot := service.manager.Current(); snapshot != nil {
		capacity = snapshot.Settings.CredentialConcurrencyLimit
	}

	views := service.registry.Snapshot()
	usageSnapshot := service.registry.UsageTracker().SnapshotAll()
	pressures := credentialPressures(views, usageSnapshot)

	result := credentialLoadResponse{
		ObservedAtMS: observedAt,
		Cap:          capacity,
		Credentials:  make([]credentialLoadItem, 0, len(views)),
		LocalRejects: localRejects,
	}
	for _, view := range views {
		usage := usageSnapshot[view.ID]
		item := credentialLoadItem{
			ID: view.ID, Name: names[view.ID], Status: string(view.Status),
			Inflight: slots.Inflight(view.ID),
			Queued:   slots.Waiting(view.ID),
			Usage: credentialLoadUsage{
				Requests5h: usage.Requests5h, Tokens5h: usage.Tokens5h,
				Requests7d: usage.Requests7d, Tokens7d: usage.Tokens7d,
			},
			Pressure: pressures[view.ID],
			Windows:  windows[view.ID],
			Errors5h: errorCounts[view.ID],
		}
		result.Credentials = append(result.Credentials, item)
	}
	c.Header("Cache-Control", "no-store")
	response.SuccessI18n(c, "common.success", result)
}

// credentialPressures 与调度器同式: 压力 = max(窗口×指标的实际份额/权重目标份额),
// 全零用量时全部为 0。仅展示用, 调度真值在 scheduler.usagePressures。
func credentialPressures(views []state.CredentialRuntimeView, usage map[uint]state.CredentialUsage) map[uint]float64 {
	pressures := make(map[uint]float64, len(views))
	weights := make(map[uint]float64, len(views))
	var totalWeight, requests5h, tokens5h, requests7d, tokens7d float64
	for _, view := range views {
		weight := 50.0
		if view.WeightManual != nil && *view.WeightManual > 0 {
			weight = float64(*view.WeightManual)
		}
		weights[view.ID] = weight
		totalWeight += weight
		entry := usage[view.ID]
		requests5h += float64(entry.Requests5h)
		tokens5h += float64(entry.Tokens5h)
		requests7d += float64(entry.Requests7d)
		tokens7d += float64(entry.Tokens7d)
	}
	if totalWeight <= 0 || (requests5h <= 0 && tokens5h <= 0 && requests7d <= 0 && tokens7d <= 0) {
		return pressures
	}
	share := func(part, total, target float64) float64 {
		if total <= 0 || target <= 0 {
			return 0
		}
		return (part / total) / target
	}
	for _, view := range views {
		entry := usage[view.ID]
		target := weights[view.ID] / totalWeight
		value := share(float64(entry.Requests5h), requests5h, target)
		if next := share(float64(entry.Tokens5h), tokens5h, target); next > value {
			value = next
		}
		if next := share(float64(entry.Requests7d), requests7d, target); next > value {
			value = next
		}
		if next := share(float64(entry.Tokens7d), tokens7d, target); next > value {
			value = next
		}
		pressures[view.ID] = value
	}
	return pressures
}

func (service *Service) credentialNames(c *gin.Context) (map[uint]string, error) {
	rows := make([]struct {
		ID   uint
		Name string
	}, 0, 16)
	if err := service.db.WithContext(c.Request.Context()).
		Table("credentials").
		Select("id, name").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	names := make(map[uint]string, len(rows))
	for _, row := range rows {
		names[row.ID] = row.Name
	}
	return names, nil
}

func (service *Service) credentialLoadWindows(
	c *gin.Context,
	observedAtMS int64,
) (map[uint]map[string]credentialLoadWindowStats, map[string]int64, error) {
	hour, fiveHours, day := observedAtMS-3_600_000, observedAtMS-18_000_000, observedAtMS-86_400_000
	rows := make([]struct {
		CredentialID uint  `gorm:"column:credential_id"`
		Requests1h   int64 `gorm:"column:requests_1h"`
		Success1h    int64 `gorm:"column:success_1h"`
		Affinity1h   int64 `gorm:"column:affinity_1h"`
		Uncached1h   int64 `gorm:"column:uncached_1h"`
		Output1h     int64 `gorm:"column:output_1h"`
		CacheRead1h  int64 `gorm:"column:cache_read_1h"`
		Requests5h   int64 `gorm:"column:requests_5h"`
		Success5h    int64 `gorm:"column:success_5h"`
		Affinity5h   int64 `gorm:"column:affinity_5h"`
		Uncached5h   int64 `gorm:"column:uncached_5h"`
		Output5h     int64 `gorm:"column:output_5h"`
		CacheRead5h  int64 `gorm:"column:cache_read_5h"`
		Requests24h  int64 `gorm:"column:requests_24h"`
		Success24h   int64 `gorm:"column:success_24h"`
		Affinity24h  int64 `gorm:"column:affinity_24h"`
		Uncached24h  int64 `gorm:"column:uncached_24h"`
		Output24h    int64 `gorm:"column:output_24h"`
		CacheRead24h int64 `gorm:"column:cache_read_24h"`
	}, 0, 16)
	if err := service.db.WithContext(c.Request.Context()).
		Table("request_logs").
		Select(`credential_id,
SUM(CASE WHEN completed_at_ms > ? THEN 1 ELSE 0 END) AS requests_1h,
SUM(CASE WHEN completed_at_ms > ? AND status = 'success' THEN 1 ELSE 0 END) AS success_1h,
SUM(CASE WHEN completed_at_ms > ? AND affinity_hit THEN 1 ELSE 0 END) AS affinity_1h,
SUM(CASE WHEN completed_at_ms > ? THEN uncached_input_tokens ELSE 0 END) AS uncached_1h,
SUM(CASE WHEN completed_at_ms > ? THEN output_tokens ELSE 0 END) AS output_1h,
SUM(CASE WHEN completed_at_ms > ? THEN cache_read_tokens ELSE 0 END) AS cache_read_1h,
SUM(CASE WHEN completed_at_ms > ? THEN 1 ELSE 0 END) AS requests_5h,
SUM(CASE WHEN completed_at_ms > ? AND status = 'success' THEN 1 ELSE 0 END) AS success_5h,
SUM(CASE WHEN completed_at_ms > ? AND affinity_hit THEN 1 ELSE 0 END) AS affinity_5h,
SUM(CASE WHEN completed_at_ms > ? THEN uncached_input_tokens ELSE 0 END) AS uncached_5h,
SUM(CASE WHEN completed_at_ms > ? THEN output_tokens ELSE 0 END) AS output_5h,
SUM(CASE WHEN completed_at_ms > ? THEN cache_read_tokens ELSE 0 END) AS cache_read_5h,
SUM(CASE WHEN completed_at_ms > ? THEN 1 ELSE 0 END) AS requests_24h,
SUM(CASE WHEN completed_at_ms > ? AND status = 'success' THEN 1 ELSE 0 END) AS success_24h,
SUM(CASE WHEN completed_at_ms > ? AND affinity_hit THEN 1 ELSE 0 END) AS affinity_24h,
SUM(CASE WHEN completed_at_ms > ? THEN uncached_input_tokens ELSE 0 END) AS uncached_24h,
SUM(CASE WHEN completed_at_ms > ? THEN output_tokens ELSE 0 END) AS output_24h,
SUM(CASE WHEN completed_at_ms > ? THEN cache_read_tokens ELSE 0 END) AS cache_read_24h`,
			hour, hour, hour, hour, hour, hour,
			fiveHours, fiveHours, fiveHours, fiveHours, fiveHours, fiveHours,
			day, day, day, day, day, day,
		).
		Where("completed_at_ms > ?", day).
		Group("credential_id").
		Scan(&rows).Error; err != nil {
		return nil, nil, err
	}

	windows := make(map[uint]map[string]credentialLoadWindowStats, len(rows))
	localRejects := map[string]int64{"1h": 0, "5h": 0, "24h": 0}
	for _, row := range rows {
		if row.CredentialID == 0 {
			localRejects["1h"] = row.Requests1h
			localRejects["5h"] = row.Requests5h
			localRejects["24h"] = row.Requests24h
			continue
		}
		windows[row.CredentialID] = map[string]credentialLoadWindowStats{
			"1h": {
				Requests: row.Requests1h, Success: row.Success1h, AffinityHits: row.Affinity1h,
				UncachedInputTokens: row.Uncached1h, OutputTokens: row.Output1h, CacheReadTokens: row.CacheRead1h,
			},
			"5h": {
				Requests: row.Requests5h, Success: row.Success5h, AffinityHits: row.Affinity5h,
				UncachedInputTokens: row.Uncached5h, OutputTokens: row.Output5h, CacheReadTokens: row.CacheRead5h,
			},
			"24h": {
				Requests: row.Requests24h, Success: row.Success24h, AffinityHits: row.Affinity24h,
				UncachedInputTokens: row.Uncached24h, OutputTokens: row.Output24h, CacheReadTokens: row.CacheRead24h,
			},
		}
	}
	return windows, localRejects, nil
}

func (service *Service) credentialLoadErrors(
	c *gin.Context,
	observedAtMS int64,
) (map[uint]credentialLoadErrorStats, error) {
	rows := make([]struct {
		CredentialID uint
		ErrorCode    string
		Count        int64
	}, 0, 16)
	if err := service.db.WithContext(c.Request.Context()).
		Table("request_logs").
		Select("credential_id, error_code, COUNT(*) AS count").
		Where("completed_at_ms > ? AND error_code != ''", observedAtMS-18_000_000).
		Group("credential_id, error_code").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[uint]credentialLoadErrorStats)
	for _, row := range rows {
		stats := counts[row.CredentialID]
		stats.add(credentialLoadErrorCategories[row.ErrorCode], row.Count)
		counts[row.CredentialID] = stats
	}
	return counts, nil
}
