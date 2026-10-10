package requestlog

import (
	"context"

	"gorm.io/gorm"

	"gpt-load/internal/platform/epochms"
)

// 每人(access key)的累计与按日用量总览, 数据源为 usage_stats 聚合表。
// 该表不随请求日志保留策略清理, 因此支持全历史查询(阶段 4)。
// 支持筛选: 时间区间(可单边)、分组、单个用户。

type AccessKeyUsageTotals struct {
	Requests            int64 `json:"requests"`
	Success             int64 `json:"success"`
	UncachedInputTokens int64 `json:"uncached_input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
}

type AccessKeyUsagePoint struct {
	AccessKeyID uint  `json:"access_key_id"`
	DayMS       int64 `json:"day_ms"`
	Requests    int64 `json:"requests"`
	Success     int64 `json:"success"`
	Tokens      int64 `json:"tokens"`
}

type AccessKeyOverviewFilter struct {
	FromMS      int64 // 0 = 不限起点
	ToMS        int64 // 0 = 不限终点
	GroupID     uint  // 0 = 全部分组
	AccessKeyID uint  // 0 = 全部用户; 非 0 时序列与累计都只含该用户
}

// AccessKeyOverview 汇总筛选范围内每人累计, 以及按日序列。
type AccessKeyOverview struct {
	ObservedAtMS int64
	Filter       AccessKeyOverviewFilter
	Totals       map[uint]AccessKeyUsageTotals
	Series       []AccessKeyUsagePoint
}

const dayMilliseconds = epochms.MillisecondsPerDay

func (service *Service) AccessKeyUsageOverview(
	ctx context.Context,
	filter AccessKeyOverviewFilter,
) (AccessKeyOverview, error) {
	now := service.now()
	observedAt, err := epochms.FromTime(now)
	if err != nil {
		return AccessKeyOverview{}, err
	}
	overview := AccessKeyOverview{
		ObservedAtMS: observedAt,
		Filter:       filter,
		Totals:       map[uint]AccessKeyUsageTotals{},
	}
	scope := func(query *gorm.DB) *gorm.DB {
		if filter.FromMS > 0 {
			query = query.Where("bucket_start_ms >= ?", filter.FromMS)
		}
		if filter.ToMS > 0 {
			query = query.Where("bucket_start_ms < ?", filter.ToMS)
		}
		if filter.GroupID > 0 {
			query = query.Where("group_id = ?", filter.GroupID)
		}
		if filter.AccessKeyID > 0 {
			query = query.Where("access_key_id = ?", filter.AccessKeyID)
		}
		return query
	}

	totalRows := make([]struct {
		AccessKeyID         uint
		Requests            int64 `gorm:"column:requests"`
		Success             int64 `gorm:"column:success"`
		UncachedInputTokens int64 `gorm:"column:uncached_input_tokens"`
		OutputTokens        int64 `gorm:"column:output_tokens"`
		CacheReadTokens     int64 `gorm:"column:cache_read_tokens"`
	}, 0, 16)
	if err := scope(service.db.WithContext(ctx).Table("usage_stats")).
		Select(`access_key_id,
SUM(request_count) AS requests,
SUM(success_count) AS success,
SUM(uncached_input_tokens) AS uncached_input_tokens,
SUM(output_tokens) AS output_tokens,
SUM(cache_read_tokens) AS cache_read_tokens`).
		Group("access_key_id").
		Scan(&totalRows).Error; err != nil {
		return AccessKeyOverview{}, err
	}
	for _, row := range totalRows {
		overview.Totals[row.AccessKeyID] = AccessKeyUsageTotals{
			Requests: row.Requests, Success: row.Success,
			UncachedInputTokens: row.UncachedInputTokens,
			OutputTokens:        row.OutputTokens,
			CacheReadTokens:     row.CacheReadTokens,
		}
	}

	seriesRows := make([]struct {
		AccessKeyID uint
		DayMS       int64 `gorm:"column:day_ms"`
		Requests    int64 `gorm:"column:requests"`
		Success     int64 `gorm:"column:success"`
		Tokens      int64 `gorm:"column:tokens"`
	}, 0, 64)
	if err := scope(service.db.WithContext(ctx).Table("usage_stats")).
		Select(`access_key_id,
(bucket_start_ms / 86400000) * 86400000 AS day_ms,
SUM(request_count) AS requests,
SUM(success_count) AS success,
SUM(uncached_input_tokens + output_tokens + cache_read_tokens) AS tokens`).
		Group("access_key_id, day_ms").
		Order("day_ms ASC").
		Scan(&seriesRows).Error; err != nil {
		return AccessKeyOverview{}, err
	}
	overview.Series = make([]AccessKeyUsagePoint, 0, len(seriesRows))
	for _, row := range seriesRows {
		overview.Series = append(overview.Series, AccessKeyUsagePoint{
			AccessKeyID: row.AccessKeyID, DayMS: row.DayMS,
			Requests: row.Requests, Success: row.Success, Tokens: row.Tokens,
		})
	}
	return overview, nil
}
