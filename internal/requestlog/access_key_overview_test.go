package requestlog

import (
	"testing"
	"time"

	"gpt-load/internal/storage/models"
)

func seedOverviewStats(t *testing.T, service *Service) {
	t.Helper()
	base := time.Now().UTC().Truncate(24 * time.Hour)
	rows := []modelsUsageStat{
		{AccessKeyID: 1, Day: 0, Hour: 0, Requests: 10, Success: 9, Tokens: 1000},
		{AccessKeyID: 1, Day: 0, Hour: 1, Requests: 5, Success: 5, Tokens: 500},
		{AccessKeyID: 1, Day: 1, Hour: 0, Requests: 7, Success: 7, Tokens: 700},
		{AccessKeyID: 2, Day: 1, Hour: 0, Requests: 4, Success: 4, Tokens: 400},
	}
	for _, row := range rows {
		bucket := base.AddDate(0, 0, row.Day).Add(time.Duration(row.Hour) * time.Hour)
		if err := service.db.Create(&models.RequestLog{
			ID: time.Now().Format("150405.000000000") + "-seed", AccessKeyID: row.AccessKeyID,
			CompletedAtMS: bucket.UnixMilli() + 1000, Status: "success",
			UncachedInputTokens: row.Tokens, OutputTokens: 0,
		}).Error; err != nil {
			t.Fatalf("seed request log: %v", err)
		}
	}
	// 直接让聚合表有数据: 手动 upsert usage_stats(测试不经 worker)。
	for _, row := range rows {
		bucket := base.AddDate(0, 0, row.Day).Add(time.Duration(row.Hour) * time.Hour)
		if err := service.db.Exec(`INSERT INTO usage_stats
(bucket_start_ms, access_key_id, channel_id, group_id, credential_id, model,
 request_count, success_count, failure_count, uncached_input_tokens, output_tokens, cache_read_tokens,
 cache_write_5m_tokens, cache_write_1h_tokens, cache_write_unknown_tokens)
VALUES (?, ?, '', 1, 0, 'gpt-4o', ?, ?, ?, ?, 0, 0, 0, 0, 0)`,
			bucket.UnixMilli(), row.AccessKeyID, row.Requests, row.Success,
			row.Requests-row.Success, row.Tokens).Error; err != nil {
			t.Fatalf("seed usage stat: %v", err)
		}
	}
}

type modelsUsageStat struct {
	AccessKeyID uint
	Day         int
	Hour        int
	Requests    int64
	Success     int64
	Tokens      int64
}

func TestAccessKeyUsageOverviewAggregatesAndFilters(t *testing.T) {
	db := openRequestLogQueryDB(t)
	service := NewService(db, nil, staticRetentionPolicy{days: 0})
	seedOverviewStats(t, service)

	overview, err := service.AccessKeyUsageOverview(t.Context(), AccessKeyOverviewFilter{})
	if err != nil {
		t.Fatalf("AccessKeyUsageOverview() error = %v", err)
	}
	if got := overview.Totals[1].Requests; got != 22 {
		t.Fatalf("person 1 requests = %d, want 22", got)
	}
	if got := overview.Totals[2].Requests; got != 4 {
		t.Fatalf("person 2 requests = %d, want 4", got)
	}
	if len(overview.Series) != 3 {
		t.Fatalf("series points = %d, want 3 (person1 x2 days + person2 x1 day)", len(overview.Series))
	}
	var person1Tokens int64
	for _, point := range overview.Series {
		if point.AccessKeyID == 1 && point.Tokens != 1500 && point.Tokens != 700 {
			t.Fatalf("unexpected tokens %d for person 1", point.Tokens)
		}
		person1Tokens += 0
	}
	_ = person1Tokens

	filtered, err := service.AccessKeyUsageOverview(t.Context(), AccessKeyOverviewFilter{AccessKeyID: 2})
	if err != nil {
		t.Fatalf("filtered overview error = %v", err)
	}
	if len(filtered.Totals) != 1 || filtered.Totals[2].Requests != 4 {
		t.Fatalf("person filter result = %#v", filtered.Totals)
	}
	if len(filtered.Series) != 1 {
		t.Fatalf("person filter series = %d, want 1", len(filtered.Series))
	}
}
