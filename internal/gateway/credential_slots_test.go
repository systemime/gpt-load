package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// cap=1 且凭证 1 已占满时, 新请求必须溢出到另一凭证;
// 溢出成功后亲和映射改写, 后续请求跟随新凭证(自愈迁移)。
func TestHandlerOverflowsCredentialAtConcurrencyCapAndRelearnsAffinity(t *testing.T) {
	forwarder := &scriptedForwarder{results: successfulAffinityResults(2)}
	handler, manager, _ := newHandlerForTest(t, forwarder, "sk-one", "sk-two")
	sink := &recordingRequestLogSink{}
	handler.requestLogSink = sink

	snapshot := manager.Current()
	snapshot.Settings.CredentialConcurrencyLimit = 1
	handler.credentialSlots.Configure(1)

	if !handler.credentialSlots.TryAcquire(1, 1) {
		t.Fatal("occupying credential 1 failed")
	}
	engine := newAffinityTestEngine(t, handler)
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"parallel subagent burst"}]}`
	serveAffinityRequest(t, engine, body)

	handler.credentialSlots.Release(1, 1)
	serveAffinityRequest(t, engine, body)

	assertAffinityAttemptKeys(t, forwarder.inputs, []string{"sk-two", "sk-two"})
	assertAffinityHits(t, sink.snapshot(), []bool{false, true})
}

// 全部凭证占满时仍需可服务(全池豁免), 不能因 cap 直接 503。
func TestHandlerServesWhenAllCredentialsCapped(t *testing.T) {
	forwarder := &scriptedForwarder{results: successfulAffinityResults(1)}
	handler, manager, _ := newHandlerForTest(t, forwarder, "sk-one", "sk-two")

	snapshot := manager.Current()
	snapshot.Settings.CredentialConcurrencyLimit = 1
	handler.credentialSlots.Configure(1)
	handler.credentialSlots.TryAcquire(1, 1)
	handler.credentialSlots.TryAcquire(2, 1)

	engine := newAffinityTestEngine(t, handler)
	serveAffinityRequest(t, engine, `{"model":"gpt-4o","messages":[{"role":"user","content":"burst"}]}`)

	if len(forwarder.inputs) == 0 {
		t.Fatal("request was not served while all credentials were at cap")
	}
}

// 组预算(cap+slack)内排队立即拿到豁免槽位并正常服务。
func TestHandlerQueuesBeyondCapWithinGroupBudget(t *testing.T) {
	forwarder := &scriptedForwarder{results: successfulAffinityResults(1)}
	handler, manager, _ := newHandlerForTest(t, forwarder, "sk-one", "sk-two")

	snapshot := manager.Current()
	snapshot.Settings.CredentialConcurrencyLimit = 1
	handler.credentialSlots.Configure(1)
	handler.credentialSlots.TryAcquire(1, 1)
	handler.credentialSlots.TryAcquire(2, 1)

	engine := newAffinityTestEngine(t, handler)
	serveAffinityRequest(t, engine, `{"model":"gpt-4o","messages":[{"role":"user","content":"queued burst"}]}`)

	if len(forwarder.inputs) == 0 {
		t.Fatal("queued request should be admitted within group budget")
	}
}

// 组预算耗尽(cap+slack 全满)时排队等待, 超时返回 429 + Retry-After。
func TestHandlerQueueTimeoutReturns429(t *testing.T) {
	forwarder := &scriptedForwarder{results: successfulAffinityResults(1)}
	handler, manager, _ := newHandlerForTest(t, forwarder, "sk-one", "sk-two")

	snapshot := manager.Current()
	snapshot.Settings.CredentialConcurrencyLimit = 1
	snapshot.Settings.CredentialQueueTimeout = 50 * time.Millisecond
	handler.credentialSlots.Configure(1)
	// 预算 = cap(1) + slack(2) = 3: 把两个凭证都填满到预算。
	for i := 0; i < 3; i++ {
		handler.credentialSlots.TryAcquireWithin(1, 1, 3)
		handler.credentialSlots.TryAcquireWithin(2, 1, 3)
	}

	engine := newAffinityTestEngine(t, handler)
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		bytes.NewBufferString(`{"model":"gpt-4o","messages":[{"role":"user","content":"over budget"}]}`),
	)
	request.Header.Set("Authorization", "Bearer gl-client")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("response = %d %s, want 429", response.Code, response.Body.String())
	}
	if response.Header().Get("Retry-After") == "" {
		t.Fatal("429 queue timeout should carry Retry-After")
	}
	if len(forwarder.inputs) != 0 {
		t.Fatal("request beyond group budget must not reach upstream")
	}
}

// Q2 严格语义: 选中凭证已达预算, 但另一凭证还在 cap → 不排队,
// 预算档重选到在途最小的凭证并重建 Selection 立即服务。
func TestHandlerBudgetTierRequeuesOnlyWhenAllKeysAtBudget(t *testing.T) {
	forwarder := &scriptedForwarder{results: successfulAffinityResults(1)}
	handler, manager, _ := newHandlerForTest(t, forwarder, "sk-one", "sk-two")

	snapshot := manager.Current()
	snapshot.Settings.CredentialConcurrencyLimit = 1
	snapshot.Settings.CredentialQueueTimeout = 5 * time.Second
	handler.credentialSlots.Configure(1)
	// 凭证 1 填满到预算(3), 凭证 2 只在 cap(1): 公平序会选 1, 但 2 有预算内空位。
	for i := 0; i < 3; i++ {
		handler.credentialSlots.TryAcquireWithin(1, 1, 3)
	}
	handler.credentialSlots.TryAcquire(2, 1)

	engine := newAffinityTestEngine(t, handler)
	started := time.Now()
	serveAffinityRequest(t, engine, `{"model":"gpt-4o","messages":[{"role":"user","content":"budget tier"}]}`)
	if time.Since(started) > 2*time.Second {
		t.Fatalf("served in %v, want immediate (no queue while budget room exists)", time.Since(started))
	}
	assertAffinityAttemptKeys(t, forwarder.inputs, []string{"sk-two"})
}

// Q1: 全部凭证达预算时排队; 其他 key 释放后唤醒并重选到该 key, 不等原选择。
func TestHandlerQueueWakesOnOtherKeyReleaseAndRepicks(t *testing.T) {
	forwarder := &scriptedForwarder{results: successfulAffinityResults(1)}
	handler, manager, _ := newHandlerForTest(t, forwarder, "sk-one", "sk-two")

	snapshot := manager.Current()
	snapshot.Settings.CredentialConcurrencyLimit = 1
	snapshot.Settings.CredentialQueueTimeout = 5 * time.Second
	handler.credentialSlots.Configure(1)
	for i := 0; i < 3; i++ {
		handler.credentialSlots.TryAcquireWithin(1, 1, 3)
		handler.credentialSlots.TryAcquireWithin(2, 1, 3)
	}

	engine := newAffinityTestEngine(t, handler)
	done := make(chan struct{})
	go func() {
		defer close(done)
		// 等待者登记后释放凭证 2 的一个预算槽, 排队请求应唤醒并落到 sk-two。
		deadline := time.Now().Add(2 * time.Second)
		for handler.credentialSlots.Waiting(2) == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		handler.credentialSlots.Release(2, 1)
	}()
	serveAffinityRequest(t, engine, `{"model":"gpt-4o","messages":[{"role":"user","content":"queued repick"}]}`)
	<-done
	assertAffinityAttemptKeys(t, forwarder.inputs, []string{"sk-two"})
}

// 回归: 重试路径(429 换凭证重试成功)结束后, 所有涉及凭证的在途计数必须归零
// —— 同时覆盖双重释放(净多扣导致负数被 floor 吞掉)与早退泄漏两种账本破坏。
func TestHandlerSlotsReturnToZeroAfterRetryPath(t *testing.T) {
	forwarder := &scriptedForwarder{results: []UpstreamResult{
		successfulAffinityResult(),
		{
			StatusCode:         http.StatusTooManyRequests,
			Header:             http.Header{"Retry-After": {"30"}},
			Body:               []byte(`{"error":"rate_limit"}`),
			ClassificationBody: []byte(`{"error":"rate_limit"}`),
			RequestWritten:     true,
		},
		successfulAffinityResult(),
	}}
	handler, manager, _ := newHandlerForTest(t, forwarder, "sk-one", "sk-two")
	sink := &recordingRequestLogSink{}
	handler.requestLogSink = sink

	snapshot := manager.Current()
	snapshot.Settings.RetryCount = 2

	engine := newAffinityTestEngine(t, handler)
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"retry accounting"}]}`
	serveAffinityRequest(t, engine, body)
	serveAffinityRequest(t, engine, body)

	for _, id := range []uint{1, 2} {
		if inflight := handler.credentialSlots.Inflight(id); inflight != 0 {
			t.Fatalf("credential %d inflight = %d after completion, want 0", id, inflight)
		}
	}
}
