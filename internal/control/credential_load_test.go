package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"gpt-load/internal/platform/config"
	"gpt-load/internal/state"
	"gpt-load/internal/storage/models"
)

func seedCredentialLoadRows(t *testing.T, fixture serviceFixture) {
	t.Helper()
	now := time.Now().UTC().UnixMilli()
	group := models.Group{ID: 1, Name: "load-test-group", ChannelID: "openai",
		Params: models.JSON("{}"), Models: models.JSON("[]")}
	if err := fixture.db.Create(&group).Error; err != nil {
		t.Fatalf("Create group error = %v", err)
	}
	credentials := []models.Credential{
		{ID: 1, GroupID: 1, Name: "alpha", Data: "cipher", Fingerprint: "fp-1", IdentityFingerprint: "fp-1"},
		{ID: 2, GroupID: 1, Name: "", Data: "cipher", Fingerprint: "fp-2", IdentityFingerprint: "fp-2"},
	}
	for index := range credentials {
		if err := fixture.db.Create(&credentials[index]).Error; err != nil {
			t.Fatalf("Create credential error = %v", err)
		}
	}
	logs := []models.RequestLog{
		// 1h 内: 凭证 1 成功 + 亲和命中 + token
		{ID: "req-1", CredentialID: 1, GroupID: 1, CompletedAtMS: now - 60_000, Status: "success",
			AffinityHit: true, UncachedInputTokens: 100, OutputTokens: 20, CacheReadTokens: 900},
		// 5h 内 1h 外: 凭证 2 限流错误
		{ID: "req-2", CredentialID: 2, GroupID: 1, CompletedAtMS: now - 2*3_600_000, Status: "error",
			ErrorCode: "upstream_rate_limited"},
		// 24h 内 5h 外: 凭证 1 连接错误
		{ID: "req-3", CredentialID: 1, GroupID: 1, CompletedAtMS: now - 6*3_600_000, Status: "error",
			ErrorCode: "upstream_connect_failed"},
		// 本地拒绝 (未达上游)
		{ID: "req-4", CredentialID: 0, GroupID: 0, CompletedAtMS: now - 60_000, Status: "error",
			ErrorCode: "no_available_candidate"},
	}
	for index := range logs {
		if err := fixture.db.Create(&logs[index]).Error; err != nil {
			t.Fatalf("Create request log error = %v", err)
		}
	}
}

func TestCredentialLoadEndpointAggregatesWindowsErrorsAndInflight(t *testing.T) {
	initControlI18n(t)
	gin.SetMode(gin.TestMode)
	fixture := newServiceFixture(t)
	seedCredentialLoadRows(t, fixture)

	if err := fixture.registry.ReplaceCredentials([]state.CredentialEntry{{
		ID: 1, GroupID: 1, Status: state.CredentialStatusActive,
		Version: 1, IdentityGeneration: 1, Fingerprint: "fp-1", EncryptedValue: "cipher",
	}, {
		ID: 2, GroupID: 1, Status: state.CredentialStatusActive,
		Version: 1, IdentityGeneration: 1, Fingerprint: "fp-2", EncryptedValue: "cipher",
	}}); err != nil {
		t.Fatalf("ReplaceCredentials() error = %v", err)
	}
	slots := fixture.registry.Slots()
	slots.Configure(2)
	if !slots.TryAcquire(1) {
		t.Fatal("occupy credential 1 failed")
	}
	if _, err := fixture.manager.Publish(state.CompileInput{
		SystemSettings: config.Settings{state.SettingCredentialConcurrencyLimit: 2},
		ChannelRegistry: fixture.channelRegistry,
	}); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	engine := gin.New()
	NewServer(&config.Config{AuthKey: "test-auth-key"}, fixture.service).RegisterRoutes(engine)
	request := httptest.NewRequest(http.MethodGet, "/api/credentials/load", nil)
	request.Header.Set("Authorization", "Bearer test-auth-key")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Data credentialLoadResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	result := payload.Data
	if result.Cap != 2 {
		t.Fatalf("cap = %d, want 2", result.Cap)
	}
	if result.LocalRejects["1h"] != 1 {
		t.Fatalf("local rejects 1h = %d, want 1", result.LocalRejects["1h"])
	}
	byID := make(map[uint]credentialLoadItem, len(result.Credentials))
	for _, item := range result.Credentials {
		byID[item.ID] = item
	}
	first := byID[1]
	if first.Name != "alpha" || first.Inflight != 1 {
		t.Fatalf("credential 1 = %#v, want name alpha inflight 1", first)
	}
	if first.Windows["1h"].Requests != 1 || first.Windows["1h"].Success != 1 ||
		first.Windows["1h"].AffinityHits != 1 || first.Windows["1h"].CacheReadTokens != 900 {
		t.Fatalf("credential 1 1h window = %#v", first.Windows["1h"])
	}
	if first.Windows["24h"].Requests != 2 {
		t.Fatalf("credential 1 24h requests = %d, want 2", first.Windows["24h"].Requests)
	}
	if first.Errors5h.Connect != 0 {
		t.Fatalf("credential 1 connect errors(5h) = %d, want 0 (row is 6h old)", first.Errors5h.Connect)
	}
	second := byID[2]
	if second.Errors5h.RateLimited != 1 {
		t.Fatalf("credential 2 rate-limited errors(5h) = %d, want 1", second.Errors5h.RateLimited)
	}
	if second.Windows["5h"].Requests != 1 || second.Windows["1h"].Requests != 0 {
		t.Fatalf("credential 2 windows = %#v", second.Windows)
	}
	if second.Name != "" {
		t.Fatalf("credential 2 name = %q, want empty (falls back to id in UI)", second.Name)
	}
}
