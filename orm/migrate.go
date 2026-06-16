package orm

import (
	"context"
	"fmt"
	"reflect"
	"sync"
)

type migrateKey struct {
	t         reflect.Type
	useGlobal bool
}

var migratedTypes sync.Map // key: migrateKey → struct{}

// MigrateTypes 在启动时对每个业务类型执行 AutoMigrate（CREATE TABLE IF NOT EXISTS + 补列）。
// 必须在 InitPool 之后、第一次 Save/Load/Delete 之前调用。
//
// protos 是各类型的零值指针实例，用于提取类型信息。
// 若某个类型的 Global bool 字段值为 true，则在 GlobalDB 上执行；否则在默认 DB 上执行。
//
// 示例：
//
//	orm.InitPool("config/orm.json")
//	if err := orm.MigrateTypes(&Player{}, &Item{}, &GuildInfo{Global: true}); err != nil {
//	    panic(err)
//	}
func MigrateTypes(protos ...any) error {
	p := GetPool()
	ctx := context.Background()

	for _, proto := range protos {
		rt := reflect.TypeOf(proto)
		if rt == nil || rt.Kind() != reflect.Ptr || rt.Elem().Kind() != reflect.Struct {
			return fmt.Errorf("gameorm: MigrateTypes: expected pointer-to-struct, got %T", proto)
		}
		hostElem := rt.Elem()
		meta := GetTableMeta(hostElem)

		useGlobal := false
		if f, ok := hostElem.FieldByName("Global"); ok && !f.Anonymous && f.Type.Kind() == reflect.Bool {
			useGlobal = reflect.ValueOf(proto).Elem().FieldByName("Global").Bool()
		}

		k := migrateKey{hostElem, useGlobal}
		if _, loaded := migratedTypes.LoadOrStore(k, struct{}{}); loaded {
			continue
		}

		if useGlobal && p.GlobalDB == nil {
			fmt.Printf("[gameorm] global mysql not configured, skip AutoMigrate [%s]\n", meta.TableName)
			migratedTypes.Delete(k)
			continue
		}

		db := p.SelectMySQL(useGlobal)
		if err := newDDLBuilderWithDB(db).AutoMigrate(ctx, meta); err != nil {
			migratedTypes.Delete(k)
			return fmt.Errorf("[gameorm] AutoMigrate [%s]: %w", meta.TableName, err)
		}
		fmt.Printf("[gameorm] AutoMigrate [%s] success\n", meta.TableName)
	}
	return nil
}