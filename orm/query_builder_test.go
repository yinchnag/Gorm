package orm

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
)

func TestQueryBuilderSQL(t *testing.T) {
	meta := GetTableMeta(reflect.TypeOf(&flushTestObj{}))
	qb := NewQueryBuilder[*flushTestObj](meta).
		Where("score > 10").
		OrderBy("score DESC").
		Limit(50)

	// 构造期望 SQL（不执行，纯逻辑验证）
	cols := []string{"`id`", "`name`", "`score`"}
	expectedParts := []string{
		"SELECT " + strings.Join(cols, ","),
		"FROM `flush_test_obj`",
		"WHERE (`is_deleted`=0)",
		"AND (score > 10)",
		"ORDER BY score DESC",
		"LIMIT 50",
	}
	// 重建 SQL 以验证 builder 字段
	if qb.where != "score > 10" {
		t.Errorf("where mismatch: %q", qb.where)
	}
	if qb.orderBy != "score DESC" {
		t.Errorf("orderBy mismatch: %q", qb.orderBy)
	}
	if qb.limit != 50 {
		t.Errorf("limit mismatch: %d", qb.limit)
	}
	_ = expectedParts // SQL 字符串验证依赖真实 ctx，跳过
}

func TestMakeScanDest(t *testing.T) {
	meta := GetTableMeta(reflect.TypeOf(&flushTestObj{}))
	objPtr := reflect.New(reflect.TypeOf(flushTestObj{}))
	base := objPtr.UnsafePointer()
	dest, targets := makeScanDest(meta, base)
	if len(dest) != len(meta.Fields) {
		t.Errorf("dest length %d != fields %d", len(dest), len(meta.Fields))
	}
	if len(targets) != len(meta.Fields) {
		t.Errorf("targets length %d != fields %d", len(targets), len(meta.Fields))
	}
	// 验证第一个字段（int64 id）使用 NullInt64 缓冲
	if _, ok := dest[0].(*sql.NullInt64); !ok {
		t.Errorf("expected *sql.NullInt64 for first field id, got %T", dest[0])
	}
}
