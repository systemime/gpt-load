package requestlog

import (
	"context"
	"strings"
	"time"

	"gpt-load/internal/state"
)

// CredentialWindowUsage 聚合每个凭证在 5h 与周窗口内的
// 请求数与消耗 token(未缓存输入+输出), 供用量均衡(阶段 3)与负载端点使用。
// 5h 窗口按厂商锚点对齐: windowAnchors 由注册表维护(每日 07:00 空闲检查重置,
// 凭证收到首个请求时开窗), 窗口起点 = 锚点 + 5h*n; 无锚点退回滚动 now-5h。
// 口径与 /api/credentials/load 一致: 全部 request_logs 行(含失败),
// 失败请求 token 为零但计一次到达。
// 周窗口锚点取优先级: 凭证级 week_reset_anchor_ms > 全局 weekAnchorMS;
// 锚点 ≤ 0 时退化为滚动 7 天。凭证可因席位单独下单/换绑而拥有独立官方周期。
func (service *Service) CredentialWindowUsage(
	ctx context.Context, weekAnchorMS int64, windowAnchors map[uint]int64,
) (map[uint]state.CredentialUsage, error) {
	fiveHoursMS := service.now().Add(-5 * time.Hour).UnixMilli()
	nowMS := service.now().UnixMilli()
	defaultWeekStart := weekStartFor(nowMS, weekAnchorMS)

	// 凭证级锚点覆盖: 每个不同窗口单独聚合(组数 = 不同锚点数, 通常 0~2)。
	anchorRows := make([]struct {
		ID     uint  `gorm:"column:id"`
		Anchor int64 `gorm:"column:week_reset_anchor_ms"`
	}, 0, 8)
	if err := service.db.WithContext(ctx).
		Table("credentials").
		Select("id, week_reset_anchor_ms").
		Where("week_reset_anchor_ms IS NOT NULL AND week_reset_anchor_ms > 0").
		Scan(&anchorRows).Error; err != nil {
		return nil, err
	}
	overrideStarts := make(map[int64][]uint, 4)
	for _, row := range anchorRows {
		start := weekStartFor(nowMS, row.Anchor)
		overrideStarts[start] = append(overrideStarts[start], row.ID)
	}
	overridden := make([]uint, 0, len(anchorRows))
	for _, ids := range overrideStarts {
		overridden = append(overridden, ids...)
	}

	usage := make(map[uint]state.CredentialUsage, 16)

	// 默认窗口: 未被覆盖的凭证。
	defaultIDs := make([]uint, 0, 16)
	idRows := make([]struct {
		ID uint
	}, 0, 32)
	query := service.db.WithContext(ctx).Table("credentials").Select("id")
	// overridden 为空时 NOT IN () 会退化成 NOT IN (NULL) 而漏掉全部凭证。
	if len(overridden) > 0 {
		query = query.Where("id NOT IN ?", overridden)
	}
	if err := query.Scan(&idRows).Error; err != nil {
		return nil, err
	}
	for _, row := range idRows {
		defaultIDs = append(defaultIDs, row.ID)
	}
	if err := service.appendWindowUsage(ctx, usage, defaultIDs, fiveHoursMS, defaultWeekStart, windowAnchors); err != nil {
		return nil, err
	}
	// 各覆盖窗口。
	for start, ids := range overrideStarts {
		if err := service.appendWindowUsage(ctx, usage, ids, fiveHoursMS, start, windowAnchors); err != nil {
			return nil, err
		}
	}
	return usage, nil
}

// appendWindowUsage 聚合指定凭证集合的 5h 与周窗口请求数/token/积分, 合并进 usage。
// windowAnchors: 凭证 → 厂商 5h 窗口锚点(epoch ms); 无锚点凭证退回滚动 now-5h。
func (service *Service) appendWindowUsage(
	ctx context.Context,
	usage map[uint]state.CredentialUsage,
	credentialIDs []uint,
	fiveHoursMS int64,
	weekStartMS int64,
	windowAnchors map[uint]int64,
) error {
	if len(credentialIDs) == 0 {
		return nil
	}
	rows := make([]struct {
		CredentialID uint    `gorm:"column:credential_id"`
		Requests5h   int64   `gorm:"column:requests_5h"`
		Tokens5h     int64   `gorm:"column:tokens_5h"`
		Requests7d   int64   `gorm:"column:requests_7d"`
		Tokens7d     int64   `gorm:"column:tokens_7d"`
		Credits5h    float64 `gorm:"column:credits_5h"`
		Credits7d    float64 `gorm:"column:credits_7d"`
	}, 0, 16)
	// request_logs 与 usage_stats 两表窗口一致, UNION ALL 子查询统一聚合:
	// credits 仅来自 usage_stats, 请求数/token 来自 request_logs。
	// 5h 边界按凭证厂商锚点对齐: 窗口起点 = 锚点 + 5h*n(在 Go 侧算好),
	// 无锚点凭证退回滚动 now-5h。锚点列表很小(凭证数), CASE 展开即可。
	nowMS := service.now().UnixMilli()
	const fiveH = 5 * 3600 * 1000
	caseParts := make([]string, 0, len(credentialIDs))
	caseArgs := make([]any, 0, len(credentialIDs))
	for _, id := range credentialIDs {
		caseParts = append(caseParts, "WHEN ? THEN ?")
		caseArgs = append(caseArgs, id)
		if anchor, ok := windowAnchors[id]; ok && anchor > 0 {
			caseArgs = append(caseArgs, anchor+fiveH*((nowMS-anchor)/fiveH))
		} else {
			caseArgs = append(caseArgs, nowMS-fiveHoursMS)
		}
	}
	winStartExpr := "COALESCE(CASE w.credential_id " + strings.Join(caseParts, " ") + " END, ?)"
	rollingFallback := nowMS - fiveHoursMS
	exprArgs := append(append([]any{}, caseArgs...), rollingFallback)
	selectArgs := make([]any, 0, len(exprArgs)*3+3)
	for i := 0; i < 3; i++ {
		selectArgs = append(selectArgs, exprArgs...)
	}
	selectArgs = append(selectArgs, weekStartMS, weekStartMS, weekStartMS)
	subquery := "(SELECT credential_id, completed_at_ms, uncached_input_tokens, output_tokens, 0 AS cache_read_tokens, '' AS model, 1 AS source FROM request_logs UNION ALL SELECT credential_id, bucket_start_ms AS completed_at_ms, uncached_input_tokens, output_tokens, cache_read_tokens, model, 2 AS source FROM usage_stats) AS w"
	if err := service.db.WithContext(ctx).
		Table(subquery).
		Select(`credential_id,
SUM(CASE WHEN source = 1 AND completed_at_ms > `+winStartExpr+` THEN 1 ELSE 0 END) AS requests_5h,
SUM(CASE WHEN source = 1 AND completed_at_ms > `+winStartExpr+` THEN uncached_input_tokens + output_tokens ELSE 0 END) AS tokens_5h,
SUM(CASE WHEN source = 2 AND completed_at_ms > `+winStartExpr+` THEN `+glmCreditCoefficientSQL+` ELSE 0 END) AS credits_5h,
SUM(CASE WHEN source = 1 AND completed_at_ms > ? THEN 1 ELSE 0 END) AS requests_7d,
SUM(CASE WHEN source = 1 AND completed_at_ms > ? THEN uncached_input_tokens + output_tokens ELSE 0 END) AS tokens_7d,
SUM(CASE WHEN source = 2 AND completed_at_ms > ? THEN `+glmCreditCoefficientSQL+` ELSE 0 END) AS credits_7d`,
			selectArgs...).
		Where("credential_id IN ? AND completed_at_ms > ?", credentialIDs, weekStartMS).
		Group("credential_id").
		Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		usage[row.CredentialID] = state.CredentialUsage{
			Requests5h: row.Requests5h,
			Tokens5h:   row.Tokens5h,
			Requests7d: row.Requests7d,
			Tokens7d:   row.Tokens7d,
			Credits5h:  row.Credits5h,
			Credits7d:  row.Credits7d,
		}
	}
	return nil
}

// weekStartFor 返回给定锚点下最近的周期起点; 锚点 ≤ 0 时为滚动 7 天。
func weekStartFor(nowMS int64, anchorMS int64) int64 {
	if anchorMS <= 0 {
		return nowMS - 7*24*int64(time.Hour)/int64(time.Millisecond)
	}
	const periodMS = 7 * 24 * int64(time.Hour) / int64(time.Millisecond)
	start := anchorMS + ((nowMS-anchorMS)/periodMS)*periodMS
	if start > nowMS {
		start -= periodMS
	}
	return start
}

// GLM 编码套餐积分系数(每 10000 token 换算积分), 与官方文档同步:
// GLM-5.3: 输入 6.9 / 缓存命中 1.7 / 输出 24; GLM-5.3-Flash: 2.3 / 0.56 / 8。
// 未匹配模型不计积分。非高峰半价未实现: 与官方后台实测对照, 后台按 1 倍计
// (周末重度用量未减半), 故按无折扣口径保持一致。
const glmCreditCoefficientSQL = `(
	uncached_input_tokens * CASE WHEN model LIKE 'glm-5.3-flash%' THEN 2.3 WHEN model LIKE 'glm-5.3%' THEN 6.9 ELSE 0 END
	+ cache_read_tokens * CASE WHEN model LIKE 'glm-5.3-flash%' THEN 0.56 WHEN model LIKE 'glm-5.3%' THEN 1.7 ELSE 0 END
	+ output_tokens * CASE WHEN model LIKE 'glm-5.3-flash%' THEN 8.0 WHEN model LIKE 'glm-5.3%' THEN 24.0 ELSE 0 END
) / 10000.0`
