package state

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"gpt-load/internal/platform/config"
)

func TestSlotWeightExactModelMatch(t *testing.T) {
	// 半槽单位: 倍率 2 → 4 单位; 倍率 1 → 2 单位。
	if SlotWeight("glm-5.3") != 4 {
		t.Fatal("glm-5.3 (weight 2) should occupy 4 units (two slots)")
	}
	for _, name := range []string{"glm-5.3-flash", "GLM-5.3", "glm-4.7", "", "unknown"} {
		if SlotWeight(name) != 2 {
			t.Fatalf("SlotWeight(%q) should be 2 units (one slot, exact match only)", name)
		}
	}
	if SlotWeightIn(map[string]float64{"glm-5.3": 1.5}, "glm-5.3") != 3 {
		t.Fatal("weight 1.5 should occupy 3 units")
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
	if SlotWeightIn(merged, "gpt-5.5") != 3 {
		t.Fatal("1.5 weight should be 3 units")
	}
	if SlotWeightIn(nil, "unknown") != 2 {
		t.Fatal("nil table should fall back to builtin (unknown = 2 units)")
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
	// 生产口径: cap 6 = 12 半槽单位; glm-5.3 倍率 2 = 4 单位, 恰好容纳 3 个请求。
	slots := NewCredentialSlots(6)
	w := SlotWeight("glm-5.3")
	for i := 0; i < 3; i++ {
		if !slots.TryAcquire(1, w) {
			t.Fatalf("weighted acquire %d should fit (3x4=12)", i)
		}
	}
	if slots.TryAcquire(1, w) {
		t.Fatal("fourth weighted request exceeds cap 6")
	}
	if slots.Inflight(1) != 6 {
		t.Fatalf("Inflight = %d, want 6 real slots (12 units)", slots.Inflight(1))
	}
	// 轻模型仍无空位: 剩余额度 0。
	if slots.Available(1, SlotWeightIn(nil, "glm-5.3-flash")) {
		t.Fatal("no unit should remain after three weighted requests")
	}
	slots.Release(1, w)
	if !slots.Available(1, w) || slots.Available(1, w+2) {
		t.Fatal("freed 4 units: same weight fits, larger does not")
	}
	if !slots.TryAcquire(1, SlotUnitScale) {
		t.Fatal("half-slot request should fit in freed 4 units")
	}
	if slots.TryAcquire(1, w) {
		t.Fatal("only 2 units remain after half-slot fill")
	}
	// 权重配对错误只钳制到 0, 不产生负数。
	slots.Release(1, w)
	slots.Release(1, w)
	slots.Release(1, w)
	slots.Release(1, w)
	if slots.Inflight(1) != 0 {
		t.Fatalf("Inflight = %d after over-release, want 0", slots.Inflight(1))
	}
}
