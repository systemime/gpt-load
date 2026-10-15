package control

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"gpt-load/internal/channel"
	"gpt-load/internal/execution"
	"gpt-load/internal/health"
	"gpt-load/internal/platform/encryption"
	"gpt-load/internal/state"
)

const (
	defaultValidationInterval = 10 * time.Minute
	maxValidationJitter       = 3 * time.Minute
	retentionInterval         = time.Hour
	credentialUsageInterval   = time.Minute
	loadHistoryInterval       = time.Second
	windowResetCheckInterval  = 20 * time.Second
)

type credentialMutationCoordinator interface {
	Do(uint, func())
}

type operationRecoveryRuntime interface {
	RunOperationRecovery(context.Context)
}

type catalogSyncRuntime interface {
	Run(context.Context)
}

// RequestLogCleaner is the control-owned scheduling view of request log
// retention. The requestlog package owns all cleanup semantics.
type RequestLogCleaner interface {
	Sweep(context.Context, time.Time)
}

type credentialStageCleaner interface {
	CleanupCredentialStages(context.Context, time.Time) error
}

// credentialUsageReader 由 requestlog.Service 实现: 聚合滚动窗口用量,
// 供用量均衡(阶段 3)的后台刷新循环写入注册表 tracker。
type credentialUsageReader interface {
	CredentialWindowUsage(context.Context, int64, map[uint]int64) (map[uint]state.CredentialUsage, error)
}

type runtimeTicker interface {
	C() <-chan time.Time
	Stop()
}

type standardRuntimeTicker struct {
	ticker *time.Ticker
}

func (ticker standardRuntimeTicker) C() <-chan time.Time {
	return ticker.ticker.C
}

func (ticker standardRuntimeTicker) Stop() {
	ticker.ticker.Stop()
}

type Runtime struct {
	registry           *state.CredentialRegistry
	validator          validationSweep
	usageReader        credentialUsageReader
	requestLogCleaner  RequestLogCleaner
	stageCleaner       credentialStageCleaner
	operationRecovery  operationRecoveryRuntime
	catalogSync        catalogSyncRuntime
	oauthCallback      *OAuthCallbackManager
	manager            *state.Manager
	validationInterval time.Duration
	validationJitter   func() time.Duration
	now                func() time.Time
	newTicker          func(time.Duration) runtimeTicker
}

func NewRuntime(
	registry *state.CredentialRegistry,
	stats *health.StatsStore,
	mutations *health.MutationCoordinator,
	manager *state.Manager,
	encryptionService encryption.Service,
	channelRegistry *channel.Registry,
	executor execution.Executor,
	requestLogCleaner RequestLogCleaner,
	operationRecovery *Service,
	catalogSync *CatalogSyncCoordinator,
) *Runtime {
	runtime := &Runtime{
		registry:           registry,
		requestLogCleaner:  requestLogCleaner,
		stageCleaner:       operationRecovery,
		operationRecovery:  operationRecovery,
		catalogSync:        catalogSync,
		manager:            manager,
		validationInterval: defaultValidationInterval,
		validationJitter: func() time.Duration {
			return time.Duration(rand.Int64N(int64(maxValidationJitter) + 1))
		},
		now: time.Now,
		newTicker: func(interval time.Duration) runtimeTicker {
			return standardRuntimeTicker{ticker: time.NewTicker(interval)}
		},
	}
	if operationRecovery != nil {
		runtime.oauthCallback = operationRecovery.oauthCallback
	}
	if reader, ok := requestLogCleaner.(credentialUsageReader); ok {
		runtime.usageReader = reader
	}
	runtime.validator = newValidationWorker(
		manager,
		registry,
		stats,
		mutations,
		encryptionService,
		channelRegistry,
		executor,
	)
	return runtime
}

func (runtime *Runtime) Run(ctx context.Context) {
	currentValidationInterval, validationUpdates := runtime.currentValidationSchedule()
	validationTicker := runtime.newTicker(validationTickerInterval(
		currentValidationInterval,
		runtime.validationJitter(),
	))

	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		runtime.runValidation(ctx, validationTicker, currentValidationInterval, validationUpdates)
	}()
	if runtime.requestLogCleaner != nil || runtime.stageCleaner != nil {
		retentionTicker := runtime.newTicker(retentionInterval)
		wait.Add(1)
		go func() {
			defer wait.Done()
			runtime.runRetention(ctx, retentionTicker)
		}()
	}
	if runtime.operationRecovery != nil {
		wait.Add(1)
		go func() {
			defer wait.Done()
			runtime.operationRecovery.RunOperationRecovery(ctx)
		}()
	}
	if runtime.catalogSync != nil {
		wait.Add(1)
		go func() {
			defer wait.Done()
			runtime.catalogSync.Run(ctx)
		}()
	}
	if runtime.oauthCallback != nil {
		wait.Add(1)
		go func() {
			defer wait.Done()
			runtime.oauthCallback.Run(ctx)
		}()
	}
	if runtime.usageReader != nil {
		usageTicker := runtime.newTicker(credentialUsageInterval)
		wait.Add(1)
		go func() {
			defer wait.Done()
			runtime.runCredentialUsage(ctx, usageTicker)
		}()
	}
	if runtime.registry != nil {
		loadHistoryTicker := runtime.newTicker(loadHistoryInterval)
		wait.Add(1)
		go func() {
			defer wait.Done()
			runtime.runLoadHistory(ctx, loadHistoryTicker)
		}()
		windowResetTicker := runtime.newTicker(windowResetCheckInterval)
		wait.Add(1)
		go func() {
			defer wait.Done()
			runtime.runWindowAnchorReset(ctx, windowResetTicker)
		}()
	}
	wait.Wait()
}

// runWindowAnchorReset 每天 07:00(Asia/Shanghai) 检查一次: 空闲 ≥5h 的凭证
// 清空 5h 窗口锚点, 窗口起点等该凭证收到下一个请求时才重新划分。
// 07:00 只是检查点, 不是窗口起点。
func (runtime *Runtime) runWindowAnchorReset(ctx context.Context, ticker runtimeTicker) {
	defer func() { ticker.Stop() }()
	cst := time.FixedZone("CST", 8*60*60)
	lastRunDate := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			if ctx.Err() != nil {
				return
			}
			now := runtime.now().In(cst)
			dateKey := now.Format("2006-01-02")
			if now.Hour() != 7 || now.Minute() != 0 || now.Second() > 30 || lastRunDate == dateKey {
				continue
			}
			lastRunDate = dateKey
			reset := runtime.registry.ResetIdleWindowAnchors(runtime.now().UnixMilli())
			if len(reset) > 0 {
				logrus.WithField("event", "window_anchor_reset").
					WithField("credential_ids", reset).
					Info("Idle credentials reset for 5h quota window")
			}
		}
	}
}

// runLoadHistory 每秒采样总在途/排队写入注册表环形缓冲, 供负载页折线图。
// 静默转移计数由网关递增, 这里只读快照, 采样开销为原子读+小循环。
func (runtime *Runtime) runLoadHistory(ctx context.Context, ticker runtimeTicker) {
	defer func() { ticker.Stop() }()
	record := func() {
		slots := runtime.registry.Slots()
		var inflight, queued int64
		for _, view := range runtime.registry.Snapshot() {
			inflight += slots.Inflight(view.ID)
			queued += slots.Waiting(view.ID)
		}
		runtime.registry.RecordLoadSample(runtime.now().UnixMilli(), inflight, queued)
	}
	record()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			if ctx.Err() != nil {
				return
			}
			record()
		}
	}
}

// runCredentialUsage 周期聚合滚动窗口用量并刷新注册表 tracker。
// 失败只记日志不中断, 下一轮重试; tracker 保持上一份成功快照。
func (runtime *Runtime) runCredentialUsage(ctx context.Context, ticker runtimeTicker) {
	defer func() { ticker.Stop() }()
	refresh := func() {
		// 周窗口锚点来自运行时设置; 0 表示滚动 7 天。
		var weekAnchorMS int64
		if snapshot := runtime.manager.Current(); snapshot != nil {
			weekAnchorMS = snapshot.Settings.GlmWeekResetAnchorMS
		}
		usage, err := runtime.usageReader.CredentialWindowUsage(ctx, weekAnchorMS, runtime.registry.WindowAnchors())
		if err != nil {
			if ctx.Err() == nil {
				logrus.WithError(err).Warn("Refresh credential window usage failed")
			}
			return
		}
		runtime.registry.UsageTracker().Refresh(usage)
	}
	refresh()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			if ctx.Err() != nil {
				return
			}
			refresh()
		}
	}
}

func (runtime *Runtime) runValidation(
	ctx context.Context,
	ticker runtimeTicker,
	interval time.Duration,
	updates <-chan struct{},
) {
	defer func() { ticker.Stop() }()
	for {
		select {
		case <-ctx.Done():
			return
		case <-updates:
			nextInterval, nextUpdates := runtime.currentValidationSchedule()
			updates = nextUpdates
			if nextInterval == interval {
				continue
			}
			ticker.Stop()
			interval = nextInterval
			ticker = runtime.newTicker(validationTickerInterval(interval, runtime.validationJitter()))
		case <-ticker.C():
			if ctx.Err() != nil {
				return
			}
			if runtime.validator != nil {
				runtime.validator.Validate(ctx)
			}
		}
	}
}

func validationTickerInterval(interval, jitter time.Duration) time.Duration {
	const maxDuration = time.Duration(1<<63 - 1)
	if jitter <= 0 {
		return interval
	}
	if interval > maxDuration-jitter {
		return maxDuration
	}
	return interval + jitter
}

func (runtime *Runtime) currentValidationSchedule() (time.Duration, <-chan struct{}) {
	interval := runtime.validationInterval
	if interval <= 0 {
		interval = defaultValidationInterval
	}
	if runtime.manager == nil {
		return interval, nil
	}
	snapshot, updates := runtime.manager.CurrentWithUpdates()
	if snapshot != nil && snapshot.Settings.ValidationInterval > 0 {
		interval = snapshot.Settings.ValidationInterval
	}
	return interval, updates
}

func (runtime *Runtime) runRetention(ctx context.Context, ticker runtimeTicker) {
	defer ticker.Stop()
	if ctx.Err() != nil {
		return
	}
	runtime.sweepRetention(ctx, runtime.now())
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			if ctx.Err() != nil {
				return
			}
			runtime.sweepRetention(ctx, runtime.now())
		}
	}
}

func (runtime *Runtime) sweepRetention(ctx context.Context, now time.Time) {
	if runtime.registry != nil {
		runtime.registry.ExpireModelCooldowns(now)
	}
	if runtime.requestLogCleaner != nil {
		runtime.requestLogCleaner.Sweep(ctx, now)
	}
	if runtime.stageCleaner != nil {
		if err := runtime.stageCleaner.CleanupCredentialStages(ctx, now); err != nil {
			logrus.WithError(err).WithField("event", "control.credential_stage_cleanup_failed").Warn("credential stage cleanup failed")
		}
	}
}
