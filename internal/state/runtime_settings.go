package state

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net/textproto"
	"strings"
	"time"

	"gpt-load/internal/health"
	"gpt-load/internal/parameteroverride"
	"gpt-load/internal/platform/config"
	"gpt-load/internal/platform/httpheader"
)

const (
	SettingGlobalConcurrencyLimit           = "global_concurrency_limit"
	SettingDefaultAccessKeyConcurrencyLimit = "default_access_key_concurrency_limit"
	SettingDefaultGroupConcurrencyLimit     = "default_group_concurrency_limit"
	SettingConcurrencyLimit                 = "concurrency_limit"
	SettingFirstByteTimeout                 = "first_byte_timeout"
	SettingRequestTimeout                   = "request_timeout"
	SettingStreamIdleTimeout                = "stream_idle_timeout"
	SettingHeaderRules                      = "header_rules"
	SettingCORS                             = "cors"
	SettingResponseHeaderRules              = "response_header_rules"
	SettingRetryCount                       = "retry_count"
	SettingRouteStrategy                    = "route_strategy"
	SettingBlacklistThreshold               = "blacklist_threshold"
	SettingAffinityEnabled                  = "affinity_enabled"
	SettingCodexLiveMode                    = "codex_live_mode"
	SettingResponsesWebsocketEnabled        = "responses_websocket_enabled"
	SettingEmptyResponseRetry               = "empty_response_retry"
	SettingAffinityTTL                      = "affinity_ttl"
	SettingAffinityCapacity                 = "affinity_capacity"
	SettingCredentialConcurrencyLimit       = "credential_concurrency_limit"
	SettingCredentialQueueEnabled           = "credential_queue_enabled"
	SettingCredentialQueueTimeout           = "credential_queue_timeout"
	SettingCredentialUsageBalance           = "credential_usage_balance_enabled"
	SettingGlmCodingPlan                    = "glm_coding_plan"
	SettingGlmWeekResetAnchorMS             = "glm_week_reset_anchor_ms"
	SettingModelQuotaWeights                = "model_quota_weights"
	SettingPlanQuotaCredits                 = "plan_quota_credits"
	SettingValidationInterval               = "validation_interval"
	SettingRequestLogRetentionDays          = "request_log_retention_days"
	SettingModelsDevAutoSyncEnabled         = "models_dev_auto_sync_enabled"
	SettingParameterOverrides               = "parameter_overrides"
	SettingErrorRules                       = "error_rules"
	SettingForcedModelRewrite               = "forced_model_rewrite"
)

type CodexLiveMode string

const (
	CodexLiveOff    CodexLiveMode = "off"
	CodexLiveDirect CodexLiveMode = "direct"
	CodexLiveRelay  CodexLiveMode = "relay"
)

type RouteStrategy string

const (
	RouteStrategyNativeFirst RouteStrategy = "native_first"
	RouteStrategyWeightedMix RouteStrategy = "weighted_mix"
)

// GlmCodingPlan 标识 GLM 编码套餐档位, 决定团队积分额度(5h/周)。
// 额度数值与官方「团队标准版/高级版」一致, 仅用于水位展示与预警。
type GlmCodingPlan string

const (
	GlmPlanStandard GlmCodingPlan = "standard"
	GlmPlanPremium  GlmCodingPlan = "premium"
)

// GlmPlanQuota 返回套餐的积分额度: 5 小时窗口与每周窗口(官方文档口径)。
func (plan GlmCodingPlan) Quota() (credits5h float64, creditsWeek float64) {
	switch plan {
	case GlmPlanPremium:
		return 35_000, 155_000
	default:
		return 15_000, 66_000
	}
}

// PlanQuotaCredits 是一套套餐档位的额度数值(水位展示与预警用)。
type PlanQuotaCredits struct {
	Credits5h float64 `json:"credits_5h"`
	Credits7d float64 `json:"credits_7d"`
}

// ForcedModelRewrite 分组级强制改写: 命中 Mappings 键的客户端模型请求一律
// 改写到目标模型(路由、报文、冷却、用量全部跟随目标)。分组专属设置,
// 不出现在全局设置键里; from/to 都必须是分组已配置的模型。
type ForcedModelRewrite struct {
	Enabled  bool              `json:"enabled"`
	Mappings map[string]string `json:"mappings"`
}

const maxForcedRewriteEntries = 32

// ParseForcedModelRewrite 导出给设置保存路径复用(分组模型归属校验在快照编译)。
func ParseForcedModelRewrite(key string, value any) (ForcedModelRewrite, error) {
	object, err := settingJSONObject(key, value)
	if err != nil {
		return ForcedModelRewrite{}, err
	}
	rewrite := ForcedModelRewrite{Mappings: map[string]string{}}
	if enabled, ok := object["enabled"]; ok {
		parsed, err := strictBoolean(key+".enabled", enabled)
		if err != nil {
			return ForcedModelRewrite{}, err
		}
		rewrite.Enabled = parsed
	}
	rawMappings, ok := object["mappings"]
	if !ok || rawMappings == nil {
		return rewrite, nil
	}
	mappings, err := settingJSONObject(key+".mappings", rawMappings)
	if err != nil {
		return ForcedModelRewrite{}, err
	}
	if len(mappings) > maxForcedRewriteEntries {
		return ForcedModelRewrite{}, fmt.Errorf("%s.mappings: at most %d entries", key, maxForcedRewriteEntries)
	}
	for from, toRaw := range mappings {
		source := strings.TrimSpace(from)
		target, err := settingNonEmptyString(key+".mappings", toRaw)
		if err != nil {
			return ForcedModelRewrite{}, err
		}
		if source == "" || len(source) > 128 || len(target) > 128 {
			return ForcedModelRewrite{}, fmt.Errorf("%s.mappings: invalid model name %q", key, from)
		}
		if source == target {
			return ForcedModelRewrite{}, fmt.Errorf("%s.mappings: %q must differ from its target", key, source)
		}
		rewrite.Mappings[source] = target
	}
	return rewrite, nil
}

// EffectiveForcedMappings 返回生效改写表(未启用或空表返回 nil); 无效条目
// (模型已从分组移除)由快照编译阶段过滤。
func (rewrite ForcedModelRewrite) EffectiveForcedMappings() map[string]string {
	if !rewrite.Enabled || len(rewrite.Mappings) == 0 {
		return nil
	}
	merged := make(map[string]string, len(rewrite.Mappings))
	for from, to := range rewrite.Mappings {
		merged[from] = to
	}
	return merged
}

func settingNonEmptyString(path string, value any) (string, error) {
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", path)
	}
	if strings.TrimSpace(text) == "" || len(text) > 128 {
		return "", fmt.Errorf("%s must be nonblank text up to 128 bytes", path)
	}
	return text, nil
}

// parseModelQuotaWeights 解析并校验 model_quota_weights 设置:
// JSON 对象 {模型名: 正倍率}, 供槽位占用与配额折算共用。
func parseModelQuotaWeights(key string, value any) (map[string]float64, error) {
	object, err := settingJSONObject(key, value)
	if err != nil {
		return nil, err
	}
	if len(object) > maxModelQuotaWeightEntries {
		return nil, fmt.Errorf("%s: at most %d entries", key, maxModelQuotaWeightEntries)
	}
	weights := make(map[string]float64, len(object))
	for modelName, raw := range object {
		name := strings.TrimSpace(modelName)
		if name == "" || len(name) > 128 {
			return nil, fmt.Errorf("%s: invalid model name %q", key, modelName)
		}
		weight, err := settingPositiveNumber(key, raw)
		if err != nil {
			return nil, err
		}
		if weight < minModelQuotaWeight || weight > maxModelQuotaWeight {
			return nil, fmt.Errorf("%s: weight for %q must be between %v and %v", key, name, minModelQuotaWeight, maxModelQuotaWeight)
		}
		if weight*2 != math.Trunc(weight*2) {
			return nil, fmt.Errorf("%s: weight for %q must be a multiple of 0.5", key, name)
		}
		weights[name] = weight
	}
	return weights, nil
}

// parsePlanQuotaCredits 解析并校验 plan_quota_credits 设置:
// JSON 对象 {套餐名: {credits_5h, credits_7d}}, 数值非负。
func parsePlanQuotaCredits(key string, value any) (map[string]PlanQuotaCredits, error) {
	object, err := settingJSONObject(key, value)
	if err != nil {
		return nil, err
	}
	if len(object) > maxPlanQuotaEntries {
		return nil, fmt.Errorf("%s: at most %d plans", key, maxPlanQuotaEntries)
	}
	credits := make(map[string]PlanQuotaCredits, len(object))
	for planName, raw := range object {
		name := strings.TrimSpace(planName)
		if name == "" || len(name) > 64 {
			return nil, fmt.Errorf("%s: invalid plan name %q", key, planName)
		}
		fields, err := settingJSONObject(key, raw)
		if err != nil {
			return nil, err
		}
		credits5h, err := settingNonNegativeNumber(key, fields["credits_5h"])
		if err != nil {
			return nil, err
		}
		credits7d, err := settingNonNegativeNumber(key, fields["credits_7d"])
		if err != nil {
			return nil, err
		}
		credits[name] = PlanQuotaCredits{Credits5h: credits5h, Credits7d: credits7d}
	}
	return credits, nil
}

const (
	defaultRequestLogRetentionDays = 7
	minRequestLogRetentionDays     = 1
	maxRequestLogRetentionDays     = 365
	defaultAffinityCapacity        = 10_000
	maxAffinityCapacity            = 1_000_000
	// 每凭证并发上限默认值 = 实测厂商硬顶(≈8, code 1302)减 2 余量; 0 表示不限制。
	defaultCredentialConcurrencyLimit = 6
	MaxCredentialConcurrencyLimit     = 64
	// CredentialQueueSlack 是组预算浮动: 全池达 cap 后每凭证最多再放行这么多, 超出进入排队。
	CredentialQueueSlack = 2
	// SlotUnitScale 是槽位记账的最小单位: 2 表示半槽步进, 模型倍率 1.5 占 3 单位。
	SlotUnitScale                 = 2
	minCredentialQueueTimeout     = 1
	maxCredentialQueueTimeout     = 600
	defaultCredentialQueueTimeout = 30
	maxJSONSafeInteger            = int64(1<<53 - 1)
	maxJSONSafeFloat              = float64(1<<53 - 1)
	maxModelQuotaWeightEntries    = 128
	minModelQuotaWeight           = 0.5
	maxModelQuotaWeight           = 3
	maxPlanQuotaEntries           = 64
)

type RuntimeSettings struct {
	ErrorRules                       health.ErrorRules
	GlobalConcurrencyLimit           int64
	DefaultAccessKeyConcurrencyLimit int64
	DefaultGroupConcurrencyLimit     int64
	FirstByteTimeout                 time.Duration
	RequestTimeout                   time.Duration
	StreamIdleTimeout                time.Duration
	HeaderRules                      HeaderRules
	CORS                             CORSConfig
	ResponseHeaderRules              HeaderRules
	RetryCount                       int
	RouteStrategy                    RouteStrategy
	BlacklistThreshold               int
	AffinityEnabled                  bool
	CodexLiveMode                    CodexLiveMode
	ResponsesWebsocketEnabled        bool
	EmptyResponseRetry               bool
	AffinityTTL                      time.Duration
	AffinityCapacity                 int
	CredentialConcurrencyLimit       int
	CredentialQueueEnabled           bool
	CredentialQueueTimeout           time.Duration
	CredentialUsageBalance           bool
	GlmCodingPlan                    GlmCodingPlan
	// GlmWeekResetAnchorMS 是官方周周期重置锚点(epoch 毫秒, UTC+8 时区语义由使用者保证)。
	// 周窗口 = 最近一次锚点 + 7 天整数倍; 0 表示退化为滚动 7 天窗口。
	GlmWeekResetAnchorMS int64
	// ModelQuotaWeights 是模型倍率覆盖(仅设置项, 内置默认见 ModelQuotaWeights);
	// nil 表示无覆盖。快照编译时与内置表合并为生效表。
	ModelQuotaWeights map[string]float64
	// PlanQuotaCredits 按套餐名覆盖积分额度(水位展示); nil 表示用官方内置数值。
	PlanQuotaCredits         map[string]PlanQuotaCredits
	ValidationInterval       time.Duration
	RequestLogRetentionDays  int
	ModelsDevAutoSyncEnabled bool
}

type ResolvedGroupSettings struct {
	ErrorRules                health.ErrorRules
	ConcurrencyLimit          int64
	Timeouts                  TimeoutConfig
	HeaderRules               HeaderRules
	BlacklistThreshold        int
	AffinityEnabled           bool
	CodexLiveMode             CodexLiveMode
	ResponsesWebsocketEnabled bool
	EmptyResponseRetry        bool
	ParameterOverrides        parameteroverride.Rules
	ForcedModelRewrite        ForcedModelRewrite
}

func DefaultRuntimeSettings() RuntimeSettings {
	return RuntimeSettings{
		FirstByteTimeout:           120 * time.Second,
		RequestTimeout:             600 * time.Second,
		StreamIdleTimeout:          300 * time.Second,
		HeaderRules:                HeaderRules{Set: map[string]string{}},
		CORS:                       defaultCORSConfig(),
		ResponseHeaderRules:        HeaderRules{Set: map[string]string{}},
		RetryCount:                 2,
		RouteStrategy:              RouteStrategyNativeFirst,
		BlacklistThreshold:         3,
		AffinityEnabled:            true,
		CodexLiveMode:              CodexLiveDirect,
		ResponsesWebsocketEnabled:  true,
		EmptyResponseRetry:         false,
		AffinityTTL:                time.Hour,
		AffinityCapacity:           defaultAffinityCapacity,
		CredentialConcurrencyLimit: defaultCredentialConcurrencyLimit,
		CredentialQueueEnabled:     true,
		CredentialQueueTimeout:     time.Duration(defaultCredentialQueueTimeout) * time.Second,
		CredentialUsageBalance:     false,
		GlmCodingPlan:              GlmPlanStandard,
		GlmWeekResetAnchorMS:       0,
		ValidationInterval:         10 * time.Minute,
		RequestLogRetentionDays:    defaultRequestLogRetentionDays,
		ModelsDevAutoSyncEnabled:   true,
	}
}

func IsRuntimeSettingKey(key string) bool {
	switch key {
	case SettingGlobalConcurrencyLimit, SettingDefaultAccessKeyConcurrencyLimit, SettingDefaultGroupConcurrencyLimit,
		SettingFirstByteTimeout,
		SettingRequestTimeout,
		SettingStreamIdleTimeout,
		SettingHeaderRules,
		SettingCORS,
		SettingResponseHeaderRules,
		SettingRetryCount,
		SettingRouteStrategy,
		SettingBlacklistThreshold,
		SettingAffinityEnabled,
		SettingCodexLiveMode,
		SettingResponsesWebsocketEnabled,
		SettingEmptyResponseRetry,
		SettingAffinityTTL,
		SettingAffinityCapacity,
		SettingCredentialConcurrencyLimit,
		SettingCredentialQueueEnabled,
		SettingCredentialQueueTimeout,
		SettingCredentialUsageBalance,
		SettingGlmCodingPlan,
		SettingGlmWeekResetAnchorMS,
		SettingModelQuotaWeights,
		SettingPlanQuotaCredits,
		SettingValidationInterval,
		SettingRequestLogRetentionDays,
		SettingModelsDevAutoSyncEnabled:
		return true
	case SettingErrorRules:
		return true
	default:
		return false
	}
}

func ResolveRuntimeSettings(settings config.Settings) (RuntimeSettings, error) {
	resolved := DefaultRuntimeSettings()
	for key, value := range settings {
		switch key {
		case SettingGlobalConcurrencyLimit, SettingDefaultAccessKeyConcurrencyLimit, SettingDefaultGroupConcurrencyLimit:
			limit, err := nonNegativeWholeNumber(key, value)
			if err != nil {
				return RuntimeSettings{}, err
			}
			switch key {
			case SettingGlobalConcurrencyLimit:
				resolved.GlobalConcurrencyLimit = int64(limit)
			case SettingDefaultAccessKeyConcurrencyLimit:
				resolved.DefaultAccessKeyConcurrencyLimit = int64(limit)
			case SettingDefaultGroupConcurrencyLimit:
				resolved.DefaultGroupConcurrencyLimit = int64(limit)
			}
		case SettingFirstByteTimeout:
			seconds, err := positiveWholeSeconds(key, value)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.FirstByteTimeout = time.Duration(seconds) * time.Second
		case SettingRequestTimeout:
			seconds, err := positiveWholeSeconds(key, value)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.RequestTimeout = time.Duration(seconds) * time.Second
		case SettingStreamIdleTimeout:
			seconds, err := positiveWholeSeconds(key, value)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.StreamIdleTimeout = time.Duration(seconds) * time.Second
		case SettingHeaderRules:
			rules, err := parseHeaderRules(value)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.HeaderRules = rules
		case SettingErrorRules:
			rules, err := health.CompileErrorRules(value, "global")
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.ErrorRules = rules
		case SettingCORS:
			cors, err := parseCORSConfig(value)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.CORS = cors
		case SettingResponseHeaderRules:
			rules, err := parseResponseHeaderRules(value)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.ResponseHeaderRules = rules
		case SettingRetryCount:
			count, err := nonNegativeWholeNumber(key, value)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.RetryCount = count
		case SettingRouteStrategy:
			strategy, err := parseRouteStrategy(value)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.RouteStrategy = strategy
		case SettingBlacklistThreshold:
			threshold, err := nonNegativeWholeNumber(key, value)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.BlacklistThreshold = threshold
		case SettingAffinityEnabled:
			value, err := strictBoolean(key, value)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.AffinityEnabled = value
		case SettingCodexLiveMode:
			parsed, err := parseCodexLiveMode(value)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.CodexLiveMode = parsed
		case SettingResponsesWebsocketEnabled:
			value, err := strictBoolean(key, value)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.ResponsesWebsocketEnabled = value
		case SettingEmptyResponseRetry:
			value, err := strictBoolean(key, value)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.EmptyResponseRetry = value
		case SettingAffinityTTL:
			seconds, err := positiveWholeSeconds(key, value)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.AffinityTTL = time.Duration(seconds) * time.Second
		case SettingAffinityCapacity:
			capacity, err := wholeNumberInRange(key, value, 1, maxAffinityCapacity)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.AffinityCapacity = capacity
		case SettingCredentialConcurrencyLimit:
			limit, err := wholeNumberInRange(key, value, 0, MaxCredentialConcurrencyLimit)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.CredentialConcurrencyLimit = limit
		case SettingCredentialQueueEnabled:
			value, err := strictBoolean(key, value)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.CredentialQueueEnabled = value
		case SettingCredentialQueueTimeout:
			seconds, err := wholeNumberInRange(key, value, minCredentialQueueTimeout, maxCredentialQueueTimeout)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.CredentialQueueTimeout = time.Duration(seconds) * time.Second
		case SettingCredentialUsageBalance:
			value, err := strictBoolean(key, value)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.CredentialUsageBalance = value
		case SettingGlmCodingPlan:
			parsed, err := parseGlmCodingPlan(value)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.GlmCodingPlan = parsed
		case SettingGlmWeekResetAnchorMS:
			anchor, err := nonNegativeWholeNumber(key, value)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.GlmWeekResetAnchorMS = int64(anchor)
		case SettingModelQuotaWeights:
			weights, err := parseModelQuotaWeights(key, value)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.ModelQuotaWeights = weights
		case SettingPlanQuotaCredits:
			credits, err := parsePlanQuotaCredits(key, value)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.PlanQuotaCredits = credits
		case SettingValidationInterval:
			seconds, err := positiveWholeSeconds(key, value)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.ValidationInterval = time.Duration(seconds) * time.Second
		case SettingRequestLogRetentionDays:
			days, err := wholeNumberInRange(
				key,
				value,
				minRequestLogRetentionDays,
				maxRequestLogRetentionDays,
			)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.RequestLogRetentionDays = days
		case SettingModelsDevAutoSyncEnabled:
			value, err := strictBoolean(key, value)
			if err != nil {
				return RuntimeSettings{}, err
			}
			resolved.ModelsDevAutoSyncEnabled = value
		default:
			return RuntimeSettings{}, fmt.Errorf("unknown runtime setting %q", key)
		}
	}
	return resolved, nil
}

func ResolveGroupRuntimeSettings(
	base RuntimeSettings,
	settings config.Settings,
) (ResolvedGroupSettings, error) {
	resolved := ResolvedGroupSettings{
		ErrorRules:       base.ErrorRules,
		ConcurrencyLimit: base.DefaultGroupConcurrencyLimit,
		Timeouts: TimeoutConfig{
			FirstByte:  base.FirstByteTimeout,
			Request:    base.RequestTimeout,
			StreamIdle: base.StreamIdleTimeout,
		},
		HeaderRules:               cloneHeaderRules(base.HeaderRules),
		BlacklistThreshold:        base.BlacklistThreshold,
		AffinityEnabled:           base.AffinityEnabled,
		CodexLiveMode:             base.CodexLiveMode,
		ResponsesWebsocketEnabled: base.ResponsesWebsocketEnabled,
		EmptyResponseRetry:        base.EmptyResponseRetry,
	}
	for key, value := range settings {
		switch key {
		case SettingConcurrencyLimit:
			limit, err := nonNegativeWholeNumber(key, value)
			if err != nil {
				return ResolvedGroupSettings{}, err
			}
			resolved.ConcurrencyLimit = int64(limit)
		case SettingFirstByteTimeout:
			seconds, err := positiveWholeSeconds(key, value)
			if err != nil {
				return ResolvedGroupSettings{}, err
			}
			resolved.Timeouts.FirstByte = time.Duration(seconds) * time.Second
		case SettingRequestTimeout:
			seconds, err := positiveWholeSeconds(key, value)
			if err != nil {
				return ResolvedGroupSettings{}, err
			}
			resolved.Timeouts.Request = time.Duration(seconds) * time.Second
		case SettingStreamIdleTimeout:
			seconds, err := positiveWholeSeconds(key, value)
			if err != nil {
				return ResolvedGroupSettings{}, err
			}
			resolved.Timeouts.StreamIdle = time.Duration(seconds) * time.Second
		case SettingHeaderRules:
			parsed, err := parseHeaderRules(value)
			if err != nil {
				return ResolvedGroupSettings{}, err
			}
			resolved.HeaderRules = parsed
		case SettingRetryCount:
			// 兼容读取历史分组配置；重试预算仅由系统设置决定。
			continue
		case SettingBlacklistThreshold:
			parsed, err := nonNegativeWholeNumber(key, value)
			if err != nil {
				return ResolvedGroupSettings{}, err
			}
			resolved.BlacklistThreshold = parsed
		case SettingAffinityEnabled:
			parsed, err := strictBoolean(key, value)
			if err != nil {
				return ResolvedGroupSettings{}, err
			}
			resolved.AffinityEnabled = parsed
		case SettingCodexLiveMode:
			parsed, err := parseCodexLiveMode(value)
			if err != nil {
				return ResolvedGroupSettings{}, err
			}
			resolved.CodexLiveMode = parsed
		case SettingResponsesWebsocketEnabled:
			parsed, err := strictBoolean(key, value)
			if err != nil {
				return ResolvedGroupSettings{}, err
			}
			resolved.ResponsesWebsocketEnabled = parsed
		case SettingEmptyResponseRetry:
			parsed, err := strictBoolean(key, value)
			if err != nil {
				return ResolvedGroupSettings{}, err
			}
			resolved.EmptyResponseRetry = parsed
		case SettingParameterOverrides:
			parsed, err := parameteroverride.Compile(value)
			if err != nil {
				return ResolvedGroupSettings{}, err
			}
			resolved.ParameterOverrides = parsed
		case SettingErrorRules:
			rules, err := health.CompileErrorRules(value, "group")
			if err != nil {
				return ResolvedGroupSettings{}, err
			}
			resolved.ErrorRules = rules
		case SettingForcedModelRewrite:
			rewrite, err := ParseForcedModelRewrite(key, value)
			if err != nil {
				return ResolvedGroupSettings{}, err
			}
			resolved.ForcedModelRewrite = rewrite
		default:
			return ResolvedGroupSettings{}, fmt.Errorf("unknown group setting %q", key)
		}
	}
	return resolved, nil
}

func ValidateRuntimeSetting(key string, value any) error {
	switch key {
	case SettingErrorRules:
		_, err := health.CompileErrorRules(value, "global")
		return err
	case SettingFirstByteTimeout,
		SettingRequestTimeout,
		SettingStreamIdleTimeout,
		SettingValidationInterval:
		_, err := positiveWholeSeconds(key, value)
		return err
	case SettingHeaderRules:
		_, err := parseHeaderRules(value)
		return err
	case SettingCORS:
		_, err := parseCORSConfig(value)
		return err
	case SettingResponseHeaderRules:
		_, err := parseResponseHeaderRules(value)
		return err
	case SettingGlobalConcurrencyLimit, SettingDefaultAccessKeyConcurrencyLimit, SettingDefaultGroupConcurrencyLimit, SettingRetryCount, SettingBlacklistThreshold:
		_, err := nonNegativeWholeNumber(key, value)
		return err
	case SettingCodexLiveMode:
		_, err := parseCodexLiveMode(value)
		return err
	case SettingRouteStrategy:
		_, err := parseRouteStrategy(value)
		return err
	case SettingAffinityEnabled, SettingResponsesWebsocketEnabled, SettingEmptyResponseRetry:
		_, err := strictBoolean(key, value)
		return err
	case SettingAffinityTTL:
		_, err := positiveWholeSeconds(key, value)
		return err
	case SettingAffinityCapacity:
		_, err := wholeNumberInRange(key, value, 1, maxAffinityCapacity)
		return err
	case SettingCredentialConcurrencyLimit:
		_, err := wholeNumberInRange(key, value, 0, MaxCredentialConcurrencyLimit)
		return err
	case SettingCredentialQueueEnabled:
		_, err := strictBoolean(key, value)
		return err
	case SettingCredentialQueueTimeout:
		_, err := wholeNumberInRange(key, value, minCredentialQueueTimeout, maxCredentialQueueTimeout)
		return err
	case SettingCredentialUsageBalance:
		_, err := strictBoolean(key, value)
		return err
	case SettingGlmCodingPlan:
		_, err := parseGlmCodingPlan(value)
		return err
	case SettingGlmWeekResetAnchorMS:
		_, err := nonNegativeWholeNumber(key, value)
		return err
	case SettingModelQuotaWeights:
		_, err := parseModelQuotaWeights(key, value)
		return err
	case SettingPlanQuotaCredits:
		_, err := parsePlanQuotaCredits(key, value)
		return err
	case SettingRequestLogRetentionDays:
		_, err := wholeNumberInRange(
			key,
			value,
			minRequestLogRetentionDays,
			maxRequestLogRetentionDays,
		)
		return err
	case SettingModelsDevAutoSyncEnabled:
		_, err := strictBoolean(key, value)
		return err
	default:
		return fmt.Errorf("unknown runtime setting %q", key)
	}
}

func parseRouteStrategy(value any) (RouteStrategy, error) {
	text, ok := value.(string)
	if ok {
		switch strategy := RouteStrategy(text); strategy {
		case RouteStrategyNativeFirst, RouteStrategyWeightedMix:
			return strategy, nil
		}
	}
	return "", fmt.Errorf("%s must be native_first or weighted_mix", SettingRouteStrategy)
}

func strictBoolean(path string, value any) (bool, error) {
	parsed, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%s must be a boolean", path)
	}
	return parsed, nil
}

// settingJSONObject 把设置值统一为 JSON 对象; 接受 map[string]any(设置装载后)
// 与 JSON 字符串(兼容 API 直传)。
func settingJSONObject(path string, value any) (map[string]any, error) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, nil
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return map[string]any{}, nil
		}
		var object map[string]any
		if err := json.Unmarshal([]byte(trimmed), &object); err != nil {
			return nil, fmt.Errorf("%s must be a JSON object", path)
		}
		return object, nil
	default:
		return nil, fmt.Errorf("%s must be a JSON object", path)
	}
}

// settingPositiveNumber 校验正数(支持小数), 供模型倍率使用。
func settingPositiveNumber(path string, value any) (float64, error) {
	weight, err := settingNonNegativeNumber(path, value)
	if err != nil {
		return 0, err
	}
	if weight <= 0 {
		return 0, fmt.Errorf("%s must be a positive number", path)
	}
	return weight, nil
}

// settingNonNegativeNumber 校验非负数(允许小数, 上限 JSON 安全整数)。
func settingNonNegativeNumber(path string, value any) (float64, error) {
	limit, err := nonNegativeWholeNumber(path, value)
	if err == nil {
		return float64(limit), nil
	}
	switch typed := value.(type) {
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed < 0 || typed > maxJSONSafeFloat {
			return 0, fmt.Errorf("%s must be a non-negative number", path)
		}
		return typed, nil
	case json.Number:
		parsed, ok := new(big.Rat).SetString(typed.String())
		if !ok || parsed.Sign() < 0 || !parsed.IsInt() && parsed.Denom().Cmp(big.NewInt(1_000_000)) > 0 {
			return 0, fmt.Errorf("%s must be a non-negative number", path)
		}
		result, _ := parsed.Float64()
		if result < 0 || result > maxJSONSafeFloat {
			return 0, fmt.Errorf("%s must be a non-negative number", path)
		}
		return result, nil
	default:
		return 0, fmt.Errorf("%s must be a non-negative number", path)
	}
}

func nonNegativeWholeNumber(path string, value any) (int, error) {
	var number *big.Int
	switch typed := value.(type) {
	case int:
		number = big.NewInt(int64(typed))
	case int64:
		number = big.NewInt(typed)
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0, fmt.Errorf("%s must be a non-negative whole number", path)
		}
		parsed := new(big.Rat).SetFloat64(typed)
		if parsed == nil || !parsed.IsInt() {
			return 0, fmt.Errorf("%s must be a non-negative whole number", path)
		}
		number = parsed.Num()
	case json.Number:
		literal := typed.String()
		parsed, ok := new(big.Rat).SetString(literal)
		if !json.Valid([]byte(literal)) || !ok || !parsed.IsInt() {
			return 0, fmt.Errorf("%s must be a non-negative whole number", path)
		}
		number = parsed.Num()
	default:
		return 0, fmt.Errorf("%s must be a non-negative whole number", path)
	}
	// Settings are exposed as JSON numbers and consumed by the management UI.
	// Keep the technical boundary lossless across Go and JavaScript; there is
	// no smaller product-level limit.
	maximum := big.NewInt(maxJSONSafeInteger)
	if number.Sign() < 0 || number.Cmp(maximum) > 0 {
		return 0, fmt.Errorf("%s must be a non-negative whole number within JSON safe integer range", path)
	}
	return int(number.Int64()), nil
}

func wholeNumberInRange(path string, value any, minimum, maximum int) (int, error) {
	var number int64
	switch typed := value.(type) {
	case int:
		number = int64(typed)
	case int64:
		number = typed
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed ||
			typed < float64(minimum) || typed > float64(maximum) {
			return 0, fmt.Errorf("%s must be a whole number between %d and %d", path, minimum, maximum)
		}
		number = int64(typed)
	case json.Number:
		literal := typed.String()
		parsed, ok := new(big.Rat).SetString(literal)
		if !json.Valid([]byte(literal)) || !ok || !parsed.IsInt() || !parsed.Num().IsInt64() {
			return 0, fmt.Errorf("%s must be a whole number between %d and %d", path, minimum, maximum)
		}
		number = parsed.Num().Int64()
	default:
		return 0, fmt.Errorf("%s must be a whole number between %d and %d", path, minimum, maximum)
	}
	if number < int64(minimum) || number > int64(maximum) {
		return 0, fmt.Errorf("%s must be a whole number between %d and %d", path, minimum, maximum)
	}
	return int(number), nil
}

func positiveWholeSeconds(path string, value any) (int64, error) {
	const maxTimeoutSeconds = int64((1<<63)-1) / int64(time.Second)

	var seconds int64
	switch typed := value.(type) {
	case int:
		seconds = int64(typed)
	case int64:
		seconds = typed
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed || typed > float64(maxTimeoutSeconds) {
			return 0, fmt.Errorf("%s must be a positive whole number", path)
		}
		seconds = int64(typed)
	case json.Number:
		literal := typed.String()
		parsed, ok := new(big.Rat).SetString(literal)
		if !json.Valid([]byte(literal)) || !ok || !parsed.IsInt() || !parsed.Num().IsInt64() {
			return 0, fmt.Errorf("%s must be a positive whole number", path)
		}
		seconds = parsed.Num().Int64()
	default:
		return 0, fmt.Errorf("%s must be a positive whole number", path)
	}
	if seconds <= 0 || seconds > maxTimeoutSeconds {
		return 0, fmt.Errorf("%s must be a positive whole number within duration range", path)
	}
	return seconds, nil
}

func parseHeaderRules(value any) (HeaderRules, error) {
	return parseHeaderRulesWithPolicy(
		value,
		SettingHeaderRules,
		func(name string) bool {
			return httpheader.IsForbiddenRequestRuleName(name) ||
				httpheader.IsCredentialName(name)
		},
	)
}

func parseResponseHeaderRules(value any) (HeaderRules, error) {
	return parseHeaderRulesWithPolicy(
		value,
		SettingResponseHeaderRules,
		httpheader.IsForbiddenResponseRuleName,
	)
}

func parseHeaderRulesWithPolicy(
	value any,
	settingName string,
	forbidden func(string) bool,
) (HeaderRules, error) {
	rules := HeaderRules{Set: make(map[string]string)}
	object, ok := value.(map[string]any)
	if !ok {
		return HeaderRules{}, fmt.Errorf("%s must be an object", settingName)
	}
	for key := range object {
		if key != "set" && key != "remove" {
			return HeaderRules{}, fmt.Errorf("unknown %s field %q", settingName, key)
		}
	}
	seen := make(map[string]struct{})
	if rawSet, exists := object["set"]; exists {
		set, ok := rawSet.(map[string]any)
		if !ok {
			return HeaderRules{}, fmt.Errorf("%s.set must be an object", settingName)
		}
		for name, rawValue := range set {
			if !validHTTPHeaderName(name) {
				return HeaderRules{}, fmt.Errorf("%s.set contains invalid header name %q", settingName, name)
			}
			canonicalName := textproto.CanonicalMIMEHeaderKey(name)
			if forbidden != nil && forbidden(canonicalName) {
				return HeaderRules{}, fmt.Errorf(
					"%s.set cannot set forbidden header %q",
					settingName,
					canonicalName,
				)
			}
			identity := strings.ToLower(name)
			if _, duplicate := seen[identity]; duplicate {
				return HeaderRules{}, fmt.Errorf(
					"%s.set contains duplicate header %q",
					settingName,
					canonicalName,
				)
			}
			seen[identity] = struct{}{}
			text, ok := rawValue.(string)
			if !ok {
				return HeaderRules{}, fmt.Errorf("%s.set.%s must be a string", settingName, name)
			}
			if !validHTTPHeaderValue(text) {
				return HeaderRules{}, fmt.Errorf("%s.set.%s contains invalid header value", settingName, name)
			}
			rules.Set[canonicalName] = text
		}
	}
	if rawRemove, exists := object["remove"]; exists {
		remove, ok := rawRemove.([]any)
		if !ok {
			return HeaderRules{}, fmt.Errorf("%s.remove must be an array", settingName)
		}
		rules.Remove = make([]string, 0, len(remove))
		for index, rawName := range remove {
			name, ok := rawName.(string)
			if !ok {
				return HeaderRules{}, fmt.Errorf("%s.remove[%d] must be a string", settingName, index)
			}
			if !validHTTPHeaderName(name) {
				return HeaderRules{}, fmt.Errorf(
					"%s.remove[%d] contains invalid header name %q",
					settingName,
					index,
					name,
				)
			}
			canonicalName := textproto.CanonicalMIMEHeaderKey(name)
			if forbidden != nil && forbidden(canonicalName) {
				return HeaderRules{}, fmt.Errorf(
					"%s.remove cannot remove forbidden header %q",
					settingName,
					canonicalName,
				)
			}
			identity := strings.ToLower(name)
			if _, duplicate := seen[identity]; duplicate {
				return HeaderRules{}, fmt.Errorf(
					"%s.remove contains duplicate header %q",
					settingName,
					canonicalName,
				)
			}
			seen[identity] = struct{}{}
			rules.Remove = append(rules.Remove, canonicalName)
		}
	}
	return rules, nil
}

func cloneHeaderRules(source HeaderRules) HeaderRules {
	cloned := HeaderRules{Set: make(map[string]string, len(source.Set))}
	for name, value := range source.Set {
		cloned.Set[name] = value
	}
	cloned.Remove = append([]string(nil), source.Remove...)
	return cloned
}

func validHTTPHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for index := range len(name) {
		if !isHTTPTokenByte(name[index]) {
			return false
		}
	}
	return true
}

func isHTTPTokenByte(value byte) bool {
	switch {
	case value >= '0' && value <= '9':
		return true
	case value >= 'a' && value <= 'z':
		return true
	case value >= 'A' && value <= 'Z':
		return true
	default:
		return strings.IndexByte("!#$%&'*+-.^_`|~", value) >= 0
	}
}

func validHTTPHeaderValue(value string) bool {
	for index := range len(value) {
		character := value[index]
		if (character < ' ' && character != '\t') || character == 0x7f {
			return false
		}
	}
	return true
}

func parseCodexLiveMode(value any) (CodexLiveMode, error) {
	if text, ok := value.(string); ok {
		switch mode := CodexLiveMode(text); mode {
		case CodexLiveOff, CodexLiveDirect, CodexLiveRelay:
			return mode, nil
		}
	}
	return "", fmt.Errorf("%s must be off, direct or relay", SettingCodexLiveMode)
}

func parseGlmCodingPlan(value any) (GlmCodingPlan, error) {
	if text, ok := value.(string); ok {
		switch plan := GlmCodingPlan(text); plan {
		case GlmPlanStandard, GlmPlanPremium:
			return plan, nil
		}
	}
	return "", fmt.Errorf("%s must be standard or premium", SettingGlmCodingPlan)
}
