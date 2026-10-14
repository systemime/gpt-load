package gateway

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"
)

// 限流退避策略(无 Retry-After 的普通 429):
//   - 基础冷却 55~80s 随机(固定 60s 会让多凭证同步节拍);
//   - 429 持续超过 10 分钟进入指数退避, 共 10 轮: 2,4,8,...,120min(单轮封顶 2h);
//   - 10 轮全败 → 冷却 24 小时, 期满后重置为全新观察窗;
//   - 任一成功请求立即清零(Retry-After 精确恢复同样清零)。
const (
	escalationBaseCooldown  = time.Minute
	escalationRoundCap      = 2 * time.Hour
	escalationMaxRounds     = 10
	escalationStreakWindow  = 10 * time.Minute
	escalationFinalCooldown = 24 * time.Hour
	escalationJitterLow     = 5 * time.Second
	escalationJitterHigh    = 20 * time.Second
)

type escalationEntry struct {
	streakStart time.Time
	round       int
	lastUntil   time.Time
	lastCool    time.Duration
}

// rateLimitEscalation 记录凭证(或 凭证+模型)的连续 429 退避状态。
type rateLimitEscalation struct {
	mu     sync.Mutex
	states map[string]escalationEntry
}

func newRateLimitEscalation() *rateLimitEscalation {
	return &rateLimitEscalation{states: make(map[string]escalationEntry)}
}

// escalationKey 标识退避主体: 凭证级 429 用凭据号, 模型级 429 追加模型名。
func escalationKey(credentialID uint, model string) string {
	if model == "" {
		return fmt.Sprintf("c%d", credentialID)
	}
	return fmt.Sprintf("c%d|%s", credentialID, model)
}

// until 返回该主体的本次冷却截止时间; center 为 judge 计算的基础冷却
// (API key 组 60s, 订阅组 10min), 抖动与指数轮次都以它为基准。
func (e *rateLimitEscalation) until(key string, center time.Duration, now time.Time) time.Time {
	return now.Add(e.cooldown(key, center, now))
}

func (e *rateLimitEscalation) cooldown(key string, center time.Duration, now time.Time) time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.states == nil {
		e.states = make(map[string]escalationEntry)
	}
	entry, ok := e.states[key]
	if !ok || now.After(entry.lastUntil.Add(2*entry.lastCool)) {
		// 首次 429 或距离上次冷却结束已久: 全新观察窗。
		entry = escalationEntry{streakStart: now, round: 0}
		entry.lastCool = jitterCooldown(center)
		entry.lastUntil = now.Add(entry.lastCool)
		e.states[key] = entry
		return entry.lastCool
	}
	if now.Sub(entry.streakStart) <= escalationStreakWindow {
		entry.lastCool = jitterCooldown(center)
		entry.lastUntil = now.Add(entry.lastCool)
		e.states[key] = entry
		return entry.lastCool
	}
	// 持续 429 超过观察窗: 指数退避。单轮封顶 escalationRoundCap。
	entry.round++
	if entry.round >= escalationMaxRounds {
		entry.round = 0
		entry.streakStart = now.Add(escalationFinalCooldown)
		entry.lastCool = escalationFinalCooldown
		entry.lastUntil = now.Add(escalationFinalCooldown)
		e.states[key] = entry
		return escalationFinalCooldown
	}
	entry.lastCool = min(center<<entry.round, escalationRoundCap)
	entry.lastUntil = now.Add(entry.lastCool)
	e.states[key] = entry
	return entry.lastCool
}

// forget 成功即清零; model 为空时连带清除该凭证所有模型级状态。
func (e *rateLimitEscalation) forget(credentialID uint, model string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.states == nil {
		return
	}
	prefix := fmt.Sprintf("c%d", credentialID)
	if model == "" {
		for key := range e.states {
			if key == prefix || strings.HasPrefix(key, prefix+"|") {
				delete(e.states, key)
			}
		}
		return
	}
	delete(e.states, escalationKey(credentialID, model))
}

// jitterCooldown 基础冷却抖动: 中枢 -5s~+20s 随机, 避免多凭证同步节拍。
func jitterCooldown(center time.Duration) time.Duration {
	span := int64((escalationJitterHigh - escalationJitterLow) / time.Millisecond)
	return center - escalationJitterLow + time.Duration(rand.Int64N(span))*time.Millisecond
}
