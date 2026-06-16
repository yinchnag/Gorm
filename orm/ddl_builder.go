package orm

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
)

// DDLBuilder 根据 TableMeta 生成建表 DDL 并执行 AUTO MIGRATE。
// 策略：CREATE TABLE IF NOT EXISTS，再通过 INFORMATION_SCHEMA 补全缺失列（ADD COLUMN）。
// 生产环境建议在灰度期用 dry-run 模式先预览 SQL。
type DDLBuilder struct {
	db *sql.DB
}

func newDDLBuilder() *DDLBuilder {
	return newDDLBuilderWithDB(GetPool().SelectMySQL(false))
}

func newDDLBuilderWithDB(db *sql.DB) *DDLBuilder {
	return &DDLBuilder{db: db}
}

// AutoMigrate 确保表存在且所有字段列存在。
// 不会删除旧列，不会修改列类型（安全迁移原则）。
func (d *DDLBuilder) AutoMigrate(ctx context.Context, meta *TableMeta) error {
	if err := d.createTableIfNotExists(ctx, meta); err != nil {
		return err
	}
	return d.addMissingColumns(ctx, meta)
}

func (d *DDLBuilder) createTableIfNotExists(ctx context.Context, meta *TableMeta) error {
	colDefs := make([]string, 0, len(meta.Fields)+2)
	var pkCol string

	for _, f := range meta.Fields {
		colDefs = append(colDefs, buildColumnDef(f))
		if f.IsPrimary {
			pkCol = f.ColName
		}
	}
	// 内置系统列：软删除标记 + 创建时间 + 更新时间
	colDefs = append(colDefs, builtInColumnDefs()...)
	if pkCol != "" {
		colDefs = append(colDefs, fmt.Sprintf("PRIMARY KEY (`%s`)", pkCol))
		colDefs = append(colDefs, builtInIndexDefs()...)
	}

	ddl := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS `%s` (\n  %s\n) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='auto-created by gameorm'",
		meta.TableName,
		strings.Join(colDefs, ",\n  "),
	)
	_, err := d.db.ExecContext(ctx, ddl)
	return err
}

// addMissingColumns 使用 INFORMATION_SCHEMA 检查并补全缺失列。
func (d *DDLBuilder) addMissingColumns(ctx context.Context, meta *TableMeta) error {
	rows, err := d.db.QueryContext(ctx,
		"SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?",
		meta.TableName,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	existing := make(map[string]struct{}, 16)
	for rows.Next() {
		var col string
		if err = rows.Scan(&col); err != nil {
			return err
		}
		existing[col] = struct{}{}
	}
	if err = rows.Err(); err != nil {
		return err
	}

	for _, f := range meta.Fields {
		if _, ok := existing[f.ColName]; ok {
			continue
		}
		alterSQL := fmt.Sprintf(
			"ALTER TABLE `%s` ADD COLUMN %s",
			meta.TableName, buildColumnDef(f),
		)
		if _, err = d.db.ExecContext(ctx, alterSQL); err != nil {
			return fmt.Errorf("addMissingColumns [%s.%s]: %w", meta.TableName, f.ColName, err)
		}
	}

	for col, def := range builtInColumnAddDefs() {
		if _, ok := existing[col]; ok {
			continue
		}
		alterSQL := fmt.Sprintf("ALTER TABLE `%s` ADD COLUMN %s", meta.TableName, def)
		if _, err = d.db.ExecContext(ctx, alterSQL); err != nil {
			return fmt.Errorf("addMissingColumns [%s.%s]: %w", meta.TableName, col, err)
		}
	}
	return nil
}

func builtInColumnDefs() []string {
	return []string{
		"`is_deleted` TINYINT(1) NOT NULL DEFAULT 0 COMMENT '软删除标记：0-正常 1-已删除'",
		"`create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间'",
		"`update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间'",
	}
}

func builtInIndexDefs() []string {
	return []string{
		"INDEX `idx_is_deleted` (`is_deleted`)",
		"INDEX `idx_update_time` (`update_time`)",
	}
}

func builtInColumnAddDefs() map[string]string {
	return map[string]string{
		"is_deleted":  "`is_deleted` TINYINT(1) NOT NULL DEFAULT 0 COMMENT '软删除标记：0-正常 1-已删除'",
		"create_time": "`create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间'",
		"update_time": "`update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间'",
	}
}

// buildColumnDef 将 FieldMeta 转换为 SQL 列定义片段。
// 为了支持补列时的向后兼容性（已有记录为 NULL），自动补充合理的默认值。
func buildColumnDef(f *FieldMeta) string {
	sqlType := goTypeToMySQL(f)
	var sb strings.Builder
	fmt.Fprintf(&sb, "`%s` %s", f.ColName, sqlType)
	if f.NotNull || f.IsPrimary {
		sb.WriteString(" NOT NULL")
	} else {
		sb.WriteString(" NULL")
	}
	if f.IsPrimary && f.IsAutoInc {
		sb.WriteString(" AUTO_INCREMENT")
	}

	// 为非主键字段补充默认值（补列时兼容已有 NULL 记录）
	if !f.IsPrimary {
		defaultVal := getDefaultValue(f)
		if defaultVal != "" {
			fmt.Fprintf(&sb, " DEFAULT %s", defaultVal)
		}
	}

	if f.Comment != "" {
		fmt.Fprintf(&sb, " COMMENT '%s'", strings.ReplaceAll(f.Comment, "'", "\\'"))
	}
	return sb.String()
}

// getDefaultValue 根据字段类型返回合理的 SQL DEFAULT 值。
// 注意：TEXT/BLOB/JSON 等大对象类型在 MySQL 中不支持 DEFAULT，返回空字符串。
func getDefaultValue(f *FieldMeta) string {
	switch f.GoType.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "0"
	case reflect.Bool:
		return "0"
	case reflect.String:
		// 仅对 VARCHAR（有明确长度）补充默认值
		// TEXT（Length == 0）不支持 DEFAULT 值
		if f.Length > 0 {
			return "''"
		}
		return ""
	}
	// 其他类型（如 time.Time）不设置默认值，保持 NULL
	return ""
}

// goTypeToMySQL 映射 Go 类型到 MySQL 列类型。
func goTypeToMySQL(f *FieldMeta) string {
	switch f.GoType.Kind() {
	case reflect.Int8:
		return "TINYINT"
	case reflect.Int16:
		return "SMALLINT"
	case reflect.Int32:
		return "INT"
	case reflect.Int, reflect.Int64:
		return "BIGINT"
	case reflect.Uint8:
		return "TINYINT UNSIGNED"
	case reflect.Uint16:
		return "SMALLINT UNSIGNED"
	case reflect.Uint32:
		return "INT UNSIGNED"
	case reflect.Uint, reflect.Uint64:
		return "BIGINT UNSIGNED"
	case reflect.Float32:
		return "FLOAT"
	case reflect.Float64:
		return "DOUBLE"
	case reflect.Bool:
		return "TINYINT(1)"
	case reflect.String:
		if f.Length > 0 {
			return fmt.Sprintf("VARCHAR(%d)", f.Length)
		}
		return "TEXT"
	default:
		return "JSON"
	}
}
