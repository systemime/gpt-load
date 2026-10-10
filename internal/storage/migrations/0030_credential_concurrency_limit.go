package migrations

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

const ID0030 = "0030_credential_concurrency_limit"

// Up0030 为单个凭据增加并发上限覆盖列: NULL 表示跟随全局
// credential_concurrency_limit, 1..64 为该凭据独立上限。
func Up0030(db *gorm.DB) error {
	if err := ValidateRecoverable0030(db); err != nil {
		return err
	}
	if !db.Migrator().HasColumn("credentials", "concurrency_limit") {
		if err := db.Exec("ALTER TABLE credentials ADD COLUMN concurrency_limit INTEGER NULL").Error; err != nil {
			return fmt.Errorf("add credential concurrency limit: %w", err)
		}
	}
	return Validate0030(db)
}

func ValidateRecoverable0030(db *gorm.DB) error {
	switch db.Dialector.Name() {
	case "sqlite", "mysql", "postgres":
	default:
		return fmt.Errorf("unsupported credential concurrency limit migration driver %q", db.Dialector.Name())
	}
	if !db.Migrator().HasTable("credentials") {
		return fmt.Errorf("credentials table is missing")
	}
	if !db.Migrator().HasColumn("credentials", "concurrency_limit") {
		return nil
	}
	return Validate0030(db)
}

func Validate0030(db *gorm.DB) error {
	columns, err := db.Migrator().ColumnTypes("credentials")
	if err != nil {
		return err
	}
	for _, column := range columns {
		if column.Name() != "concurrency_limit" {
			continue
		}
		name := strings.ToLower(column.DatabaseTypeName())
		if !strings.Contains(name, "int") {
			return fmt.Errorf("credential concurrency limit must be an integer type")
		}
		nullable, known := column.Nullable()
		if !known || !nullable {
			return fmt.Errorf("credential concurrency limit must be nullable")
		}
		return nil
	}
	return fmt.Errorf("credential concurrency limit is missing")
}
