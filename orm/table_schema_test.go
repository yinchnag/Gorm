package orm

import (
	"reflect"
	"testing"
)

// TestUser 是 ORM 文档中的典型用例，用于验证 TableSchema CRTP。
type TestUser struct {
	TableSchema[*TestUser]
	UserId   int64   `orm:"primary,name:user_id,comment:用户ID,autoInc"`
	UserName string  `orm:"name:user_name,comment:用户名,length:100,notNull"`
	Email    string  `orm:"name:email,comment:邮箱,length:255"`
	Age      int     `orm:"name:age,comment:年龄"`
	Score    float64 `orm:"name:score,comment:积分"`
	Status   int8    `orm:"name:status,comment:状态"`
}

type GlobalRouteUser struct {
	TableSchema[*GlobalRouteUser]
	ID     int64 `orm:"primary,name:id,autoInc"`
	Global bool  `orm:"-"`
}

func TestTableSchema_Meta(t *testing.T) {
	user := &TestUser{UserId: 5001, UserName: "init_test", Age: 30}
	// Init 不访问连接池，无需 pool 即可运行。
	initialGlobalPool := globalPool
	globalPool = nil
	defer func() { globalPool = initialGlobalPool }()

	user.Init()
	meta := user.Meta()
	if meta == nil {
		t.Fatal("meta should not be nil")
	}
	if meta.TableName != "test_user" {
		t.Errorf("expected test_user, got %s", meta.TableName)
	}
	if meta.PrimaryField == nil || meta.PrimaryField.ColName != "user_id" {
		t.Errorf("primary field mismatch: %v", meta.PrimaryField)
	}
	if len(meta.Fields) != 6 {
		t.Errorf("expected 6 fields, got %d", len(meta.Fields))
	}
}

func TestTableSchema_SelfPtrOffset(t *testing.T) {
	user := &TestUser{UserId: 9876}
	initialGlobalPool := globalPool
	globalPool = nil
	defer func() { globalPool = initialGlobalPool }()
	user.Init()

	// selfPtr 应指向 TestUser 基址，读取第一个非嵌入字段（UserId）
	userMeta := GetTableMeta(reflect.TypeOf(user))
	pk := ReadPrimaryKey(user.selfPtr, userMeta.PrimaryField)
	if pk.(int64) != 9876 {
		t.Errorf("expected pk=9876, got %v", pk)
	}
}

func TestTableSchema_TypeInitCachedAcrossInstances(t *testing.T) {
	initialGlobalPool := globalPool
	globalPool = nil
	defer func() { globalPool = initialGlobalPool }()

	a := &TestUser{UserId: 1}
	b := &TestUser{UserId: 2}

	a.Init()
	b.Init()

	if a.meta == nil || b.meta == nil {
		t.Fatal("meta should not be nil")
	}
	if a.meta != b.meta {
		t.Fatal("same host type should share one cached *TableMeta from metaCache")
	}
}

func TestTableSchema_MustInitPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when calling Save without Init")
		}
	}()
	u := &TestUser{}
	u.mustInit() // 应 panic
}

func TestTableSchema_SaveR_MustInitPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when calling SaveR without Init")
		}
	}()
	u := &TestUser{}
	u.SaveR()
}

func TestTableSchema_LoadR_MustInitPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when calling LoadR without Init")
		}
	}()
	u := &TestUser{}
	_ = u.LoadR()
}

func TestTableSchema_UseGlobalStorageByField(t *testing.T) {
	initialGlobalPool := globalPool
	globalPool = nil
	defer func() { globalPool = initialGlobalPool }()

	u := &GlobalRouteUser{ID: 1, Global: true}
	u.Init()
	if !u.useGlobalStorage() {
		t.Fatal("expected useGlobalStorage=true when Global field is true")
	}

	u.Global = false
	if u.useGlobalStorage() {
		t.Fatal("expected useGlobalStorage=false when Global field is false")
	}
}
