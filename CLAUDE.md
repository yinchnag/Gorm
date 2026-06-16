# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

`gameorm` is a high-performance ORM framework for Go game servers. Its three core guarantees:
1. **Non-blocking game logic** — all MySQL writes go through async worker queues; only Redis writes are synchronous
2. **No data loss** — workers do a final flush on process exit; multiple `Save()` calls on the same object are deduplicated into one MySQL write
3. **Soft deletes only** — `DELETE` becomes `UPDATE SET is_deleted=1`; all SELECTs auto-filter with `AND is_deleted=0`

## Commands

```bash
# Run all tests
go test ./...

# Run tests for a single package
go test ./orm -v
go test ./config -v

# Run a single test
go test ./orm -run TestTableSchema -v

# Run the example (requires MySQL + Redis + config/orm.json)
go run ./example/main.go

# Build
go build ./...
```

## Architecture

### Two-tier storage

Every object lives in two places simultaneously:
- **Redis** (hot): synchronous reads and writes via `redis_store.go`
- **MySQL** (persistent): asynchronous writes via a worker queue in `mysql_store.go`

`Load()` reads Redis first; on a cache miss it falls back to MySQL and back-fills Redis. `Save()` writes Redis synchronously and enqueues a MySQL upsert. `Delete()` removes the Redis key and enqueues a soft-delete.

### CRTP base class (`orm/table_schema.go`)

All data models embed `orm.TableSchema[*Self]`. The generic parameter gives the base struct a typed self-pointer without virtual dispatch:

```go
type Player struct {
    orm.TableSchema[*Player]
    PlayerID int64  `orm:"primary,name:player_id,autoInc"`
    NickName string `orm:"name:nick_name,length:64,notNull"`
    Level    int    `orm:"name:level"`
    Attrs    map[string]int `orm:"name:attrs"`  // stored as JSON column
}

p := &Player{PlayerID: 1001}
p.Init()   // required first call; runs AutoMigrate; panics on failure
p.Save()
p.Load()
p.Delete()
users, _ := p.FindAll("level > 5", "level DESC", 50)
```

`Init()` is the only call that panics — intentionally, for fast failure at startup if the schema can't be created.

### orm tag syntax

```
orm:"[primary][,autoInc][,name:<col>][,comment:<text>][,length:<n>][,notNull]"
```

Use `-` to exclude a field. The `length` directive determines the MySQL column type: `string` with `length>0` → `VARCHAR(n)`; `string` without `length` → `TEXT` (no DEFAULT allowed by MySQL). Complex types (`map`/`slice`/`struct`) → `JSON` column (also no DEFAULT).

### Auto-added system columns

`AutoMigrate` (inside `ddl_builder.go`) always adds these three columns — user structs must not declare them:

| Column | DDL |
|--------|-----|
| `is_deleted` | `TINYINT(1) NOT NULL DEFAULT 0` |
| `create_time` | `DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP` |
| `update_time` | `DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP` |

### Async MySQL worker pool (`orm/mysql_store.go`)

`flushQueue` holds a `map[string]*pendingItem` keyed by `"{table}:{pk}"`. The same PK always hashes to the same worker, preserving per-object write order. A new `Save()` overwrites an already-queued save — deduplication happens automatically. Workers flush on a configurable ticker (default 500 ms) and do one final flush on `Stop()`.

Upsert SQL is `INSERT ... ON DUPLICATE KEY UPDATE` — it also resets `is_deleted=0` on conflict, so a re-saved soft-deleted record is restored.

### Unsafe pointer operations (`orm/field_meta.go`)

Fields are read and written via `FieldPtr(base unsafe.Pointer, offset uintptr)` — never via `reflect.Value.Set`. Metadata (offsets, types, column names) is parsed once from struct tags and cached in a `sync.Map` keyed by `reflect.Type`.

### NULL-safe scanning (`orm/query_builder.go`)

Rows are scanned into `scanTarget` structs containing `sql.NullInt64`, `sql.NullString`, etc. After scanning, `writeScanResultsToFields` copies values back into struct fields via unsafe offsets. NULL → Go zero value (field is left at its struct default). Complex types use `sql.RawBytes` and are deserialized with `sonic.Unmarshal`.

### Multi-region routing

If a struct contains a `bool` field named `Global`, the framework routes that object's reads/writes to the optional `GlobalDB`/`GlobalRedis` configured in `ORMConfig`. Both the default and global stores run independent worker pools.

### Configuration (`config/config.go`)

Loaded from a JSON file via `orm.InitPool("path/to/orm.json")` or `orm.InitPoolWithConfig(cfg)`. Key fields: `flush_interval_ms` (default 500), `worker_count` (default 4), Redis `key_ttl_sec` (default 7200).

## Hard Rules

- Never use `gorm` or any other third-party ORM
- Never do physical deletes (`DELETE FROM`) — always soft-delete
- Never use `reflect.Value.Set` in scan paths — use `unsafe` pointer writes
- Never use `fmt.Sprintf` for SQL parameters — use `?` placeholders
- Never add `DEFAULT` to `TEXT`/`JSON` columns in DDL — MySQL rejects it
- Never scan into bare `int64`/`string` — new columns start as NULL; use `sql.Null*`
- Never serialize `map`/`slice`/`struct` directly to the MySQL driver — call `sonic.MarshalString` first
- One major struct per `.go` file; keep functions under 100 lines (hard refactor at 150)