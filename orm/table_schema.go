package orm

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"unsafe"
)

// ISchema 是 TableSchema 的接口约束，用于在泛型约束中引用。
type ISchema interface {
	tableName() string
	primaryKeyVal() any
}

// TableSchema 是所有游戏数据对象的嵌入基类（CRTP 模式）。
//
// 使用方式：
//
//	type Player struct {
//	    TableSchema[*Player]
//	    PlayerID int64 `orm:"primary,name:player_id,autoInc"`
//	    ...
//	}
//
// T 必须是指向宿主结构体的指针类型，例如 *Player。
// 框架通过 T 的类型信息在 metaCache 中缓存 TableMeta，做到类型级零开销元数据获取。
type TableSchema[T any] struct {
	// selfPtr 在 Init() 时被设置为指向宿主对象的指针，
	// 后续所有操作通过此指针进行字段偏移读写，无需再次 interface 装箱。
	selfPtr unsafe.Pointer
	// once 保证同一实例只执行一次 Init 内部逻辑。
	once sync.Once
	// meta 缓存宿主类型的 TableMeta，避免每次操作都走 metaCache。
	meta *TableMeta
	// globalOffset 指向宿主结构体中的 Global bool 字段偏移（若存在）。
	globalOffset uintptr
	// hasGlobalField 标记宿主结构体是否声明了 Global bool 字段。
	hasGlobalField bool
}

// Init 必须在使用任何 ORM 方法前调用，完成两件事：
//  1. 将 selfPtr 指向宿主对象（T 本身是 *Host，故通过 unsafe 取得 Host 基址）
//  2. 从 metaCache 取出（或首次构建）宿主类型的 TableMeta
//
// DDL 同步（AutoMigrate）不在此处执行；应在进程启动时显式调用 orm.MigrateTypes。
//
// 示例：user := &Player{PlayerID: 1001}; user.Init()
func (s *TableSchema[T]) Init() {
	s.once.Do(func() {
		var zero T
		rt := reflect.TypeOf(zero) // *Host
		if rt == nil {
			panic("gameorm: TableSchema[T] requires T to be a concrete pointer type")
		}
		if rt.Kind() != reflect.Ptr || rt.Elem().Kind() != reflect.Struct {
			panic("gameorm: TableSchema[T] requires T to be a pointer to struct type")
		}

		s.meta = GetTableMeta(rt.Elem())
		s.hasGlobalField, s.globalOffset = detectGlobalField(rt.Elem())

		// selfPtr 指向宿主结构体基址：
		// s 是嵌入在宿主结构体内偏移 0 处的字段（嵌入结构体首字段），
		// 因此 &s == 宿主基址（当且仅当 TableSchema 是第一个嵌入字段）。
		s.selfPtr = unsafe.Pointer(s)
	})
}

// Save 将宿主对象写入 Redis（同步）并提交 MySQL 异步存盘请求（不阻塞）。
// 多次调用时，新的存盘请求会覆盖队列中尚未执行的旧请求，以减少 MySQL 写入次数。
func (s *TableSchema[T]) Save() {
	s.mustInit()
	ctx := context.Background()
	pk := ReadPrimaryKey(s.selfPtr, s.meta.PrimaryField)
	useGlobal := s.useGlobalStorage()

	// 1. 同步写 Redis 热缓存
	rds := getRedisStoreForRoute(useGlobal)
	hostObj := s.hostInterface()
	if err := rds.Set(ctx, s.meta.TableName, pk, hostObj); err != nil {
		fmt.Printf("[gameorm] Save redis error [%s:%v]: %v\n", s.meta.TableName, pk, err)
	}

	// 2. 异步入队 MySQL 存盘（不阻塞游戏逻辑）
	getMySQLStoreForRoute(useGlobal).EnqueueSave(s.meta.TableName, s.meta, s.selfPtr)
}

// SaveR 仅将宿主对象写入 Redis，不提交 MySQL 异步存盘。
// 适用于临时态/会话态等无需落盘 MySQL 的数据。
func (s *TableSchema[T]) SaveR() {
	s.mustInit()
	ctx := context.Background()
	pk := ReadPrimaryKey(s.selfPtr, s.meta.PrimaryField)
	hostObj := s.hostInterface()
	useGlobal := s.useGlobalStorage()

	if err := getRedisStoreForRoute(useGlobal).Set(ctx, s.meta.TableName, pk, hostObj); err != nil {
		fmt.Printf("[gameorm] SaveR redis error [%s:%v]: %v\n", s.meta.TableName, pk, err)
	}
}

// Load 按主键从 Redis 读取对象；Redis 未命中时降级读 MySQL，并回写 Redis。
// 结果直接写入宿主对象字段（通过指针偏移），无额外分配。
func (s *TableSchema[T]) Load() error {
	s.mustInit()
	ctx := context.Background()
	pk := ReadPrimaryKey(s.selfPtr, s.meta.PrimaryField)
	useGlobal := s.useGlobalStorage()

	// 1. 先查 Redis
	hostObj := s.hostInterface()
	rds := getRedisStoreForRoute(useGlobal)
	if err := rds.Get(ctx, s.meta.TableName, pk, hostObj); err == nil {
		return nil
	} else if !IsNotFound(err) {
		fmt.Printf("[gameorm] Load redis error [%s:%v]: %v\n", s.meta.TableName, pk, err)
	}

	// 2. Redis 未命中，查 MySQL
	if globalPool == nil {
		return fmt.Errorf("gameorm: pool not initialized")
	}
	return s.loadFromMySQL(ctx, pk, rds, useGlobal)
}

// LoadR 仅从 Redis 加载对象，不会降级查询 MySQL。
func (s *TableSchema[T]) LoadR() error {
	s.mustInit()
	ctx := context.Background()
	pk := ReadPrimaryKey(s.selfPtr, s.meta.PrimaryField)
	hostObj := s.hostInterface()
	useGlobal := s.useGlobalStorage()

	if err := getRedisStoreForRoute(useGlobal).Get(ctx, s.meta.TableName, pk, hostObj); err != nil {
		return fmt.Errorf("gameorm: LoadR [%s:%v] redis: %w", s.meta.TableName, pk, err)
	}
	return nil
}

// loadFromMySQL 从 MySQL 按主键查询单行并回写 Redis（内部方法，保持 Load 函数简洁）。
func (s *TableSchema[T]) loadFromMySQL(ctx context.Context, pk any, rds *RedisStore, useGlobal bool) error {
	meta := s.meta
	cols := make([]string, len(meta.Fields))
	for i, f := range meta.Fields {
		cols[i] = fmt.Sprintf("`%s`", f.ColName)
	}
	query := fmt.Sprintf(
		"SELECT %s FROM `%s` WHERE `%s`=? AND `is_deleted`=0 LIMIT 1",
		joinCols(cols), meta.TableName, meta.PrimaryField.ColName,
	)
	db := GetPool().SelectMySQL(useGlobal)
	row := db.QueryRowContext(ctx, query, pk)
	scanDest, scanTargets := makeScanDest(meta, s.selfPtr)
	if err := row.Scan(scanDest...); err != nil {
		return fmt.Errorf("gameorm: Load [%s:%v] mysql: %w", meta.TableName, pk, err)
	}
	// 将扫描结果回写到字段
	writeScanResultsToFields(meta, s.selfPtr, scanTargets)
	// 回写 Redis
	hostObj := s.hostInterface()
	ttl := getRedisStore().pool
	_ = ttl
	if err := rds.Set(ctx, meta.TableName, pk, hostObj); err != nil {
		fmt.Printf("[gameorm] Load redis set-back error: %v\n", err)
	}
	return nil
}

// Delete 软删除：Redis 删除缓存 + MySQL 异步设置 is_deleted=1。
func (s *TableSchema[T]) Delete() {
	s.mustInit()
	ctx := context.Background()
	pk := ReadPrimaryKey(s.selfPtr, s.meta.PrimaryField)
	useGlobal := s.useGlobalStorage()

	// 1. 删除 Redis 缓存
	if err := getRedisStoreForRoute(useGlobal).Del(ctx, s.meta.TableName, pk); err != nil {
		fmt.Printf("[gameorm] Delete redis error [%s:%v]: %v\n", s.meta.TableName, pk, err)
	}
	// 2. 异步软删除 MySQL
	getMySQLStoreForRoute(useGlobal).EnqueueDelete(s.meta.TableName, s.meta, s.selfPtr)
}

// FindAll 等效于 NewQueryBuilder[T].Where(cond).OrderBy(orderBy).Limit(limit).FindAll(ctx)。
// 提供简洁的单行调用体验：users, err := user.FindAll("age > 18", "age DESC", 100)
func (s *TableSchema[T]) FindAll(cond, orderBy string, limit int) ([]T, error) {
	s.mustInit()
	ctx := context.Background()
	useGlobal := s.useGlobalStorage()
	return NewQueryBuilderWithDB[T](s.meta, GetPool().SelectMySQL(useGlobal)).
		Where(cond).
		OrderBy(orderBy).
		Limit(limit).
		FindAll(ctx)
}

// Migrate 手动触发 AutoMigrate，用于测试或工具程序。
func (s *TableSchema[T]) Migrate() error {
	s.mustInit()
	useGlobal := s.useGlobalStorage()
	return newDDLBuilderWithDB(GetPool().SelectMySQL(useGlobal)).AutoMigrate(context.Background(), s.meta)
}

// Meta 返回宿主类型的 TableMeta，供高级用户直接操作。
func (s *TableSchema[T]) Meta() *TableMeta {
	s.mustInit()
	return s.meta
}

// mustInit 确保 Init 已被调用；若未调用则 panic（开发期快速失败，避免静默错误）。
func (s *TableSchema[T]) mustInit() {
	if s.meta == nil {
		panic("gameorm: Init() must be called before any ORM operation")
	}
}

// hostInterface 将宿主对象转换为 any，供 sonic 序列化使用。
// 利用 T 的类型参数在编译期确定宿主指针类型，通过 unsafe 构造 interface，
// 避免 reflect.NewAt(...).Interface() 带来的额外堆分配。
func (s *TableSchema[T]) hostInterface() T {
	return *(*T)(unsafe.Pointer(&s.selfPtr))
}

func (s *TableSchema[T]) useGlobalStorage() bool {
	if !s.hasGlobalField {
		return false
	}
	ptr := FieldPtr(s.selfPtr, s.globalOffset)
	return *(*bool)(ptr)
}

func detectGlobalField(hostType reflect.Type) (bool, uintptr) {
	f, ok := hostType.FieldByName("Global")
	if !ok || f.Anonymous || f.Type.Kind() != reflect.Bool {
		return false, 0
	}
	return true, f.Offset
}

// joinCols 将列名切片用逗号连接。
func joinCols(cols []string) string {
	result := ""
	for i, c := range cols {
		if i > 0 {
			result += ","
		}
		result += c
	}
	return result
}
