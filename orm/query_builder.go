package orm

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"unsafe"

	"github.com/bytedance/sonic"
)

// QueryBuilder 为指定表提供 WHERE/ORDER/LIMIT 查询能力，
// 查询结果以 []T 形式返回，通过指针偏移直接写入字段，零 reflect.Set 开销。
type QueryBuilder[T any] struct {
	meta    *TableMeta
	db      DBExecutor
	where   string
	orderBy string
	limit   int
}

type DBExecutor interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// NewQueryBuilder 返回针对类型 T 的查询构建器。
// T 必须是结构体指针，例如 *TestUser。
func NewQueryBuilder[T any](meta *TableMeta) *QueryBuilder[T] {
	return NewQueryBuilderWithDB[T](meta, GetPool().SelectMySQL(false))
}

func NewQueryBuilderWithDB[T any](meta *TableMeta, db DBExecutor) *QueryBuilder[T] {
	return &QueryBuilder[T]{
		meta: meta,
		db:   db,
	}
}

// Where 设置 WHERE 子句（不含 "WHERE" 关键字），
// 框架自动追加 `is_deleted=0` 以过滤软删除记录。
func (q *QueryBuilder[T]) Where(cond string) *QueryBuilder[T] {
	q.where = cond
	return q
}

// OrderBy 设置 ORDER BY 子句（不含 "ORDER BY" 关键字）。
func (q *QueryBuilder[T]) OrderBy(expr string) *QueryBuilder[T] {
	q.orderBy = expr
	return q
}

// Limit 设置最大返回行数。
func (q *QueryBuilder[T]) Limit(n int) *QueryBuilder[T] {
	q.limit = n
	return q
}

// FindAll 执行查询并返回结果切片。
// 直接通过 unsafe 指针将列值写入结构体字段，避免 reflect.Value.Set 带来的隐式分配。
func (q *QueryBuilder[T]) FindAll(ctx context.Context) ([]T, error) {
	cols := make([]string, len(q.meta.Fields))
	for i, f := range q.meta.Fields {
		cols[i] = fmt.Sprintf("`%s`", f.ColName)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "SELECT %s FROM `%s` WHERE (`is_deleted`=0)",
		strings.Join(cols, ","), q.meta.TableName)

	if q.where != "" {
		fmt.Fprintf(&sb, " AND (%s)", q.where)
	}
	if q.orderBy != "" {
		fmt.Fprintf(&sb, " ORDER BY %s", q.orderBy)
	}
	if q.limit > 0 {
		fmt.Fprintf(&sb, " LIMIT %d", q.limit)
	}

	rows, err := q.db.QueryContext(ctx, sb.String())
	if err != nil {
		return nil, fmt.Errorf("FindAll query: %w", err)
	}
	defer rows.Close()

	// 获取 T 的底层 elem 类型（去掉指针层）
	var zero T
	elemType := reflect.TypeOf(zero)
	if elemType == nil || elemType.Kind() == reflect.Ptr {
		if elemType != nil {
			elemType = elemType.Elem()
		}
	}

	var results []T
	for rows.Next() {
		// 分配新对象，获取其基址
		objPtr := reflect.New(elemType)
		base := objPtr.UnsafePointer()

		scanDest, scanTargets := makeScanDest(q.meta, base)
		if err = rows.Scan(scanDest...); err != nil {
			return nil, fmt.Errorf("FindAll scan: %w", err)
		}
		// 将扫描结果从 sql.Null* 缓冲回写到原字段
		writeScanResultsToFields(q.meta, base, scanTargets)
		results = append(results, objPtr.Interface().(T))
	}
	return results, rows.Err()
}

// scanTarget 是一个临时缓冲对象，用于安全地扫描可能为 NULL 的值。
type scanTarget struct {
	fieldIdx int // 字段在 TableMeta.Fields 中的下标
	intVal   sql.NullInt64
	strVal   sql.NullString
	fltVal   sql.NullFloat64
	boolVal  sql.NullBool
	rawVal   []byte
}

// makeScanDest 为每个字段生成 sql.Scan 目标。
// 返回两个值：
//   - []any: 传给 sql.Rows.Scan 的目标指针（使用 sql.Null* 缓冲）
//   - []*scanTarget: 扫描目标的元数据，用于扫描后回写原字段
func makeScanDest(meta *TableMeta, base unsafe.Pointer) ([]any, []*scanTarget) {
	targets := make([]*scanTarget, len(meta.Fields))
	dest := make([]any, len(meta.Fields))
	for i, f := range meta.Fields {
		target := &scanTarget{fieldIdx: i}
		targets[i] = target
		// 根据字段类型选择合适的 sql.Null* 缓冲
		switch f.GoType.Kind() {
		case reflect.Int64, reflect.Int32, reflect.Int, reflect.Int8, reflect.Int16:
			dest[i] = &target.intVal
		case reflect.Uint64, reflect.Uint32, reflect.Uint, reflect.Uint8, reflect.Uint16:
			// MySQL 无符号整数仍然映射到 int64
			dest[i] = &target.intVal
		case reflect.Float32, reflect.Float64:
			dest[i] = &target.fltVal
		case reflect.Bool:
			dest[i] = &target.boolVal
		case reflect.String:
			dest[i] = &target.strVal
		default:
			dest[i] = &target.rawVal
		}
	}
	return dest, targets
}

// writeScanResultsToFields 将 sql.Null* 缓冲中的值安全地写回到结构体字段。
func writeScanResultsToFields(meta *TableMeta, base unsafe.Pointer, targets []*scanTarget) {
	for i, target := range targets {
		if i >= len(meta.Fields) {
			break
		}
		f := meta.Fields[i]
		ptr := FieldPtr(base, f.Offset)

		switch f.GoType.Kind() {
		case reflect.Int64:
			if target.intVal.Valid {
				*(*int64)(ptr) = target.intVal.Int64
			} else {
				*(*int64)(ptr) = 0
			}
		case reflect.Int32:
			if target.intVal.Valid {
				*(*int32)(ptr) = int32(target.intVal.Int64)
			} else {
				*(*int32)(ptr) = 0
			}
		case reflect.Int:
			if target.intVal.Valid {
				*(*int)(ptr) = int(target.intVal.Int64)
			} else {
				*(*int)(ptr) = 0
			}
		case reflect.Int8:
			if target.intVal.Valid {
				*(*int8)(ptr) = int8(target.intVal.Int64)
			} else {
				*(*int8)(ptr) = 0
			}
		case reflect.Int16:
			if target.intVal.Valid {
				*(*int16)(ptr) = int16(target.intVal.Int64)
			} else {
				*(*int16)(ptr) = 0
			}
		case reflect.Uint64:
			if target.intVal.Valid {
				*(*uint64)(ptr) = uint64(target.intVal.Int64)
			} else {
				*(*uint64)(ptr) = 0
			}
		case reflect.Uint32:
			if target.intVal.Valid {
				*(*uint32)(ptr) = uint32(target.intVal.Int64)
			} else {
				*(*uint32)(ptr) = 0
			}
		case reflect.Uint:
			if target.intVal.Valid {
				*(*uint)(ptr) = uint(target.intVal.Int64)
			} else {
				*(*uint)(ptr) = 0
			}
		case reflect.Uint8:
			if target.intVal.Valid {
				*(*uint8)(ptr) = uint8(target.intVal.Int64)
			} else {
				*(*uint8)(ptr) = 0
			}
		case reflect.Uint16:
			if target.intVal.Valid {
				*(*uint16)(ptr) = uint16(target.intVal.Int64)
			} else {
				*(*uint16)(ptr) = 0
			}
		case reflect.Float32:
			if target.fltVal.Valid {
				*(*float32)(ptr) = float32(target.fltVal.Float64)
			} else {
				*(*float32)(ptr) = 0
			}
		case reflect.Float64:
			if target.fltVal.Valid {
				*(*float64)(ptr) = target.fltVal.Float64
			} else {
				*(*float64)(ptr) = 0
			}
		case reflect.Bool:
			if target.boolVal.Valid {
				*(*bool)(ptr) = target.boolVal.Bool
			} else {
				*(*bool)(ptr) = false
			}
		case reflect.String:
			if target.strVal.Valid {
				*(*string)(ptr) = target.strVal.String
			} else {
				*(*string)(ptr) = ""
			}
		default:
			// map / slice / array / struct：从 JSON 列反序列化回原始类型
			if target.rawVal == nil {
				break
			}
			dest := reflect.NewAt(f.GoType, ptr)
			if err := sonic.Unmarshal(target.rawVal, dest.Interface()); err != nil {
				fmt.Printf("[gameorm] unmarshal field %s error: %v\n", f.ColName, err)
			}
		}
	}
}
