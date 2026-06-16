package orm

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unsafe"
)

// FieldMeta 描述一个结构体字段到数据库列的映射元数据。
// 使用 unsafe.Offsetof 直接持有字段相对于结构体基址的偏移，
// 后续通过指针运算直接读写字段值，避免 reflect.Value 带来的分配与装箱开销。
type FieldMeta struct {
	GoName    string        // Go 字段名
	ColName   string        // 数据库列名
	Comment   string        // 列注释
	GoType    reflect.Type  // 字段 reflect.Type
	Offset    uintptr       // 字段在结构体内的字节偏移
	IsPrimary bool          // 是否是主键
	IsAutoInc bool          // 主键是否自增
	NotNull   bool          // 是否 NOT NULL
	Length    int           // 字符串列长度，0 表示不限
}

// TableMeta 描述一张表的全部字段元数据，使用 sync.Map 做类型级缓存以实现无锁读。
type TableMeta struct {
	TableName    string
	Fields       []*FieldMeta // 所有参与 ORM 的字段（不含嵌入的 TableSchema）
	PrimaryField *FieldMeta   // 主键字段（唯一）
}

var metaCache sync.Map // key: reflect.Type → *TableMeta

// GetTableMeta 获取（或懒初始化）指定类型的 TableMeta。
// T 必须是指针，例如 *TestUser；传入非指针将 panic。
func GetTableMeta(t reflect.Type) *TableMeta {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if v, ok := metaCache.Load(t); ok {
		return v.(*TableMeta)
	}
	meta := buildTableMeta(t)
	metaCache.Store(t, meta)
	return meta
}

// buildTableMeta 通过反射解析 struct tag，构建 TableMeta。
// 命名规则：表名 = snake_case(TypeName)；列名 = tag.name 或 snake_case(GoName)。
func buildTableMeta(t reflect.Type) *TableMeta {
	meta := &TableMeta{
		TableName: toSnakeCase(t.Name()),
	}

	for i := range t.NumField() {
		f := t.Field(i)
		// 跳过嵌入字段（TableSchema 本身）
		if f.Anonymous {
			continue
		}
		tag := f.Tag.Get("orm")
		if tag == "-" {
			continue
		}
		fm := parseFieldMeta(f, tag)
		meta.Fields = append(meta.Fields, fm)
		if fm.IsPrimary {
			meta.PrimaryField = fm
		}
	}
	if meta.PrimaryField == nil && len(meta.Fields) > 0 {
		// 兜底：第一个字段作为主键
		meta.PrimaryField = meta.Fields[0]
		meta.PrimaryField.IsPrimary = true
	}
	return meta
}

// parseFieldMeta 解析单个字段的 orm tag。
// tag 格式示例：primary,name:user_id,comment:用户ID,autoInc,length:100,notNull
func parseFieldMeta(f reflect.StructField, tag string) *FieldMeta {
	fm := &FieldMeta{
		GoName: f.Name,
		GoType: f.Type,
		Offset: f.Offset,
	}
	// 默认列名 = snake_case
	fm.ColName = toSnakeCase(f.Name)

	parts := strings.Split(tag, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		switch {
		case p == "primary":
			fm.IsPrimary = true
		case p == "autoInc":
			fm.IsAutoInc = true
		case p == "notNull":
			fm.NotNull = true
		case strings.HasPrefix(p, "name:"):
			fm.ColName = strings.TrimPrefix(p, "name:")
		case strings.HasPrefix(p, "comment:"):
			fm.Comment = strings.TrimPrefix(p, "comment:")
		case strings.HasPrefix(p, "length:"):
			n, _ := strconv.Atoi(strings.TrimPrefix(p, "length:"))
			fm.Length = n
		}
	}
	return fm
}

// FieldPtr 返回指向某对象（ptr 是指向结构体的 unsafe.Pointer）内
// 指定字段的 unsafe.Pointer，可配合 *(*T)(ptr) 模式直接操作字段值。
func FieldPtr(base unsafe.Pointer, offset uintptr) unsafe.Pointer {
	return unsafe.Pointer(uintptr(base) + offset)
}

// ReadPrimaryKey 从对象读取主键值（int64/string/int32/int），其余类型返回 fmt.Sprintf。
func ReadPrimaryKey(base unsafe.Pointer, fm *FieldMeta) any {
	ptr := FieldPtr(base, fm.Offset)
	switch fm.GoType.Kind() {
	case reflect.Int64:
		return *(*int64)(ptr)
	case reflect.Int32:
		return int64(*(*int32)(ptr))
	case reflect.Int:
		return int64(*(*int)(ptr))
	case reflect.String:
		return *(*string)(ptr)
	default:
		// 泛型兜底，使用反射读取
		v := reflect.NewAt(fm.GoType, ptr).Elem()
		return fmt.Sprintf("%v", v.Interface())
	}
}

// toSnakeCase 将驼峰 Go 名称转换为 snake_case 列名/表名。
func toSnakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteByte(byte(r + 32))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
