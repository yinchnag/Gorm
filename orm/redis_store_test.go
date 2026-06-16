package orm

import (
	"reflect"
	"testing"
	"unsafe"
)

type pkTestObj struct {
	ID   int64
	Name string
}

func TestPointerOf(t *testing.T) {
	obj := &pkTestObj{ID: 42, Name: "hello"}
	ptr := pointerOf(obj)
	// 通过偏移读取 ID 字段（偏移=0）
	id := *(*int64)(unsafe.Pointer(uintptr(ptr)))
	if id != 42 {
		t.Errorf("expected 42, got %d", id)
	}
}

func TestRedisKey(t *testing.T) {
	key := redisKey("player", int64(1001))
	if key != "player:1001" {
		t.Errorf("unexpected key: %s", key)
	}
}

func TestIsNotFound(t *testing.T) {
	if !IsNotFound(errRedisNil()) {
		t.Error("expected IsNotFound=true for redis.Nil")
	}
}

// errRedisNil 返回 goredis.Nil，避免引入 goredis 包直接依赖测试文件。
func errRedisNil() error {
	store := &RedisStore{}
	_ = store
	return goredisNil
}

type redisHashCodecObj struct {
	TableSchema[*redisHashCodecObj]
	ID    int64            `orm:"primary,name:id"`
	Name  string           `orm:"name:name,length:64"`
	Score float64          `orm:"name:score"`
	Tags  map[string]int64 `orm:"name:tags"`
}

func TestRedisHashFieldCodecRoundTrip(t *testing.T) {
	obj := &redisHashCodecObj{
		ID:    101,
		Name:  "alice",
		Score: 99.5,
		Tags:  map[string]int64{"pve": 7, "pvp": 3},
	}

	meta := GetTableMeta(reflect.TypeOf(obj))
	fields, err := buildRedisHashFields(meta, pointerOf(obj))
	if err != nil {
		t.Fatalf("buildRedisHashFields error: %v", err)
	}

	raw := make(map[string]string, len(fields))
	for k, v := range fields {
		raw[k] = v.(string)
	}

	out := &redisHashCodecObj{}
	if err := applyRedisHashFields(meta, pointerOf(out), raw); err != nil {
		t.Fatalf("applyRedisHashFields error: %v", err)
	}

	if out.ID != obj.ID || out.Name != obj.Name || out.Score != obj.Score {
		t.Fatalf("primitive fields mismatch: out=%+v want=%+v", out, obj)
	}
	if len(out.Tags) != len(obj.Tags) || out.Tags["pve"] != obj.Tags["pve"] || out.Tags["pvp"] != obj.Tags["pvp"] {
		t.Fatalf("map field mismatch: out=%v want=%v", out.Tags, obj.Tags)
	}
}
