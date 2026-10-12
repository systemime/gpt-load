package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

const ID0031 = "0031_credential_week_reset_anchor"

// Up0031 为单个凭据增加周周期锚点覆盖列(官方团队套餐按席位独立计时):
// NULL/0 表示跟随全局 glm_week_reset_anchor_ms, 非 0 为该凭据独立的
// 官方周重置锚点(epoch 毫秒)。用于席位单独下单/换绑导致周期错位的凭证。
func Up0031(db *gorm.DB) error {
	if err := ValidateRecoverable0031(db); err != nil {
		return err
	}
	if !db.Migrator().HasColumn("credentials", "week_reset_anchor_ms") {
		if err := db.Exec("ALTER TABLE credentials ADD COLUMN week_reset_anchor_ms INTEGER NULL").Error; err != nil {
			return fmt.Errorf("add credential week reset anchor: %w", err)
		}
	}
	return Validate0031(db)
}

func ValidateRecoverable0031(db *gorm.DB) error {
	switch db.Dialector.Name() {
	case "sqlite", "mysql", "postgres":
	default:
		return fmt.Errorf("unsupported credential week reset anchor migration driver %q", db.Dialector.Name())
	}
	if !db.Migrator().HasTable("credentials") {
		return fmt.Errorf("credentials table is missing")
	}
	if !db.Migrator().HasColumn("credentials", "week_reset_anchor_ms") {
		return nil
	}
	return Validate0031(db)
}

func Validate0031(db *gorm.DB) error {
	if !db.Migrator().HasColumn("credentials", "week_reset_anchor_ms") {
		return fmt.Errorf("credentials.week_reset_anchor_ms column is missing")
	}
	return nil
}
