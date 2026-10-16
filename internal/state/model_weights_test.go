package state

import "testing"

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
