package requestlog

import (
	"strings"
	"testing"
)

// 自定义权重表必须进入配额折算 SQL; provider 未注入时回退内置表。
func TestQuotaTokenMultiplierSQLUsesConfiguredWeights(t *testing.T) {
	builtin := quotaTokenMultiplierSQL(nil)
	if !strings.Contains(builtin, "WHEN model IN ('glm-5.3') THEN 2") {
		t.Fatalf("builtin SQL missing glm-5.3 branch: %s", builtin)
	}
	custom := quotaTokenMultiplierSQL(map[string]float64{"gpt-5.5": 1.5, "glm-5.3": 3})
	if !strings.Contains(custom, "WHEN model IN ('gpt-5.5') THEN 1.5") ||
		!strings.Contains(custom, "WHEN model IN ('glm-5.3') THEN 3") {
		t.Fatalf("custom SQL missing entries: %s", custom)
	}
	service := &Service{}
	service.SetQuotaWeightsProvider(stubQuotaWeights{weights: map[string]float64{"m": 4}})
	if got := quotaTokenMultiplierSQL(service.currentQuotaWeights()); !strings.Contains(got, "WHEN model IN ('m') THEN 4") {
		t.Fatalf("provider weights missing: %s", got)
	}
	empty := &Service{}
	if got := quotaTokenMultiplierSQL(empty.currentQuotaWeights()); !strings.Contains(got, "WHEN model IN ('glm-5.3') THEN 2") {
		t.Fatalf("nil provider should fall back to builtin: %s", got)
	}
}

type stubQuotaWeights struct{ weights map[string]float64 }

func (stub stubQuotaWeights) ModelQuotaWeights() map[string]float64 { return stub.weights }
