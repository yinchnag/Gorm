package orm

import (
	"reflect"
	"testing"
)

type mockUser struct {
	UserID   int64   `orm:"primary,name:user_id,autoInc"`
	UserName string  `orm:"name:user_name,length:100,notNull"`
	Age      int     `orm:"name:age"`
	Score    float64 `orm:"name:score"`
}

func TestGetTableMeta(t *testing.T) {
	meta := GetTableMeta(reflect.TypeOf(&mockUser{}))
	if meta.TableName != "mock_user" {
		t.Errorf("expected mock_user, got %s", meta.TableName)
	}
	if len(meta.Fields) != 4 {
		t.Errorf("expected 4 fields, got %d", len(meta.Fields))
	}
	if meta.PrimaryField == nil {
		t.Fatal("primary field is nil")
	}
	if meta.PrimaryField.ColName != "user_id" {
		t.Errorf("expected user_id, got %s", meta.PrimaryField.ColName)
	}
	if !meta.PrimaryField.IsAutoInc {
		t.Error("expected autoInc=true")
	}
	// 验证偏移量唯一（各字段偏移互不相同）
	offsets := make(map[uintptr]string)
	for _, f := range meta.Fields {
		if prev, dup := offsets[f.Offset]; dup {
			t.Errorf("offset collision between %s and %s", f.GoName, prev)
		}
		offsets[f.Offset] = f.GoName
	}
}

func TestReadPrimaryKey(t *testing.T) {
	u := &mockUser{UserID: 9999}
	meta := GetTableMeta(reflect.TypeOf(u))
	pk := ReadPrimaryKey(pointerOf(u), meta.PrimaryField)
	if pk.(int64) != 9999 {
		t.Errorf("expected 9999, got %v", pk)
	}
}

func TestToSnakeCase(t *testing.T) {
	cases := [][2]string{
		{"UserID", "user_i_d"},
		{"TestUser", "test_user"},
		{"score", "score"},
	}
	for _, c := range cases {
		if got := toSnakeCase(c[0]); got != c[1] {
			t.Errorf("toSnakeCase(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}
