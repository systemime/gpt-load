package control

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"gpt-load/internal/catalog"
	"gpt-load/internal/health"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/pricing"
	"gpt-load/internal/storage/models"

	"github.com/sirupsen/logrus"
)

const defaultAccessKeyMarker = models.InternalSystemSettingPrefix + "bootstrap.default_access_key.v1"

func (s *Service) EnsureInitialState(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var catalogSnapshot *catalog.Snapshot
	if s.catalogRuntime != nil {
		catalogSnapshot = s.catalogRuntime.Load()
	}

	var priceTable *pricing.Table
	err := s.withControlTransaction(ctx, func(tx *gorm.DB) error {
		nowMS := s.now().UnixMilli()
		if err := tx.Model(&models.Credential{}).
			Where("auth_state = ?", models.CredentialAuthStateRefreshing).
			Updates(map[string]any{
				"auth_state":      models.CredentialAuthStateOutcomeUnknown,
				"auth_error_code": "refresh_interrupted", "updated_at_ms": nowMS,
			}).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
		if err := tx.Model(&models.CredentialStage{}).
			Where("status = ?", models.CredentialStageExchanging).
			Updates(map[string]any{
				"status":            models.CredentialStageOutcomeUnknown,
				"encrypted_payload": "", "oauth_state_hash": nil,
				"error_code": "authorization_exchange_interrupted", "updated_at_ms": nowMS,
			}).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
		var marker models.SystemSetting
		query := tx.Where(&models.SystemSetting{Key: defaultAccessKeyMarker}).
			Limit(1).Find(&marker)
		if query.Error != nil {
			return app_errors.ParseDBError(query.Error)
		}
		if query.RowsAffected == 0 {
			var count int64
			if err := tx.Model(&models.AccessKey{}).Count(&count).Error; err != nil {
				return app_errors.ParseDBError(err)
			}
			if count == 0 {
				filters, err := normalizeAccessKeyFilters(nil)
				if err != nil {
					return fmt.Errorf("build default access key filters: %w", err)
				}
				row, _, err := s.newAccessKeyRow("Default", filters, 0, "")
				if err != nil {
					return err
				}
				if err := tx.Create(&row).Error; err != nil {
					return app_errors.ParseDBError(err)
				}
			}

			marker = models.SystemSetting{
				Key:   defaultAccessKeyMarker,
				Value: "true",
			}
			if err := tx.Create(&marker).Error; err != nil {
				return app_errors.ParseDBError(err)
			}
		}

		if catalogSnapshot != nil {
			if err := reconcileCatalogAutomaticPrices(tx, catalogSnapshot); err != nil {
				return err
			}
		}
		if err := reconcileReferencedPrices(tx, catalogSnapshot); err != nil {
			return err
		}
		if err := cleanupUnreferencedAutomaticPrices(tx); err != nil {
			return err
		}
		var err error
		priceTable, err = loadPriceTable(ctx, tx)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("ensure initial control state: %w", err)
	}
	s.priceRuntime.Publish(priceTable)
	logrus.WithField("event", "startup.model_prices_publish").Info("model prices published")
	s.seedCredentialHealthStats()
	return nil
}

// seedCredentialHealthStats 用持久化的最近一次冷却触发记录回填健康统计。
// 健康统计只存在于进程内, 重启后冷却中的凭证不会产生新尝试(被冷却拦截),
// 不回填的话健康页只能显示"失败原因已不在近期记录"。
func (s *Service) seedCredentialHealthStats() {
	if s.stats == nil {
		return
	}
	type cooldownSeed struct {
		CredentialID    uint
		FailureCategory string
		StatusCode      int
		ErrorSummary    string
		CompletedAtMS   int64
	}
	var rows []cooldownSeed
	err := s.db.Model(&models.RequestLogAttempt{}).
		Select("credential_id, failure_category, status_code, error_summary, MAX(completed_at_ms) AS completed_at_ms").
		Where("action = ?", "cooldown_credential").
		Group("credential_id").
		Scan(&rows).Error
	if err != nil {
		logrus.WithError(err).Warn("seed credential health stats from cooldown attempts")
		return
	}
	for _, row := range rows {
		s.stats.RecordProblem(
			row.CredentialID,
			seedFailureCategory(row.FailureCategory),
			row.StatusCode,
			row.ErrorSummary,
			time.UnixMilli(row.CompletedAtMS),
		)
	}
	logrus.WithField("event", "startup.credential_health_seeded").
		WithField("count", len(rows)).
		Info("credential health stats seeded from cooldown attempts")
}

func seedFailureCategory(value string) health.FailureCategory {
	for category := health.FailureCategoryAmbiguous; category <= health.FailureCategoryAuthenticationRequired; category++ {
		if category.String() == value {
			return category
		}
	}
	return health.FailureCategoryAmbiguous
}
