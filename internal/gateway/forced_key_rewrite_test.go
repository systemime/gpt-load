package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/gin-gonic/gin"
	"testing"

	"gpt-load/internal/channel"
	"gpt-load/internal/dialect"
	"gpt-load/internal/health"
	"gpt-load/internal/state"
	"gpt-load/internal/telemetry"
	"gpt-load/internal/testutil/encryptiontest"
)

// 密钥级强制改写: 请求模型命中密钥映射时, 报文/路由/上游模型整体切换到目标;
// 对未命中模型与未配置密钥不产生任何影响。
func TestHandlerKeyForcedModelRewriteRewiresRequest(t *testing.T) {
	forwarder := &scriptedForwarder{results: []UpstreamResult{{
		StatusCode: http.StatusOK, Header: make(http.Header),
		Body: []byte(`{"ok":true}`), RequestWritten: true,
	}}}
	keyService := encryptiontest.Service(t, "key-rewrite-test-master-key")
	manager := state.NewManager()
	if _, err := manager.Publish(state.CompileInput{
		ChannelRegistry: channel.NewRegistry(),
		Groups: []state.GroupConfig{{ConnectionType: "api_key", ID: 1, Name: "openai",
			ChannelID: channel.OpenAI, Params: json.RawMessage(`{}`),
			Models: []state.ModelConfig{{ID: "gpt-4o"}, {ID: "gpt-4o-mini"}}, Enabled: true,
		}},
		Credentials: []state.CredentialConfig{{ID: 1, GroupID: 1,
			Status: state.CredentialStatusActive, Version: 1, IdentityGeneration: 1, Fingerprint: "c1"}},
		AccessKeys: []state.AccessKeyConfig{{
			ID: 1, Name: "client", KeyHash: keyService.Hash("gl-client"),
			Status: state.AccessKeyStatusActive,
			Filters: state.FilterSet{ForcedModelRewrite: state.ForcedModelRewrite{
				Enabled: true, Mappings: map[string]string{"gpt-4o": "gpt-4o-mini"},
			}},
		}},
	}); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	registry := state.NewCredentialRegistry()
	credential, encryptErr := keyService.Encrypt(`{"api_key":"sk-upstream"}`)
	if encryptErr != nil {
		t.Fatalf("Encrypt() error = %v", encryptErr)
	}
	if err := registry.ReplaceCredentials([]state.CredentialEntry{{
		ID: 1, GroupID: 1, Status: state.CredentialStatusActive, Version: 1,
		IdentityGeneration: 1, Fingerprint: "c1", EncryptedValue: credential,
	}}); err != nil {
		t.Fatalf("ReplaceCredentials() error = %v", err)
	}
	handler := NewHandler(
		manager, registry, keyService, forwarder, dialect.NewSet(dialect.NewOpenAI()),
		health.NewStatsStore(), health.NewMutationCoordinator(),
		&recordingAccessKeyRPMLimiter{}, telemetry.NoopRequestLogSink{}, nil,
	)
	handler.newRequestID = func() (string, error) { return fixedRequestID, nil }
	engine := gin.New()
	bindGatewayRoutesForTest(t, engine, handler)

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer gl-client")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if len(forwarder.inputs) != 1 {
		t.Fatalf("forward calls = %d, want 1; http=%d body=%s",
			len(forwarder.inputs), recorder.Code, recorder.Body.String())
	}
	input := forwarder.inputs[0]
	if input.ExternalModel != "gpt-4o-mini" || input.UpstreamModelID != "gpt-4o-mini" {
		t.Fatalf("models = external %q upstream %q, want both gpt-4o-mini",
			input.ExternalModel, input.UpstreamModelID)
	}
	var body struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(input.Request.Body, &body); err != nil {
		t.Fatalf("Unmarshal(body) error = %v", err)
	}
	if body.Model != "gpt-4o-mini" {
		t.Fatalf("body model = %q, want gpt-4o-mini", body.Model)
	}
}

// 未命中映射的模型与未启用改写的密钥保持原样。
func TestHandlerKeyForcedModelRewriteLeavesOtherModelsUntouched(t *testing.T) {
	forwarder := &scriptedForwarder{results: []UpstreamResult{{
		StatusCode: http.StatusOK, Header: make(http.Header),
		Body: []byte(`{"ok":true}`), RequestWritten: true,
	}}}
	keyService := encryptiontest.Service(t, "key-rewrite-test-master-key")
	manager := state.NewManager()
	if _, err := manager.Publish(state.CompileInput{
		ChannelRegistry: channel.NewRegistry(),
		Groups: []state.GroupConfig{{ConnectionType: "api_key", ID: 1, Name: "openai",
			ChannelID: channel.OpenAI, Params: json.RawMessage(`{}`),
			Models: []state.ModelConfig{{ID: "gpt-4o"}, {ID: "gpt-4o-mini"}}, Enabled: true,
		}},
		Credentials: []state.CredentialConfig{{ID: 1, GroupID: 1,
			Status: state.CredentialStatusActive, Version: 1, IdentityGeneration: 1, Fingerprint: "c1"}},
		AccessKeys: []state.AccessKeyConfig{{
			ID: 1, Name: "client", KeyHash: keyService.Hash("gl-client"),
			Status: state.AccessKeyStatusActive,
			Filters: state.FilterSet{ForcedModelRewrite: state.ForcedModelRewrite{
				Enabled: true, Mappings: map[string]string{"gpt-4o": "gpt-4o-mini"},
			}},
		}},
	}); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	registry := state.NewCredentialRegistry()
	credential, encryptErr := keyService.Encrypt(`{"api_key":"sk-upstream"}`)
	if encryptErr != nil {
		t.Fatalf("Encrypt() error = %v", encryptErr)
	}
	if err := registry.ReplaceCredentials([]state.CredentialEntry{{
		ID: 1, GroupID: 1, Status: state.CredentialStatusActive, Version: 1,
		IdentityGeneration: 1, Fingerprint: "c1", EncryptedValue: credential,
	}}); err != nil {
		t.Fatalf("ReplaceCredentials() error = %v", err)
	}
	handler := NewHandler(
		manager, registry, keyService, forwarder, dialect.NewSet(dialect.NewOpenAI()),
		health.NewStatsStore(), health.NewMutationCoordinator(),
		&recordingAccessKeyRPMLimiter{}, telemetry.NoopRequestLogSink{}, nil,
	)
	handler.newRequestID = func() (string, error) { return fixedRequestID, nil }
	engine := gin.New()
	bindGatewayRoutesForTest(t, engine, handler)

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer gl-client")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if len(forwarder.inputs) != 1 {
		t.Fatalf("forward calls = %d, want 1; http=%d body=%s",
			len(forwarder.inputs), recorder.Code, recorder.Body.String())
	}
	input := forwarder.inputs[0]
	if input.ExternalModel != "gpt-4o-mini" || input.UpstreamModelID != "gpt-4o-mini" {
		t.Fatalf("models = %q/%q, want untouched gpt-4o-mini",
			input.ExternalModel, input.UpstreamModelID)
	}
}
