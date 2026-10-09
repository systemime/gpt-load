package gateway

import (
	"testing"
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

	if !handler.credentialSlots.TryAcquire(1) {
		t.Fatal("occupying credential 1 failed")
	}
	engine := newAffinityTestEngine(t, handler)
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"parallel subagent burst"}]}`
	serveAffinityRequest(t, engine, body)

	handler.credentialSlots.Release(1)
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
	handler.credentialSlots.TryAcquire(1)
	handler.credentialSlots.TryAcquire(2)

	engine := newAffinityTestEngine(t, handler)
	serveAffinityRequest(t, engine, `{"model":"gpt-4o","messages":[{"role":"user","content":"burst"}]}`)

	if len(forwarder.inputs) == 0 {
		t.Fatal("request was not served while all credentials were at cap")
	}
}
