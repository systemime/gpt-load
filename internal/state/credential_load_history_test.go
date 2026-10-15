package state

import (
	"testing"
)

func TestLoadHistoryRingAndSilentDelta(t *testing.T) {
	registry := NewCredentialRegistry()

	if got := registry.LoadHistorySnapshot(); got == nil || len(got) != 0 {
		t.Fatalf("empty snapshot = %#v, want empty non-nil slice", got)
	}

	registry.CountSilentTransfer()
	registry.CountSilentTransfer()
	registry.RecordLoadSample(1000, 3, 0)
	registry.CountSilentTransfer()
	registry.RecordLoadSample(2000, 5, 1)

	samples := registry.LoadHistorySnapshot()
	if len(samples) != 2 {
		t.Fatalf("snapshot len = %d, want 2", len(samples))
	}
	if samples[0].AtMS != 1000 || samples[0].Inflight != 3 ||
		samples[0].SilentTransfers != 2 {
		t.Fatalf("sample[0] = %#v", samples[0])
	}
	if samples[1].SilentTransfers != 1 {
		t.Fatalf("sample[1].SilentTransfers = %d, want delta 1", samples[1].SilentTransfers)
	}

	// 环回覆盖: 超容量后只保留最近 300 个, 顺序仍为时间升序。
	for i := 0; i < LoadHistoryCapacity+5; i++ {
		registry.RecordLoadSample(int64(3000+i), int64(i), 0)
	}
	samples = registry.LoadHistorySnapshot()
	if len(samples) != LoadHistoryCapacity {
		t.Fatalf("ring snapshot len = %d, want %d", len(samples), LoadHistoryCapacity)
	}
	if samples[0].AtMS != 3005 || samples[len(samples)-1].AtMS != int64(3000+LoadHistoryCapacity+4) {
		t.Fatalf("ring order broken: first=%d last=%d", samples[0].AtMS, samples[len(samples)-1].AtMS)
	}
}
