package requestlog

import (
	"context"
	"time"

	"gpt-load/internal/state"
)

// CredentialWindowUsage 聚合每个凭证在滚动 5h 与 7d 窗口内的
// 请求数与消耗 token(未缓存输入+输出), 供用量均衡(阶段 3)与负载端点使用。
// 口径与 /api/credentials/load 一致: 全部 request_logs 行(含失败),
// 失败请求 token 为零但计一次到达。
func (service *Service) CredentialWindowUsage(ctx context.Context) (map[uint]state.CredentialUsage, error) {
	fiveHours := service.now().Add(-5 * time.Hour)
	sevenDays := service.now().Add(-7 * 24 * time.Hour)

	rows := make([]struct {
		CredentialID uint
		Requests5h   int64 `gorm:"column:requests_5h"`
		Tokens5h     int64 `gorm:"column:tokens_5h"`
		Requests7d   int64 `gorm:"column:requests_7d"`
		Tokens7d     int64 `gorm:"column:tokens_7d"`
	}, 0, 16)
	if err := service.db.WithContext(ctx).
		Table("request_logs").
		Select(`credential_id,
SUM(CASE WHEN completed_at_ms > ? THEN 1 ELSE 0 END) AS requests_5h,
SUM(CASE WHEN completed_at_ms > ? THEN uncached_input_tokens + output_tokens ELSE 0 END) AS tokens_5h,
SUM(CASE WHEN completed_at_ms > ? THEN 1 ELSE 0 END) AS requests_7d,
SUM(CASE WHEN completed_at_ms > ? THEN uncached_input_tokens + output_tokens ELSE 0 END) AS tokens_7d`,
			fiveHours.UnixMilli(), fiveHours.UnixMilli(),
			sevenDays.UnixMilli(), sevenDays.UnixMilli()).
		Where("completed_at_ms > ? AND credential_id > 0", sevenDays.UnixMilli()).
		Group("credential_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	usage := make(map[uint]state.CredentialUsage, len(rows))
	for _, row := range rows {
		usage[row.CredentialID] = state.CredentialUsage{
			Requests5h: row.Requests5h,
			Tokens5h:   row.Tokens5h,
			Requests7d: row.Requests7d,
			Tokens7d:   row.Tokens7d,
		}
	}
	if err := service.appendCredentialCredits(ctx, usage, fiveHours.UnixMilli(), sevenDays.UnixMilli()); err != nil {
		return nil, err
	}
	return usage, nil
}

// GLM 编码套餐积分系数(每 10000 token 换算积分), 与官方文档同步:
// GLM-5.3: 输入 6.9 / 缓存命中 1.7 / 输出 24; GLM-5.3-Flash: 2.3 / 0.56 / 8。
// 未匹配模型不计积分。非高峰(周一至五 14:00-18:00 UTC+8 之外)按 50% 抵扣。
const glmCreditCoefficientSQL = `(
	uncached_input_tokens * CASE WHEN model LIKE 'glm-5.3-flash%' THEN 2.3 WHEN model LIKE 'glm-5.3%' THEN 6.9 ELSE 0 END
	+ cache_read_tokens * CASE WHEN model LIKE 'glm-5.3-flash%' THEN 0.56 WHEN model LIKE 'glm-5.3%' THEN 1.7 ELSE 0 END
	+ output_tokens * CASE WHEN model LIKE 'glm-5.3-flash%' THEN 8.0 WHEN model LIKE 'glm-5.3%' THEN 24.0 ELSE 0 END
) / 10000.0 * CASE
	WHEN CAST(strftime('%w', bucket_start_ms / 1000, 'unixepoch', '+8 hours') AS INTEGER) BETWEEN 1 AND 5
	 AND CAST(strftime('%H', bucket_start_ms / 1000, 'unixepoch', '+8 hours') AS INTEGER) BETWEEN 14 AND 17
	THEN 1.0 ELSE 0.5 END`

// appendCredentialCredits 从 usage_stats(含 model 与 bucket 时间)按官方公式
// 换算各凭证 5h/7d 积分, 合并进已有用量表。失败不影响请求数/token 口径。
func (service *Service) appendCredentialCredits(
	ctx context.Context,
	usage map[uint]state.CredentialUsage,
	fiveHoursMS int64,
	sevenDaysMS int64,
) error {
	rows := make([]struct {
		CredentialID uint    `gorm:"column:credential_id"`
		Credits5h    float64 `gorm:"column:credits_5h"`
		Credits7d    float64 `gorm:"column:credits_7d"`
	}, 0, 16)
	if err := service.db.WithContext(ctx).
		Table("usage_stats").
		Select(`credential_id,
SUM(CASE WHEN bucket_start_ms > ? THEN `+glmCreditCoefficientSQL+` ELSE 0 END) AS credits_5h,
SUM(`+glmCreditCoefficientSQL+`) AS credits_7d`, fiveHoursMS).
		Where("credential_id > 0 AND bucket_start_ms > ?", sevenDaysMS).
		Group("credential_id").
		Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		entry := usage[row.CredentialID]
		entry.Credits5h = row.Credits5h
		entry.Credits7d = row.Credits7d
		usage[row.CredentialID] = entry
	}
	return nil
}
