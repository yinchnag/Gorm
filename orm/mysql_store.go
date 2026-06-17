package orm

import (
	"context"
	"fmt"
	"math/bits"
	"reflect"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/bytedance/sonic"
)

// pendingItem 代表一条待刷盘到 MySQL 的写入请求。
// 同一 key 的新请求会覆盖老请求，从而合并高频存盘，减少 MySQL 写操作次数。
type pendingItem struct {
	key       string // "{table}:{pk}"，用于去重
	tableName string
	meta      *TableMeta
	snapshot  []any // 字段值快照，与 meta.Fields 一一对应
	deleted   bool  // 是否标记删除
}

// flushQueue 是单个 worker 的待刷盘队列，使用 map 保证同 key 只保留最新快照。
type flushQueue struct {
	mu    sync.Mutex
	items map[string]*pendingItem
}

func newFlushQueue() *flushQueue {
	return &flushQueue{items: make(map[string]*pendingItem, 64)}
}

const warnQueueDepth = 1000

func (q *flushQueue) push(item *pendingItem) {
	q.mu.Lock()
	q.items[item.key] = item // 覆盖旧条目——核心去重逻辑
	depth := len(q.items)
	q.mu.Unlock()
	if depth > warnQueueDepth {
		fmt.Printf("[gameorm] queue depth %d exceeds %d, MySQL may be lagging\n", depth, warnQueueDepth)
	}
}

// pushIfAbsent 仅当 key 不存在时才入队，用于 flush 失败后的重试。
// 若期间已有更新的 Save/Delete 入队（key 已存在），则丢弃旧快照，让新版本胜出，
// 从而在不破坏写入顺序的前提下实现安全重试。
func (q *flushQueue) pushIfAbsent(item *pendingItem) {
	q.mu.Lock()
	if _, exists := q.items[item.key]; !exists {
		q.items[item.key] = item
	}
	q.mu.Unlock()
}

func (q *flushQueue) empty() bool {
	q.mu.Lock()
	e := len(q.items) == 0
	q.mu.Unlock()
	return e
}

func (q *flushQueue) drain() []*pendingItem {
	q.mu.Lock()
	out := make([]*pendingItem, 0, len(q.items))
	for _, v := range q.items {
		out = append(out, v)
	}
	q.items = make(map[string]*pendingItem, 64)
	q.mu.Unlock()
	return out
}

// MySQLStore 管理异步、批量、去重的 MySQL 刷盘。
// 架构：N 个 worker goroutine，每隔 FlushInterval 批量执行 UPSERT/软删除。
// 提交操作：调用 EnqueueSave/EnqueueDelete 仅将快照入队，不阻塞游戏逻辑。
type MySQLStore struct {
	pool      *Pool
	useGlobal bool
	queues    []*flushQueue
	nWorker   int
	stopCh    chan struct{}
	wg        sync.WaitGroup
}

var (
	globalMySQLStore           *MySQLStore
	globalRegionMySQLStore     *MySQLStore
	mysqlStoreOnce             sync.Once
	globalRegionMySQLStoreOnce sync.Once
	frozenMetaCache            sync.Map // key: *TableMeta -> *TableMeta（深拷贝后的只读副本）
)

func freezeTableMeta(meta *TableMeta) *TableMeta {
	if v, ok := frozenMetaCache.Load(meta); ok {
		return v.(*TableMeta)
	}
	cloned := cloneTableMeta(meta)
	actual, _ := frozenMetaCache.LoadOrStore(meta, cloned)
	return actual.(*TableMeta)
}

func cloneTableMeta(meta *TableMeta) *TableMeta {
	clonedFields := make([]*FieldMeta, len(meta.Fields))
	pkIdx := -1

	for i, f := range meta.Fields {
		cf := *f
		clonedFields[i] = &cf
		if f.IsPrimary {
			pkIdx = i
		}
	}

	cloned := &TableMeta{
		TableName: meta.TableName,
		Fields:    clonedFields,
	}
	if pkIdx >= 0 {
		cloned.PrimaryField = clonedFields[pkIdx]
	} else if meta.PrimaryField != nil {
		cpk := *meta.PrimaryField
		cloned.PrimaryField = &cpk
	}
	return cloned
}

// getMySQLStore 返回全局 MySQLStore 单例，首次调用时启动 worker。
func getMySQLStore() *MySQLStore {
	return getMySQLStoreForRoute(false)
}

func getMySQLStoreForRoute(useGlobal bool) *MySQLStore {
	if useGlobal {
		p := GetPool()
		if p.GlobalDB == nil {
			fmt.Printf("[gameorm] global mysql not configured, fallback to default mysql store\n")
			return getMySQLStore()
		}
		globalRegionMySQLStoreOnce.Do(func() {
			n := p.Cfg.WorkerCount
			s := &MySQLStore{
				pool:      p,
				useGlobal: true,
				nWorker:   n,
				queues:    make([]*flushQueue, n),
				stopCh:    make(chan struct{}),
			}
			for i := range n {
				s.queues[i] = newFlushQueue()
			}
			s.start()
			globalRegionMySQLStore = s
		})
		return globalRegionMySQLStore
	}

	mysqlStoreOnce.Do(func() {
		p := GetPool()
		n := p.Cfg.WorkerCount
		s := &MySQLStore{
			pool:      p,
			useGlobal: false,
			nWorker:   n,
			queues:    make([]*flushQueue, n),
			stopCh:    make(chan struct{}),
		}
		for i := range n {
			s.queues[i] = newFlushQueue()
		}
		s.start()
		globalMySQLStore = s
	})
	return globalMySQLStore
}

// start 启动所有 worker goroutine。
func (s *MySQLStore) start() {
	interval := time.Duration(s.pool.Cfg.FlushIntervalMs) * time.Millisecond
	for i := range s.nWorker {
		s.wg.Add(1)
		go s.worker(i, interval)
	}
}

// Stop 优雅停止所有 worker：先关闭信号，再等待最后一次 flush 完成。
func (s *MySQLStore) Stop() {
	close(s.stopCh)
	s.wg.Wait()
}

const maxFlushBackoff = 30 * time.Second

// maxBackoffShift 是指数退避的最大位移量，从 maxFlushBackoff 推导，保证 1<<maxBackoffShift 不超过上限。
// bits.Len64(30)-1 = 4，即最大退避 1<<4 = 16s，两个参数只需维护 maxFlushBackoff 一处。
var maxBackoffShift = bits.Len64(uint64(maxFlushBackoff/time.Second)) - 1

// worker 每隔 interval 触发一次 flush。
// MySQL 连续报错时，用指数退避（1s→2s→4s…上限 30s）减少日志刷屏。
// 停机时循环重试直到队列清空，避免 pushIfAbsent 放回的 item 永久丢失。
func (s *MySQLStore) worker(idx int, interval time.Duration) {
	defer s.wg.Done()

	errStreak := 0
	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			hadError := s.flush(s.queues[idx])
			if hadError {
				errStreak++
				backoff := time.Duration(1<<min(errStreak-1, maxBackoffShift)) * time.Second
				timer.Reset(backoff)
			} else {
				errStreak = 0
				timer.Reset(interval)
			}
		case <-s.stopCh:
			// 循环重试直到队列清空：flush 失败的 item 会经 pushIfAbsent 回到队列，
			// 需要再次 flush，否则 worker 退出后这批数据永久丢失。
			for i := 0; i < 3; i++ {
				s.flush(s.queues[idx])
				if s.queues[idx].empty() {
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
			return
		}
	}
}

// flush 批量执行队列内所有 pendingItem 对应的 SQL。
// 执行失败的条目通过 pushIfAbsent 重新入队，等待下次 tick 重试。
// 若期间已有更新的快照入队（同 key），旧快照被丢弃，新版本优先——写入顺序不受影响。
func (s *MySQLStore) flush(q *flushQueue) (hadError bool) {
	items := q.drain()
	if len(items) == 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, item := range items {
		var err error
		if item.deleted {
			err = s.execSoftDelete(ctx, item)
		} else {
			err = s.execUpsert(ctx, item)
		}
		if err != nil {
			fmt.Printf("[gameorm] flush error table=%s pk=%v: %v, will retry next tick\n",
				item.tableName, item.snapshot[pkIndex(item.meta)], err)
			q.pushIfAbsent(item)
			hadError = true
		}
	}
	return
}

// execUpsert 执行 INSERT ... ON DUPLICATE KEY UPDATE（自动幂等）。
// 每次更新都会将 is_deleted 复位为 0，并刷新 update_time。
func (s *MySQLStore) execUpsert(ctx context.Context, item *pendingItem) error {
	fields := item.meta.Fields
	cols := make([]string, 0, len(fields)+3)
	placeholders := make([]string, 0, len(fields)+3)
	updates := make([]string, 0, len(fields))
	args := make([]any, 0, len(fields)+1)

	for i, f := range fields {
		cols = append(cols, f.ColName)
		placeholders = append(placeholders, "?")
		args = append(args, item.snapshot[i])
		if !f.IsPrimary {
			updates = append(updates, fmt.Sprintf("`%s`=VALUES(`%s`)", f.ColName, f.ColName))
		}
	}

	// 内置系统列值：插入时固定 is_deleted=0，创建时间/更新时间交给数据库当前时间。
	cols = append(cols, "is_deleted", "create_time", "update_time")
	placeholders = append(placeholders, "?", "NOW()", "NOW()")
	args = append(args, 0)

	// 更新时恢复软删除标记并刷新更新时间。
	updates = append(updates, "`is_deleted`=0", "`update_time`=NOW()")

	sql := fmt.Sprintf(
		"INSERT INTO `%s` (`%s`) VALUES (%s) ON DUPLICATE KEY UPDATE %s",
		item.tableName,
		strings.Join(cols, "`,`"),
		strings.Join(placeholders, ","),
		strings.Join(updates, ","),
	)
	_, err := s.pool.SelectMySQL(s.useGlobal).ExecContext(ctx, sql, args...)
	return err
}

// execSoftDelete 通过设置 is_deleted=1 实现软删除，同时刷新 update_time。
func (s *MySQLStore) execSoftDelete(ctx context.Context, item *pendingItem) error {
	pk := item.meta.PrimaryField
	pkVal := item.snapshot[pkIndex(item.meta)]

	sql := fmt.Sprintf(
		"UPDATE `%s` SET `is_deleted`=1, `update_time`=NOW() WHERE `%s`=? AND `is_deleted`=0",
		item.tableName, pk.ColName,
	)
	_, err := s.pool.SelectMySQL(s.useGlobal).ExecContext(ctx, sql, pkVal)
	return err
}

// EnqueueSave 将对象快照入队，不阻塞调用方。
// workerIdx = hash(pk) % nWorker，保证同一对象始终进同一队列（顺序保证）。
func (s *MySQLStore) EnqueueSave(tableName string, meta *TableMeta, base unsafe.Pointer) {
	frozenMeta := freezeTableMeta(meta)
	snap := snapshotFields(frozenMeta, base)
	pk := snap[pkIndex(frozenMeta)]
	key := fmt.Sprintf("%s:%v", tableName, pk)
	idx := hashKey(key) % uint64(s.nWorker)
	s.queues[idx].push(&pendingItem{
		key:       key,
		tableName: tableName,
		meta:      frozenMeta,
		snapshot:  snap,
	})
}

// EnqueueDelete 将软删除请求入队。
func (s *MySQLStore) EnqueueDelete(tableName string, meta *TableMeta, base unsafe.Pointer) {
	frozenMeta := freezeTableMeta(meta)
	snap := snapshotFields(frozenMeta, base)
	pk := snap[pkIndex(frozenMeta)]
	key := fmt.Sprintf("%s:%v", tableName, pk)
	idx := hashKey(key) % uint64(s.nWorker)
	s.queues[idx].push(&pendingItem{
		key:       key,
		tableName: tableName,
		meta:      frozenMeta,
		snapshot:  snap,
		deleted:   true,
	})
}

// snapshotFields 将对象当前字段值全量快照为 []any，避免后续对象被修改导致存档错乱。
func snapshotFields(meta *TableMeta, base unsafe.Pointer) []any {
	snap := make([]any, len(meta.Fields))
	for i, f := range meta.Fields {
		ptr := FieldPtr(base, f.Offset)
		snap[i] = readFieldValue(f, ptr)
	}
	return snap
}

// readFieldValue 通过 unsafe 指针读取字段值，返回适合 MySQL driver 的类型。
// 基本类型通过指针直接转型（零开销）；map/slice/array/struct 等复杂类型
// 用 sonic 序列化为 JSON 字符串，存入 JSON 列。
func readFieldValue(f *FieldMeta, ptr unsafe.Pointer) any {
	switch f.GoType.Kind() {
	case reflect.Int64:
		return *(*int64)(ptr)
	case reflect.Int32:
		return *(*int32)(ptr)
	case reflect.Int:
		return *(*int)(ptr)
	case reflect.Int8:
		return *(*int8)(ptr)
	case reflect.Int16:
		return *(*int16)(ptr)
	case reflect.Uint64:
		return *(*uint64)(ptr)
	case reflect.Uint32:
		return *(*uint32)(ptr)
	case reflect.Uint:
		return *(*uint)(ptr)
	case reflect.Float32:
		return *(*float32)(ptr)
	case reflect.Float64:
		return *(*float64)(ptr)
	case reflect.String:
		return *(*string)(ptr)
	case reflect.Bool:
		return *(*bool)(ptr)
	default:
		// map / slice / array / struct → JSON 字符串存入 JSON 列
		v := reflect.NewAt(f.GoType, ptr).Elem().Interface()
		data, err := sonic.Marshal(v)
		if err != nil {
			fmt.Printf("[gameorm] marshal field %s error: %v\n", f.ColName, err)
			return nil
		}
		return string(data)
	}
}

// pkIndex 返回主键字段在 meta.Fields 中的下标。
func pkIndex(meta *TableMeta) int {
	for i, f := range meta.Fields {
		if f.IsPrimary {
			return i
		}
	}
	return 0
}

// Shutdown 等待所有异步 MySQL 写操作完成后停止 worker，适用于进程优雅退出场景。
// 若 worker 从未启动（未调用过 Save/Delete），此函数为空操作。
// 调用后 MySQLStore 停止，不可再次提交写入请求。
func Shutdown() {
	if globalMySQLStore != nil {
		globalMySQLStore.Stop()
	}
	if globalRegionMySQLStore != nil {
		globalRegionMySQLStore.Stop()
	}
}

// hashKey 使用 FNV-1a 对 key 做轻量哈希，用于分派 worker。
func hashKey(s string) uint64 {
	const offset64 uint64 = 14695981039346656037
	const prime64 uint64 = 1099511628211
	h := offset64
	for i := range len(s) {
		h ^= uint64(s[i])
		h *= prime64
	}
	return h
}
