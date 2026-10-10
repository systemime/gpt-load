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
	return usage, nil
}
