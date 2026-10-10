package state

import (
	"sync"
	"testing"
	"time"
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

func TestCredentialSlotsAcquireWithinWaitWakesOnRelease(t *testing.T) {
	slots := NewCredentialSlots(1)
	if !slots.TryAcquireWithin(3, 3) || !slots.TryAcquireWithin(3, 3) || !slots.TryAcquireWithin(3, 3) {
		t.Fatal("fill budget failed")
	}
	if slots.TryAcquireWithin(3, 3) {
		t.Fatal("acquire beyond limit should fail")
	}
	if slots.Waiting(3) != 0 {
		t.Fatalf("Waiting = %d before wait, want 0", slots.Waiting(3))
	}
	acquired := make(chan bool, 1)
	go func() {
		acquired <- slots.AcquireWithinWait(3, 3, 5*time.Second, nil)
	}()
	// 等待者登记后再释放, 确保走唤醒路径而非快路径。
	deadline := time.Now().Add(time.Second)
	for slots.Waiting(3) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if slots.Waiting(3) != 1 {
		t.Fatal("waiter not registered")
	}
	slots.Release(3)
	if !<-acquired {
		t.Fatal("AcquireWithinWait did not wake on release")
	}
	if slots.Inflight(3) != 3 {
		t.Fatalf("Inflight = %d, want 3", slots.Inflight(3))
	}
}

func TestCredentialSlotsAcquireWithinWaitTimesOut(t *testing.T) {
	slots := NewCredentialSlots(4)
	if !slots.TryAcquireWithin(5, 2) || !slots.TryAcquireWithin(5, 2) {
		t.Fatal("fill budget failed")
	}
	started := time.Now()
	if slots.AcquireWithinWait(5, 2, 30*time.Millisecond, nil) {
		t.Fatal("wait should time out while budget is held")
	}
	if time.Since(started) < 20*time.Millisecond {
		t.Fatal("returned before timeout")
	}
	if slots.Waiting(5) != 0 {
		t.Fatalf("Waiting after timeout = %d, want 0", slots.Waiting(5))
	}
}

func TestCredentialSlotsAcquireWithinWaitHonorsDone(t *testing.T) {
	slots := NewCredentialSlots(1)
	slots.TryAcquire(7)
	done := make(chan struct{})
	close(done)
	if slots.AcquireWithinWait(7, 1, 5*time.Second, done) {
		t.Fatal("wait should abort when done closes")
	}
}

func TestCredentialSlotsAcquireAnyWithinWakesOnOtherKeyRelease(t *testing.T) {
	slots := NewCredentialSlots(1)
	// 预算 3: 两个凭证都填满到预算。
	for i := 0; i < 3; i++ {
		slots.TryAcquireWithin(11, 3)
		slots.TryAcquireWithin(12, 3)
	}
	acquired := make(chan uint, 1)
	go func() {
		id, ok := slots.AcquireAnyWithin([]uint{11, 12}, 0, 5*time.Second, nil)
		if !ok {
			acquired <- 0
			return
		}
		acquired <- id
	}()
	deadline := time.Now().Add(time.Second)
	for slots.Waiting(11) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	slots.Release(12) // 另一个 key 释放, 等待者应被唤醒并选中 12。
	select {
	case id := <-acquired:
		if id != 12 {
			t.Fatalf("acquired = %d, want 12 (freed key)", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter not woken by other-key release")
	}
}

func TestCredentialSlotsAcquireAnyWithinPrefersPreferredWhenFree(t *testing.T) {
	slots := NewCredentialSlots(1) // 预算 = 1+2 = 3
	slots.TryAcquire(11)           // 11 在途 1, 12 在途 0
	id, ok := slots.AcquireAnyWithin([]uint{12, 11}, 11, time.Second, nil)
	if !ok || id != 11 {
		t.Fatalf("acquired = (%d, %v), want preferred 11", id, ok)
	}
	// preferred 满预算(3)时退到其余候选中在途最小者。
	for i := 0; i < 2; i++ {
		slots.TryAcquireWithin(11, 3)
	}
	id, ok = slots.AcquireAnyWithin([]uint{11, 12}, 11, time.Second, nil)
	if !ok || id != 12 {
		t.Fatalf("fallback acquired = (%d, %v), want 12", id, ok)
	}
}

func TestCredentialSlotsPerCredentialLimitOverridesGlobal(t *testing.T) {
	slots := NewCredentialSlots(4)
	slots.SetLimits(map[uint]int64{11: 2})
	if !slots.TryAcquire(11) || !slots.TryAcquire(11) {
		t.Fatal("override cap 2 should admit two")
	}
	if slots.TryAcquire(11) {
		t.Fatal("override cap 2 should reject the third")
	}
	if !slots.TryAcquire(12) || !slots.TryAcquire(12) || !slots.TryAcquire(12) || !slots.TryAcquire(12) {
		t.Fatal("global cap 4 should still admit four for other ids")
	}
	if slots.Available(11) || slots.Available(12) {
		t.Fatal("both ids are at their effective limits")
	}
	if !slots.Available(13) {
		t.Fatal("untouched id should remain available under global cap")
	}
	if slots.EffectiveLimit(11) != 2 || slots.EffectiveLimit(12) != 4 {
		t.Fatalf("effective limits = (%d, %d), want (2, 4)", slots.EffectiveLimit(11), slots.EffectiveLimit(12))
	}
	// 清除覆盖后回落全局。
	slots.SetLimits(map[uint]int64{})
	if !slots.TryAcquire(11) || !slots.TryAcquire(11) {
		t.Fatal("cleared override should fall back to global cap 4")
	}
}
