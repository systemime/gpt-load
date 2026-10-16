package requestlog

import (
	"testing"
	"time"

	"gpt-load/internal/storage/models"
)

// 配额压力口径按模型倍率折算(glm-5.3 ×2, 精确匹配), 原始口径保持字面值。
func TestCredentialWindowUsageWeightsQuotaTokensByModel(t *testing.T) {
	db := openRequestLogQueryDB(t)
	service := newRequestLogTestService(db)
	now := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	group := models.Group{Name: "glm-quota", ChannelID: "glm", Params: models.JSON(`{}`), Models: models.JSON(`{}`), Enabled: true}
	if err := db.Create(&group).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	credential := models.Credential{Name: "cred-51", GroupID: group.ID, Data: "sk-test", Fingerprint: "fp-quota-51"}
	if err := db.Create(&credential).Error; err != nil {
		t.Fatalf("create credential: %v", err)
	}
	credentialID := credential.ID

	doubled := requestLogWindowRow("00000000-0000-4000-8000-000000000751", credentialID, now.Add(-time.Hour), 100)
	doubled.UpstreamModel = "glm-5.3"
	doubled.OutputTokens = 50
	// 前缀变体不参与精确匹配, 按原值计。
	variant := requestLogWindowRow("00000000-0000-4000-8000-000000000752", credentialID, now.Add(-2*time.Hour), 30)
	variant.UpstreamModel = "glm-5.3-flash"
	variant.OutputTokens = 5
	for _, row := range []models.RequestLog{doubled, variant} {
		createRequestLogQueryRow(t, db, row)
	}

	usage, err := service.CredentialWindowUsage(t.Context(), 0)
	if err != nil {
		t.Fatalf("CredentialWindowUsage() error = %v", err)
	}
	got := usage[credentialID]
	// 折算: (100+50)*2 + (30+5)*1 = 335; 原始: 150 + 35 = 185。
	if got.Tokens5h != 335 || got.Tokens7d != 335 ||
		got.RawTokens5h != 185 || got.RawTokens7d != 185 {
		t.Fatalf("quota-weighted usage = %#v", got)
	}
}
