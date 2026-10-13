package gateway

import (
	"testing"

	"gpt-load/internal/state"
)

// 全池在途满载判定: 任一空闲即 false, 全满(跳过零引用)才 true, 空集恒 false。
func TestHandlerAllowedCredentialsAllAtCapacity(t *testing.T) {
	slots := state.NewCredentialSlots(0)
	slots.Configure(1)
	handler := &Handler{credentialSlots: slots}
	refs := map[uint]state.CredentialRef{
		1: {ID: 1, GroupID: 1, IdentityGeneration: 1},
		2: {ID: 2, GroupID: 1, IdentityGeneration: 2},
	}
	if handler.allowedCredentialsAllAtCapacity(refs) {
		t.Fatal("empty pool reported at capacity")
	}
	slots.ForceAcquire(1)
	if handler.allowedCredentialsAllAtCapacity(refs) {
		t.Fatal("one free credential reported at capacity")
	}
	slots.ForceAcquire(2)
	if !handler.allowedCredentialsAllAtCapacity(refs) {
		t.Fatal("full pool not reported at capacity")
	}
	refs[0] = state.CredentialRef{}
	if !handler.allowedCredentialsAllAtCapacity(refs) {
		t.Fatal("zero id not skipped")
	}
	if handler.allowedCredentialsAllAtCapacity(map[uint]state.CredentialRef{}) {
		t.Fatal("empty refs reported at capacity")
	}
	slots.Release(1)
	if handler.allowedCredentialsAllAtCapacity(refs) {
		t.Fatal("released slot should free capacity")
	}
}
