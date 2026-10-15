package requestlog

import (
	"testing"
	"time"

	"gpt-load/internal/pricing"
	"gpt-load/internal/storage/models"
	"gpt-load/internal/telemetry"
	"gpt-load/internal/usage"
)

// 锚点对齐验证: 5h 窗口起点 = 锚点 + 5h*n(registry 维护, 每日 07:00 空闲检查
// 重置, 凭证收到首个请求时开窗)。tokens_5h/requests_5h 只累计当前窗口内的行;
// 无锚点凭证退回滚动 now-5h。
func TestCredentialWindowUsageAlignsFiveHourWindowToAnchor(t *testing.T) {
	db := openRequestLogQueryDB(t)
	service := newRequestLogTestService(db)
	now := time.Date(2026, time.August, 14, 14, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	// 锚点 08:00 → 窗口起点 = 08:00 + 5h*floor(6h/5h) = 13:00。
	anchoredID := uint(41)
	rollingID := uint(42)

	if err := db.Exec("INSERT INTO groups (id, name, channel_id, params, models, created_at_ms, updated_at_ms) VALUES (17, 'anchor-group', 'codex', '{}', '{}', 0, 0)").Error; err != nil {
		t.Fatalf("seed group row: %v", err)
	}
	if err := db.Exec("INSERT INTO credentials (id, group_id, data, fingerprint, identity_fingerprint, created_at_ms, updated_at_ms) VALUES (41, 17, '{}', 'fp-a41', 'ifp-a41', 0, 0)").Error; err != nil {
		t.Fatalf("seed credential 41: %v", err)
	}
	if err := db.Exec("INSERT INTO credentials (id, group_id, data, fingerprint, identity_fingerprint, created_at_ms, updated_at_ms) VALUES (42, 17, '{}', 'fp-a42', 'ifp-a42', 0, 0)").Error; err != nil {
		t.Fatalf("seed credential 42: %v", err)
	}

	mk := func(id string, credentialID uint, at time.Time, input int64, status string) models.RequestLog {
		row := requestLogWindowRow(id, credentialID, at, input)
		row.Status = status
		if status != string(telemetry.RequestStatusSuccess) {
			row.StatusCode = 429
			row.UncachedInputTokens = 0
			row.OutputTokens = 0
			row.UsageState = string(usage.StateNotApplicable)
			row.CostState = string(pricing.CostStateNotApplicable)
			row.PricingCompleteness = string(pricing.CompletenessNotApplicable)
			row.EstimatedCostNanoUSD = 0
		}
		return row
	}

	rows := []models.RequestLog{
		// 凭证 41: 锚点窗 08:00 前的行属于上一窗口, 对齐后排除(滚动 now-5h=09:00 也排除)。
		mk("00000000-0000-4000-8000-000000000801", anchoredID, now.Add(-6*time.Hour), 500, string(telemetry.RequestStatusSuccess)),
		// 09:30: 在滚动 now-5h(09:00)内、在锚点窗 13:00 前 —— 只有滚动口径会误含。
		mk("00000000-0000-4000-8000-000000000802", anchoredID, now.Add(-4*time.Hour-30*time.Minute), 400, string(telemetry.RequestStatusSuccess)),
		// 当前窗口 13:00 起。
		mk("00000000-0000-4000-8000-000000000803", anchoredID, now.Add(-30*time.Minute), 100, string(telemetry.RequestStatusSuccess)),
		// 失败行计请求数但 token 为 0。
		mk("00000000-0000-4000-8000-000000000804", anchoredID, now.Add(-15*time.Minute), 0, string(telemetry.RequestStatusError)),
		// 凭证 42(无锚点): 滚动回退口径。
		mk("00000000-0000-4000-8000-000000000805", rollingID, now.Add(-time.Hour), 60, string(telemetry.RequestStatusSuccess)),
	}
	for _, row := range rows {
		createRequestLogQueryRow(t, db, row)
	}

	result, err := service.CredentialWindowUsage(t.Context(), 0, map[uint]int64{
		anchoredID: now.Add(-6 * time.Hour).UnixMilli(),
	})
	if err != nil {
		t.Fatalf("CredentialWindowUsage() error = %v", err)
	}

	if got := result[anchoredID]; got.Tokens5h != 105 || got.Requests5h != 2 {
		t.Fatalf("anchored credential = %#v (rolling=%#v), want tokens_5h=105 requests_5h=2", got, result[rollingID])
	}
	if got := result[rollingID]; got.Tokens5h != 65 || got.Requests5h != 1 {
		t.Fatalf("rolling credential = %#v, want tokens_5h=65 requests_5h=1", got)
	}
}
