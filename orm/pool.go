package orm

import (
	"database/sql"
	"fmt"
	"time"

	goredis "github.com/go-redis/redis/v8"
	_ "github.com/go-sql-driver/mysql"
	"github.com/norm/config"
)

// Pool 持有 MySQL 和 Redis 连接池的全局单例，由 InitPool 初始化。
type Pool struct {
	DB          *sql.DB
	Redis       *goredis.Client
	GlobalDB    *sql.DB
	GlobalRedis *goredis.Client
	Cfg         *config.ORMConfig
}

var globalPool *Pool

// InitPool 通过配置文件路径初始化全局连接池；应在进程启动时调用一次。
func InitPool(cfgPath string) error {
	cfg, err := config.LoadFromFile(cfgPath)
	if err != nil {
		return fmt.Errorf("gameorm: load config: %w", err)
	}
	return InitPoolWithConfig(cfg)
}

// InitPoolWithConfig 使用已加载的 *config.ORMConfig 初始化连接池，便于测试注入。
func InitPoolWithConfig(cfg *config.ORMConfig) error {
	db, err := openMySQL(&cfg.MySQL)
	if err != nil {
		return fmt.Errorf("gameorm: open mysql: %w", err)
	}
	rdb := openRedis(&cfg.Redis)

	var globalDB *sql.DB
	if cfg.GlobalMySQL != nil {
		globalDB, err = openMySQL(cfg.GlobalMySQL)
		if err != nil {
			return fmt.Errorf("gameorm: open global mysql: %w", err)
		}
	}

	var globalRedis *goredis.Client
	if cfg.GlobalRedis != nil {
		globalRedis = openRedis(cfg.GlobalRedis)
	}

	globalPool = &Pool{
		DB:          db,
		Redis:       rdb,
		GlobalDB:    globalDB,
		GlobalRedis: globalRedis,
		Cfg:         cfg,
	}
	return nil
}

func openMySQL(cfg *config.DBConfig) (*sql.DB, error) {
	db, err := sql.Open("mysql", cfg.DSN)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetime) * time.Second)
	return db, nil
}

func openRedis(cfg *config.RedisConfig) *goredis.Client {
	return goredis.NewClient(&goredis.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password,
		DB:           cfg.DB,
		PoolSize:     cfg.PoolSize,
		MinIdleConns: cfg.MinIdleConns,
	})
}

func (p *Pool) SelectMySQL(useGlobal bool) *sql.DB {
	if useGlobal && p.GlobalDB != nil {
		return p.GlobalDB
	}
	return p.DB
}

func (p *Pool) SelectRedis(useGlobal bool) *goredis.Client {
	if useGlobal && p.GlobalRedis != nil {
		return p.GlobalRedis
	}
	return p.Redis
}

func (p *Pool) SelectRedisConfig(useGlobal bool) *config.RedisConfig {
	if useGlobal && p.Cfg != nil && p.Cfg.GlobalRedis != nil {
		return p.Cfg.GlobalRedis
	}
	return &p.Cfg.Redis
}

// GetPool 返回全局连接池，未初始化时 panic（开发期快速失败）。
func GetPool() *Pool {
	if globalPool == nil {
		panic("gameorm: pool not initialized, call InitPool first")
	}
	return globalPool
}
