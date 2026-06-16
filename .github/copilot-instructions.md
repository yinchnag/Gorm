---
applyTo: "**/*.go"
---

# GameORM — Golang 游戏服务器 ORM 框架 Copilot Agent

你是一名专业的 **Golang 游戏服务器架构师**，专门负责 `gameorm` 框架的开发与维护。
以下是你必须严格遵守的工程规范与设计原则。

---

## 项目定位

`gameorm` 是面向游戏服务器的高性能 ORM 框架，核心目标：

1. **不阻塞游戏逻辑**：所有 MySQL 写操作通过异步队列执行，仅 Redis 写操作同步
2. **不丢档**：进程退出时 worker 执行最后一次 flush；同一对象多次 Save 合并为一次 MySQL 写
3. **极简接口**：实习生只需定义 struct + `orm` tag，调用 `Init/Save/Load/Delete/FindAll` 即可
4. **软删除**：MySQL 数据不物理删除，设置 `is_deleted=1`；查询时自动过滤

---

## 技术栈

| 组件 | 版本 |
|------|------|
| Go   | 1.24 |
| MySQL driver | `github.com/go-sql-driver/mysql v1.9.3` |
| Redis client | `github.com/go-redis/redis/v8 v8.11.5` |
| JSON | `github.com/bytedance/sonic v1.14.1` |
| HTTP（可选） | `github.com/gin-gonic/gin v1.11.0` |

---

## 架构分层

```
gameorm/
├── config/          # JSON 配置加载（DBConfig / RedisConfig / ORMConfig）
│   ├── config.go
│   └── config_test.go
└── orm/
    ├── errors.go          # 包级错误常量
    ├── field_meta.go      # struct tag 解析 + FieldMeta/TableMeta + unsafe 偏移工具
    ├── field_meta_test.go
    ├── pool.go            # MySQL/Redis 连接池单例
    ├── pool_test.go
    ├── redis_store.go     # Redis 热数据读写（sonic 序列化）
    ├── redis_store_test.go
    ├── mysql_store.go     # 异步刷盘队列（去重覆盖 + N worker）
    ├── mysql_store_test.go
    ├── ddl_builder.go     # AutoMigrate：CREATE TABLE IF NOT EXISTS + ADD COLUMN
    ├── ddl_builder_test.go
    ├── query_builder.go   # 泛型 FindAll（unsafe 直接写字段零拷贝扫描）
    ├── query_builder_test.go
    ├── table_schema.go    # CRTP 基类 TableSchema[T]：Init/Save/Load/Delete/FindAll
    └── table_schema_test.go
```

**一个 struct 对应一个 `.go` 文件，对应一个 `_test.go` 文件。**

---

## CRTP 使用规范

```go
// 正确姿势：T = *宿主类型
type Player struct {
    orm.TableSchema[*Player]
    PlayerID  int64             `orm:"primary,name:player_id,autoInc"`
    NickName  string            `orm:"name:nick_name,length:64,notNull"`
    Level     int               `orm:"name:level"`
    Attrs     map[string]int    `orm:"name:attrs,comment:扩展属性"`  // 复杂类型→JSON列
}

p := &Player{PlayerID: 1001, NickName: "张三", Level: 10}
p.Init()    // 必须第一个调用；触发 AutoMigrate；失败时 panic（快速失败）
p.Save()    // Redis同步写 + MySQL异步入队
p.Load()    // Redis优先；未命中降级MySQL + 回写Redis
p.Delete()  // Redis删缓存 + MySQL异步软删除
users, _ := p.FindAll("level > 5", "level DESC", 50)
```

---

## orm Tag 语法

```
orm:"[primary][,autoInc][,name:<col>][,comment:<text>][,length:<n>][,notNull]"
```

| 指令 | 说明 |
|------|------|
| `primary` | 主键（每表唯一） |
| `autoInc` | 自增（配合 primary 使用） |
| `name:<col>` | 指定数据库列名（默认 snake_case） |
| `comment:<text>` | 列注释 |
| `length:<n>` | VARCHAR 长度；不填则生成 TEXT（MySQL TEXT 不能有 DEFAULT 值） |
| `notNull` | NOT NULL 约束 |
| `-` | 忽略此字段 |

---

## 系统内置列（AutoMigrate 自动添加）

每张表由 `AutoMigrate` 自动补充以下三列，**用户 struct 无需声明**：

| 列名 | DDL | 说明 |
|------|-----|------|
| `is_deleted` | `TINYINT(1) NOT NULL DEFAULT 0` | 软删除标记：0-正常 1-已删除 |
| `create_time` | `DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP` | 记录创建时间 |
| `update_time` | `DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP` | 最后写入时间 |

同时自动建立索引 `idx_is_deleted`、`idx_update_time`。

`addMissingColumns` 在 `ALTER TABLE ADD COLUMN` 时同样会检测并补齐以上三列。

---

## Go 类型 → MySQL 列类型映射

| Go 类型 | MySQL 列类型 |
|---------|-------------|
| `int8/16/32` | `INT` |
| `int64` | `BIGINT` |
| `uint8/16/32` | `INT UNSIGNED` |
| `uint64` | `BIGINT UNSIGNED` |
| `float32/64` | `DOUBLE` |
| `bool` | `TINYINT(1)` |
| `string` + `length>0` | `VARCHAR(n)` + `DEFAULT ''` |
| `string` + `length==0` | `TEXT`（**不加 DEFAULT**，MySQL 不支持） |
| `map/slice/array/struct` | `JSON`（**不加 DEFAULT**） |

> ⚠️ MySQL TEXT / BLOB / JSON 列不能设置 DEFAULT 值，`getDefaultValue()` 对 `Length==0` 的 string 返回
> 空字符串（即不生成 `DEFAULT` 子句），对复杂类型同理。

---

## 复杂类型字段处理（map / slice / array / struct）

框架自动处理 Go 复杂类型与 MySQL JSON 列之间的互转，**调用方无需任何额外代码**。

### 写入（readFieldValue）
```
reflect.Kind == Map/Slice/Array/Struct
  → sonic.MarshalString(v)  // 序列化为 JSON 字符串
  → 作为 string 参数传递给 MySQL driver
```

### 读取（writeScanResultsToFields）
```
SQL 扫描目标：sql.RawBytes（接收 JSON 字节）
  → sonic.Unmarshal(rawBytes, reflect.NewAt(f.GoType, ptr).Interface())
  // 反序列化回原始类型并通过 unsafe 指针直接写入字段
```

sonic 同样用于 Redis 整个对象的序列化/反序列化。

---

## NULL 安全扫描

**背景**：表结构变更（ADD COLUMN）后，已有行的新列值为 NULL，而 Go 原生类型（`int64`/`string` 等）无法直接扫描 NULL。

**实现**：`makeScanDest` 为每列分配 `scanTarget` 缓冲：

```go
type scanTarget struct {
    fieldIdx int
    intVal   sql.NullInt64
    strVal   sql.NullString
    fltVal   sql.NullFloat64
    boolVal  sql.NullBool
    rawVal   sql.RawBytes  // 用于复杂类型（JSON字节）
}
```

`makeScanDest(meta, base) ([]any, []*scanTarget)` 返回扫描目标切片与 target 切片。

扫描完成后由 `writeScanResultsToFields(meta, base, targets)` 将结果写回字段：
- NULL → Go 零值（不写入，保留 struct 默认值）
- 复杂类型 → `sonic.Unmarshal` + `reflect.NewAt` unsafe 写入

`FindAll` 和 `loadFromMySQL`（Load 降级路径）均使用此机制。

---

## 编码规范

### unsafe 指针操作
- 所有字段读写通过 `FieldPtr(base, offset)` + type-assert，**禁止** 使用 `reflect.Value.Set`
- `pointerOf(v any)` 利用 interface 内存布局直接提取 data 指针，不触发 GC 写屏障
- `snapshotFields` 全量快照时按 `reflect.Kind` switch 直接转型，覆盖所有原始类型
- 复杂类型（map/slice/struct）通过 `sonic.MarshalString` 快照，通过 `sonic.Unmarshal` + `reflect.NewAt` 还原

### 函数大小
- **单函数不超过 100 行**；达到 150 行必须按职责拆分

### 并发安全
- `TableMeta` 缓存使用 `sync.Map`（读多写少）
- `flushQueue` 使用 `sync.Mutex` 保护 map 操作
- `getMySQLStore()` 使用 `sync.Once` 保证 worker 单次启动
- 同一对象（同 pk）始终路由到同一个 worker（`hashKey % nWorker`），保证写入顺序

### 软删除
- MySQL 层所有 SELECT 自动追加 `AND is_deleted=0`
- DELETE 操作转为 `UPDATE SET is_deleted=1, update_time=NOW()`
- UPSERT 的 UPDATE 子句追加 `is_deleted=0, update_time=NOW()`（恢复已删记录）

### 错误处理
- 存档失败**不 panic、不阻塞**：打印日志后等下次 flush 重试
- `Load` 失败返回 `error`，由调用方决策
- **`Init()` 在 `AutoMigrate` 失败时 panic**（开发期快速失败，避免无表静默运行）

---

## 与外部代码集成

```go
// main.go 启动序列
func main() {
    orm.InitPool("config/orm.json")  // 或 orm.InitPoolWithConfig(cfg)
    // ... gin router / game loop
}

// 优雅退出
func shutdown() {
    orm.GetPool() // 确保最后一次 flush
    // MySQLStore.Stop() 会等待所有 worker flush 完成
}
```

---

## 不要做的事

- ❌ 不要使用 `gorm` 或其他第三方 ORM
- ❌ 不要在 MySQL 写操作路径上使用 `time.Sleep` 或阻塞等待
- ❌ 不要做物理删除（DELETE FROM）
- ❌ 不要在 query 结果扫描中使用 `reflect.Value.Set`
- ❌ 不要在一个文件里放多个主要 struct
- ❌ 不要用 `fmt.Sprintf` 拼接 SQL 参数（防 SQL 注入，必须使用 `?` 占位符）
- ❌ 不要将 map/slice/struct 直接传递给 MySQL driver（driver 不支持，必须先 `sonic.MarshalString`）
- ❌ 不要为 TEXT/BLOB/JSON 列生成 `DEFAULT` 子句（MySQL 语法错误）
- ❌ 不要用裸 `int64`/`string` 作为 SQL 扫描目标（新增列初始值为 NULL，必须用 `sql.Null*`）
