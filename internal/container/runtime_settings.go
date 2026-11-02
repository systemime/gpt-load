package container

import "gpt-load/internal/state"

type retentionSnapshotProvider struct {
	manager *state.Manager
}

func (provider retentionSnapshotProvider) RequestLogRetentionDays() int {
	snapshot := provider.manager.Current()
	if snapshot == nil {
		return state.DefaultRuntimeSettings().RequestLogRetentionDays
	}
	return snapshot.Settings.RequestLogRetentionDays
}

// quotaWeightsSnapshotProvider 把快照里的生效模型倍率表提供给 requestlog 聚合。
type quotaWeightsSnapshotProvider struct {
	manager *state.Manager
}

func (provider quotaWeightsSnapshotProvider) ModelQuotaWeights() map[string]float64 {
	snapshot := provider.manager.Current()
	if snapshot == nil {
		return nil
	}
	return snapshot.ModelQuotaWeights
}
