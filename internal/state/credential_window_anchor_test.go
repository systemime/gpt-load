package state

import (
	"testing"
)

func TestWindowAnchorLifecycle(t *testing.T) {
	registry := NewCredentialRegistry()
	const id = uint(7)

	if anchors := registry.WindowAnchors(); len(anchors) != 0 {
		t.Fatalf("initial anchors = %#v, want empty", anchors)
	}

	// 首次分配开窗: anchor = 该请求时刻。
	if !registry.BeginWindowIfNone(id, 1000) {
		t.Fatal("first allocation should open the window")
	}
	if registry.BeginWindowIfNone(id, 2000) {
		t.Fatal("second allocation must not reopen the window")
	}
	// 分配同时刷新活跃时间。
	anchors := registry.WindowAnchors()
	if anchors[id] != 1000 {
		t.Fatalf("anchor = %d, want 1000", anchors[id])
	}

	// 空闲 <5h: 07:00 检查保留锚点(活跃时间已被第二次分配刷新到 2000)。
	if reset := registry.ResetIdleWindowAnchors(2000 + 4*3600*1000); len(reset) != 0 {
		t.Fatalf("reset = %#v, want none (idle < 5h)", reset)
	}

	// 空闲 ≥5h: 清空锚点, 等下一个请求重新开窗。
	if reset := registry.ResetIdleWindowAnchors(2000 + 5*3600*1000); len(reset) != 1 {
		t.Fatalf("reset = %#v, want exactly one credential", reset)
	}
	if anchors := registry.WindowAnchors(); len(anchors) != 0 {
		t.Fatalf("anchors after reset = %#v, want empty", anchors)
	}
	// 清空后的第一次分配重新开窗。
	if !registry.BeginWindowIfNone(id, 9000) {
		t.Fatal("allocation after reset should open a new window")
	}
	if anchors := registry.WindowAnchors(); anchors[id] != 9000 {
		t.Fatalf("anchor = %d, want 9000", anchors[id])
	}
}

func TestWindowAnchorCheckpointRoundTrip(t *testing.T) {
	registry := NewCredentialRegistry()
	if err := registry.ReplaceCredentials([]CredentialEntry{{
		ID: 3, GroupID: 17, Version: 1, IdentityGeneration: 1,
		Fingerprint: "fp", Status: CredentialStatusActive, EncryptedValue: "cipher",
	}}); err != nil {
		t.Fatalf("ReplaceCredentials() error = %v", err)
	}
	registry.BeginWindowIfNone(3, 5000)

	checkpoints := registry.CaptureRuntimeCheckpoint()
	if len(checkpoints) == 0 {
		t.Fatal("no checkpoints captured")
	}

	restored := NewCredentialRegistry()
	if err := restored.ReplaceCredentials([]CredentialEntry{{
		ID: 3, GroupID: 17, Version: 1, IdentityGeneration: 1,
		Fingerprint: "fp", Status: CredentialStatusActive, EncryptedValue: "cipher",
	}}); err != nil {
		t.Fatalf("ReplaceCredentials() error = %v", err)
	}
	if restored.RestoreRuntimeCheckpoint(checkpoints) == 0 {
		t.Fatal("restore applied nothing")
	}
	if anchors := restored.WindowAnchors(); anchors[3] != 5000 {
		t.Fatalf("restored anchor = %#v, want 5000", anchors)
	}
}
