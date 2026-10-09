package state

import (
	"sync"
	"testing"
)

func TestCredentialSlotsAdmitUpToCapacity(t *testing.T) {
	slots := NewCredentialSlots(2)
	if !slots.TryAcquire(1) || !slots.TryAcquire(1) {
		t.Fatal("first two acquires should succeed")
	}
	if slots.TryAcquire(1) {
		t.Fatal("third acquire at capacity 2 should fail")
	}
	if slots.Inflight(1) != 2 {
		t.Fatalf("Inflight = %d, want 2", slots.Inflight(1))
	}
	slots.Release(1)
	if !slots.TryAcquire(1) {
		t.Fatal("acquire after release should succeed")
	}
}

func TestCredentialSlotsUnlimitedWhenCapacityZero(t *testing.T) {
	slots := NewCredentialSlots(0)
	for i := 0; i < 100; i++ {
		if !slots.TryAcquire(7) {
			t.Fatalf("acquire %d failed under unlimited capacity", i)
		}
	}
	if slots.Inflight(7) != 100 {
		t.Fatalf("Inflight = %d, want 100", slots.Inflight(7))
	}
}

func TestCredentialSlotsIndependentPerCredential(t *testing.T) {
	slots := NewCredentialSlots(1)
	if !slots.TryAcquire(1) {
		t.Fatal("credential 1 acquire failed")
	}
	if !slots.TryAcquire(2) {
		t.Fatal("credential 2 blocked by credential 1 at capacity")
	}
}

func TestCredentialSlotsConfigureAppliesImmediately(t *testing.T) {
	slots := NewCredentialSlots(1)
	if !slots.TryAcquire(1) || slots.TryAcquire(1) {
		t.Fatal("capacity 1 behavior unexpected")
	}
	slots.Configure(3)
	if !slots.TryAcquire(1) || !slots.TryAcquire(1) {
		t.Fatal("raised capacity should admit more")
	}
	if slots.TryAcquire(1) {
		t.Fatal("acquire beyond raised capacity should fail")
	}
	slots.Configure(0)
	if !slots.TryAcquire(1) {
		t.Fatal("unlimited configure should admit")
	}
}

func TestCredentialSlotsReleaseNeverGoesNegative(t *testing.T) {
	slots := NewCredentialSlots(2)
	slots.Release(1)
	slots.Release(1)
	if !slots.TryAcquire(1) || !slots.TryAcquire(1) || slots.TryAcquire(1) {
		t.Fatal("negative release corrupted capacity")
	}
}

func TestCredentialSlotsConcurrentAcquireRespectsCapacity(t *testing.T) {
	slots := NewCredentialSlots(8)
	var acquired sync.WaitGroup
	var mu sync.Mutex
	granted := 0
	for i := 0; i < 64; i++ {
		acquired.Add(1)
		go func() {
			defer acquired.Done()
			if slots.TryAcquire(9) {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}()
	}
	acquired.Wait()
	if granted != 8 {
		t.Fatalf("granted = %d, want 8", granted)
	}
}
