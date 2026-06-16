package orm

import (
	"reflect"
	"sync"
	"testing"
)

type flushTestObj struct {
	ID    int64  `orm:"primary,name:id,autoInc"`
	Name  string `orm:"name:name"`
	Score int    `orm:"name:score"`
}

func TestFlushQueueDedup(t *testing.T) {
	q := newFlushQueue()

	meta := GetTableMeta(reflect.TypeOf(&flushTestObj{}))
	obj1 := &flushTestObj{ID: 1, Name: "v1", Score: 10}
	obj2 := &flushTestObj{ID: 1, Name: "v2", Score: 20}

	push := func(o *flushTestObj) {
		snap := snapshotFields(meta, pointerOf(o))
		q.push(&pendingItem{key: "t:1", tableName: "t", meta: meta, snapshot: snap})
	}
	push(obj1)
	push(obj2) // 应覆盖 obj1

	items := q.drain()
	if len(items) != 1 {
		t.Fatalf("expected 1 item after dedup, got %d", len(items))
	}
	// snapshot 应该是 obj2 的值
	nameIdx := 1 // Fields[1] = name
	if items[0].snapshot[nameIdx].(string) != "v2" {
		t.Errorf("expected v2, got %v", items[0].snapshot[nameIdx])
	}
}

func TestHashKey(t *testing.T) {
	a := hashKey("player:1001")
	b := hashKey("player:1001")
	if a != b {
		t.Error("hash must be deterministic")
	}
	c := hashKey("player:1002")
	if a == c {
		t.Log("hash collision (rare, acceptable)")
	}
}

func TestSnapshotFields(t *testing.T) {
	obj := &flushTestObj{ID: 42, Name: "test", Score: 99}
	meta := GetTableMeta(reflect.TypeOf(obj))
	snap := snapshotFields(meta, pointerOf(obj))
	if snap[0].(int64) != 42 {
		t.Errorf("expected ID=42, got %v", snap[0])
	}
	if snap[1].(string) != "test" {
		t.Errorf("expected name=test, got %v", snap[1])
	}
	if snap[2].(int) != 99 {
		t.Errorf("expected score=99, got %v", snap[2])
	}
}

func TestCloneTableMetaDeepCopy(t *testing.T) {
	fields := []*FieldMeta{
		{GoName: "ID", ColName: "id", IsPrimary: true},
		{GoName: "Name", ColName: "name"},
	}
	orig := &TableMeta{TableName: "flush_test", Fields: fields, PrimaryField: fields[0]}

	cloned := cloneTableMeta(orig)
	if cloned == orig {
		t.Fatal("cloneTableMeta should return a new TableMeta instance")
	}
	if len(cloned.Fields) != len(orig.Fields) {
		t.Fatalf("expected %d fields, got %d", len(orig.Fields), len(cloned.Fields))
	}
	if cloned.Fields[0] == orig.Fields[0] {
		t.Fatal("cloneTableMeta should deep-copy FieldMeta entries")
	}
	if cloned.PrimaryField != cloned.Fields[0] {
		t.Fatal("cloned PrimaryField should point to cloned field entry")
	}

	orig.TableName = "mutated_table"
	orig.Fields[0].ColName = "mutated_id"
	orig.PrimaryField.ColName = "mutated_pk"

	if cloned.TableName != "flush_test" {
		t.Fatalf("cloned table name changed unexpectedly: %s", cloned.TableName)
	}
	if cloned.Fields[0].ColName != "id" {
		t.Fatalf("cloned field changed unexpectedly: %s", cloned.Fields[0].ColName)
	}
}

func TestFreezeTableMetaCachesSingleClone(t *testing.T) {
	fields := []*FieldMeta{
		{GoName: "ID", ColName: "id", IsPrimary: true},
		{GoName: "Name", ColName: "name"},
	}
	orig := &TableMeta{TableName: "freeze_test", Fields: fields, PrimaryField: fields[0]}

	const n = 16
	results := make([]*TableMeta, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = freezeTableMeta(orig)
		}(i)
	}
	wg.Wait()

	first := results[0]
	if first == nil {
		t.Fatal("freezeTableMeta returned nil")
	}
	if first == orig {
		t.Fatal("freezeTableMeta should not return original meta pointer")
	}
	for i := 1; i < n; i++ {
		if results[i] != first {
			t.Fatal("freezeTableMeta should return same cached clone for same meta")
		}
	}
}
