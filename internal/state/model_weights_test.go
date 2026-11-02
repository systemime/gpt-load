package state

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"gpt-load/internal/platform/config"
)

func TestSlotWeightExactModelMatch(t *testing.T) {
	if SlotWeight("glm-5.3") != 2 {
		t.Fatal("glm-5.3 should occupy two slots")
	}
	for _, name := range []string{"glm-5.3-flash", "GLM-5.3", "glm-4.7", "", "unknown"} {
		if SlotWeight(name) != 1 {
			t.Fatalf("SlotWeight(%q) should be 1 (exact match only)", name)
		}
	}
}

func TestEffectiveModelQuotaWeightsMergesOverrides(t *testing.T) {
	merged := EffectiveModelQuotaWeights(map[string]float64{"glm-5.3": 3, "gpt-5.5": 1.5})
	if merged["glm-5.3"] != 3 {
		t.Fatalf("override should replace builtin: %v", merged["glm-5.3"])
	}
	if merged["gpt-5.5"] != 1.5 {
		t.Fatalf("new entry missing: %v", merged)
	}
	if ModelQuotaWeights["glm-5.3"] != 2 {
		t.Fatal("builtin table must stay untouched")
	}
	if EffectiveModelQuotaWeights(nil)["glm-5.3"] != 2 {
		t.Fatal("nil custom should fall back to builtin table")
	}
	if SlotWeightIn(merged, "gpt-5.5") != 2 {
		t.Fatal("1.5 weight should ceil to 2 slots")
	}
	if SlotWeightIn(nil, "unknown") != 1 {
		t.Fatal("nil table should fall back to builtin (unknown = 1)")
	}
}

func TestResolveModelQuotaWeightsSetting(t *testing.T) {
	resolved, err := ResolveRuntimeSettings(config.Settings{
		SettingModelQuotaWeights: `{"gpt-5.5":1.5,"glm-5.3":3}`,
	})
	if err != nil {
		t.Fatalf("ResolveRuntimeSettings() error = %v", err)
	}
	if resolved.ModelQuotaWeights["gpt-5.5"] != 1.5 || resolved.ModelQuotaWeights["glm-5.3"] != 3 {
		t.Fatalf("weights = %#v", resolved.ModelQuotaWeights)
	}
	for name, invalid := range map[string]string{
		"negative weight":  `{"m":-1}`,
		"empty name":       `{"  ":2}`,
		"not an object":    `[]`,
		"too many entries": `{"a":1,"b":1,"c":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			if name == "too many entries" {
				entries := make([]string, 0, maxModelQuotaWeightEntries+1)
				for i := 0; i <= maxModelQuotaWeightEntries; i++ {
					entries = append(entries, fmt.Sprintf(`"m%d":1`, i))
				}
				invalid = "{" + strings.Join(entries, ",") + "}"
			}
			if _, err := ResolveRuntimeSettings(config.Settings{SettingModelQuotaWeights: invalid}); err == nil {
				t.Fatalf("invalid weights %s should be rejected", name)
			}
		})
	}
	if err := ValidateRuntimeSetting(SettingModelQuotaWeights, map[string]any{"m": json.Number("1.5")}); err != nil {
		t.Fatalf("ValidateRuntimeSetting() error = %v", err)
	}
}

func TestResolvePlanQuotaCreditsSetting(t *testing.T) {
	resolved, err := ResolveRuntimeSettings(config.Settings{
		SettingPlanQuotaCredits: `{"standard":{"credits_5h":1,"credits_7d":2},"vendor_x":{"credits_5h":10,"credits_7d":20}}`,
	})
	if err != nil {
		t.Fatalf("ResolveRuntimeSettings() error = %v", err)
	}
	if resolved.PlanQuotaCredits["vendor_x"] != (PlanQuotaCredits{Credits5h: 10, Credits7d: 20}) {
		t.Fatalf("credits = %#v", resolved.PlanQuotaCredits)
	}
	if err := ValidateRuntimeSetting(SettingPlanQuotaCredits, map[string]any{
		"p": map[string]any{"credits_5h": float64(-1), "credits_7d": float64(2)},
	}); err == nil {
		t.Fatal("negative credits should be rejected")
	}
}

func TestSnapshotPlanQuotaCreditsOverride(t *testing.T) {
	snapshot, err := Compile(CompileInput{
		SystemSettings: config.Settings{
			SettingGlmCodingPlan:    "standard",
			SettingPlanQuotaCredits: `{"standard":{"credits_5h":100,"credits_7d":700}}`,
		},
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if credits5h, credits7d := snapshot.PlanQuotaCredits(GlmPlanStandard); credits5h != 100 || credits7d != 700 {
		t.Fatalf("override = %v/%v, want 100/700", credits5h, credits7d)
	}
	if credits5h, credits7d := snapshot.PlanQuotaCredits(GlmPlanPremium); credits5h != 35_000 || credits7d != 155_000 {
		t.Fatalf("non-overridden plan should use builtin: %v/%v", credits5h, credits7d)
	}
	weights := snapshot.ModelQuotaWeights
	if weights == nil || weights["glm-5.3"] != 2 {
		t.Fatalf("snapshot effective weights = %#v", weights)
	}
}

func TestCredentialSlotsWeightedAdmission(t *testing.T) {
	slots := NewCredentialSlots(6)
	for i := 0; i < 3; i++ {
		if !slots.TryAcquire(1, SlotWeight("glm-5.3")) {
			t.Fatalf("weighted acquire %d should fit (3x2=6)", i)
		}
	}
	if slots.TryAcquire(1, 2) {
		t.Fatal("fourth weighted request exceeds cap 6")
	}
	if slots.Inflight(1) != 6 {
		t.Fatalf("Inflight = %d, want 6 weighted units", slots.Inflight(1))
	}
	// 轻模型仍无空位: 剩余额度 0。
	if slots.Available(1, 1) {
		t.Fatal("no unit should remain after three weighted requests")
	}
	slots.Release(1, 2)
	if !slots.Available(1, 2) || slots.Available(1, 3) {
		t.Fatal("freed 2 units: weight-2 fits, weight-3 does not")
	}
	if !slots.TryAcquire(1, 1) {
		t.Fatal("single-unit request should fit in 2 freed units")
	}
	if slots.TryAcquire(1, 2) {
		t.Fatal("only 1 unit remains after mixed fill")
	}
	// 权重配对错误只钳制到 0, 不产生负数。
	slots.Release(1, 2)
	slots.Release(1, 2)
	slots.Release(1, 2)
	slots.Release(1, 2)
	if slots.Inflight(1) != 0 {
		t.Fatalf("Inflight = %d after over-release, want 0", slots.Inflight(1))
	}
}
