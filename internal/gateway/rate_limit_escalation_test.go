package gateway

import (
	"testing"
	"time"
)

func TestRateLimitEscalationJitterBase(t *testing.T) {
	e := newRateLimitEscalation()
	now := time.Now()
	got := e.cooldown("c1", time.Minute, now)
	if got < time.Minute-5*time.Second || got > time.Minute+20*time.Second {
		t.Fatalf("base cooldown = %v, want 60s ±[-5s,+20s]", got)
	}
}

func TestRateLimitEscalationBackoffRounds(t *testing.T) {
	e := newRateLimitEscalation()
	now := time.Now()
	// 持续 429: 每次冷却到期立即重试, 观察窗(10min)内保持基础抖动。
	e.cooldown("c1", time.Minute, now)
	var round1 time.Duration
	for step := 1; step <= 12; step++ {
		got := e.cooldown("c1", time.Minute, now.Add(time.Duration(step)*time.Minute))
		if got >= 2*time.Minute {
			round1 = got
			break
		}
	}
	if round1 != 2*time.Minute {
		t.Fatalf("round1 = %v, want 2m", round1)
	}
	round2 := e.cooldown("c1", time.Minute, now.Add(13*time.Minute).Add(round1))
	if round2 != 4*time.Minute {
		t.Fatalf("round2 = %v, want 4m", round2)
	}
	// 逐轮推进到第 10 轮失败 → 24h。
	cursor := now.Add(13*time.Minute + round1 + round2)
	var final time.Duration
	for round := 3; round <= escalationMaxRounds; round++ {
		final = e.cooldown("c1", time.Minute, cursor)
		if final == escalationFinalCooldown && round != escalationMaxRounds {
			t.Fatalf("round %d escalated to final early: %v", round, final)
		}
		cursor = cursor.Add(final)
	}
	if final != escalationFinalCooldown {
		t.Fatalf("final = %v, want 24h", final)
	}
}

func TestRateLimitEscalationGapRestartsWindow(t *testing.T) {
	e := newRateLimitEscalation()
	now := time.Now()
	e.cooldown("c1", time.Minute, now)
	e.cooldown("c1", time.Minute, now.Add(11*time.Minute))
	// 冷却结束后远超 2 倍周期才再来 429: 全新观察窗, 回到基础抖动。
	later := now.Add(11 * time.Minute).Add(2 * time.Minute).Add(2 * time.Hour)
	if got := e.cooldown("c1", time.Minute, later); got > time.Minute+20*time.Second {
		t.Fatalf("post-gap cooldown = %v, want fresh jittered base", got)
	}
}

func TestRateLimitEscalationForgetOnSuccess(t *testing.T) {
	e := newRateLimitEscalation()
	now := time.Now()
	e.cooldown("c1", time.Minute, now)
	e.cooldown("c1|gpt-4o", time.Minute, now)
	e.forget(1, "")
	if _, ok := e.states["c1"]; ok {
		t.Fatal("credential state survived forget")
	}
	if _, ok := e.states["c1|gpt-4o"]; ok {
		t.Fatal("model state survived credential-level forget")
	}
}
