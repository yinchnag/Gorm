package orm

import (
	"context"
	"fmt"
	"reflect"
	"time"
	"unsafe"

	"github.com/bytedance/sonic"
	goredis "github.com/go-redis/redis/v8"
)

// RedisStore 提供对象级 Redis 存取操作，序列化使用 sonic（字节级零拷贝 JSON）。
type RedisStore struct {
	pool      *Pool
	useGlobal bool
}

var (
	defaultRedisStore = &RedisStore{}
	globalRedisStore  = &RedisStore{useGlobal: true}
)

func getRedisStore() *RedisStore {
	return getRedisStoreForRoute(false)
}

func getRedisStoreForRoute(useGlobal bool) *RedisStore {
	if useGlobal {
		globalRedisStore.pool = GetPool()
		return globalRedisStore
	}
	defaultRedisStore.pool = GetPool()
	return defaultRedisStore
}

// redisKey 生成对象的 Redis key：{table}:{pk}
func redisKey(tableName string, pk any) string {
	return fmt.Sprintf("%s:%v", tableName, pk)
}

// Set 将对象按字段写入 Redis Hash，TTL 从全局配置读取。
func (r *RedisStore) Set(ctx context.Context, tableName string, pk any, obj any) error {
	meta := GetTableMeta(reflect.TypeOf(obj))
	base := pointerOf(obj)
	fields, err := buildRedisHashFields(meta, base)
	if err != nil {
		return fmt.Errorf("redisStore.Set build hash fields: %w", err)
	}

	key := redisKey(tableName, pk)
	rcfg := r.pool.SelectRedisConfig(r.useGlobal)
	ttl := time.Duration(rcfg.KeyTTLSec) * time.Second
	client := r.pool.SelectRedis(r.useGlobal)
	pipe := client.TxPipeline()
	pipe.HSet(ctx, key, fields)
	pipe.Expire(ctx, key, ttl)
	_, err = pipe.Exec(ctx)
	return err
}

// Get 从 Redis Hash 读取对象；key 不存在时返回 goredis.Nil。
func (r *RedisStore) Get(ctx context.Context, tableName string, pk any, dest any) error {
	key := redisKey(tableName, pk)
	vals, err := r.pool.SelectRedis(r.useGlobal).HGetAll(ctx, key).Result()
	if err != nil {
		return err // 包含 goredis.Nil
	}
	if len(vals) == 0 {
		return goredis.Nil
	}

	meta := GetTableMeta(reflect.TypeOf(dest))
	base := pointerOf(dest)
	if err := applyRedisHashFields(meta, base, vals); err != nil {
		return fmt.Errorf("redisStore.Get apply hash fields: %w", err)
	}
	return nil
}

// Del 从 Redis 删除对象缓存。
func (r *RedisStore) Del(ctx context.Context, tableName string, pk any) error {
	return r.pool.SelectRedis(r.useGlobal).Del(ctx, redisKey(tableName, pk)).Err()
}

func buildRedisHashFields(meta *TableMeta, base unsafe.Pointer) (map[string]interface{}, error) {
	fields := make(map[string]interface{}, len(meta.Fields))
	for _, f := range meta.Fields {
		ptr := FieldPtr(base, f.Offset)
		v := reflect.NewAt(f.GoType, ptr).Elem().Interface()
		data, err := sonic.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("field=%s marshal: %w", f.ColName, err)
		}
		fields[f.ColName] = string(data)
	}
	return fields, nil
}

func applyRedisHashFields(meta *TableMeta, base unsafe.Pointer, values map[string]string) error {
	for _, f := range meta.Fields {
		raw, ok := values[f.ColName]
		if !ok {
			continue
		}
		ptr := FieldPtr(base, f.Offset)
		target := reflect.NewAt(f.GoType, ptr).Interface()
		if err := sonic.Unmarshal([]byte(raw), target); err != nil {
			return fmt.Errorf("field=%s unmarshal: %w", f.ColName, err)
		}
	}
	return nil
}

// SetRaw 直接写入 JSON bytes，供批量刷盘使用，避免二次序列化。
func (r *RedisStore) SetRaw(ctx context.Context, key string, data []byte, ttl time.Duration) error {
	return r.pool.SelectRedis(r.useGlobal).Set(ctx, key, data, ttl).Err()
}

// IsNotFound 判断 Redis 错误是否为 key 不存在。
func IsNotFound(err error) bool {
	return err == goredis.Nil
}

// pointerOf 将任意指针类型转换为 unsafe.Pointer，用于字段偏移运算。
// 这是整个框架的"魔法入口"：通过 unsafe.Pointer 桥接 reflect 元数据与运行时对象。
func pointerOf(v any) unsafe.Pointer {
	// any 的底层布局：[itab *ptr | data *ptr]
	// 此处利用 Go interface 内存布局直接取 data 指针。
	type iface struct {
		_    uintptr
		data unsafe.Pointer
	}
	return (*iface)(unsafe.Pointer(&v)).data
}
