package config

import (
	"os"

	"github.com/bytedance/sonic"
)

// DBConfig MySQL 连接配置
type DBConfig struct {
	DSN             string `json:"dsn"`               // user:pass@tcp(host:port)/dbname?charset=utf8mb4&parseTime=True
	MaxOpenConns    int    `json:"max_open_conns"`    // 最大打开连接数，默认 100
	MaxIdleConns    int    `json:"max_idle_conns"`    // 最大空闲连接数，默认 20
	ConnMaxLifetime int    `json:"conn_max_lifetime"` // 连接最大存活秒数，默认 3600
}

// RedisConfig Redis 连接配置
type RedisConfig struct {
	Addr         string `json:"addr"`          // host:port
	Password     string `json:"password"`      // 无密码留空
	DB           int    `json:"db"`            // 数据库编号，默认 0
	PoolSize     int    `json:"pool_size"`      // 连接池大小，默认 50
	MinIdleConns int    `json:"min_idle_conns"` // 最小空闲连接数，默认 10
	KeyTTLSec    int    `json:"key_ttl_sec"`    // Redis key 默认 TTL（秒），默认 7200
}

// ORMConfig 整体 ORM 配置
type ORMConfig struct {
	MySQL           DBConfig     `json:"mysql"`
	Redis           RedisConfig  `json:"redis"`
	GlobalMySQL     *DBConfig    `json:"global_mysql,omitempty"`
	GlobalRedis     *RedisConfig `json:"global_redis,omitempty"`
	FlushIntervalMs int        `json:"flush_interval_ms"` // 批量刷盘间隔（毫秒），默认 500
	WorkerCount     int        `json:"worker_count"`       // 异步刷盘 goroutine 数，默认 4
}

// DefaultORMConfig 返回合理的默认值；未配置字段将被填充。
func DefaultORMConfig() *ORMConfig {
	return &ORMConfig{
		MySQL: DBConfig{
			MaxOpenConns:    100,
			MaxIdleConns:    20,
			ConnMaxLifetime: 3600,
		},
		Redis: RedisConfig{
			PoolSize:     50,
			MinIdleConns: 10,
			KeyTTLSec:    7200,
		},
		FlushIntervalMs: 500,
		WorkerCount:     4,
	}
}

// LoadFromFile 从 JSON 文件加载配置；未配置字段保留默认值。
func LoadFromFile(path string) (*ORMConfig, error) {
	cfg := DefaultORMConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err = sonic.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	applyDefaults(cfg)
	return cfg, nil
}

func applyDefaults(c *ORMConfig) {
	applyDBDefaults(&c.MySQL)
	applyRedisDefaults(&c.Redis)

	if c.GlobalMySQL != nil {
		applyDBDefaults(c.GlobalMySQL)
	}
	if c.GlobalRedis != nil {
		applyRedisDefaults(c.GlobalRedis)
	}

	if c.FlushIntervalMs <= 0 {
		c.FlushIntervalMs = 500
	}
	if c.WorkerCount <= 0 {
		c.WorkerCount = 4
	}
}

func applyDBDefaults(c *DBConfig) {
	if c.MaxOpenConns <= 0 {
		c.MaxOpenConns = 100
	}
	if c.MaxIdleConns <= 0 {
		c.MaxIdleConns = 20
	}
	if c.ConnMaxLifetime <= 0 {
		c.ConnMaxLifetime = 3600
	}
}

func applyRedisDefaults(c *RedisConfig) {
	if c.PoolSize <= 0 {
		c.PoolSize = 50
	}
	if c.MinIdleConns <= 0 {
		c.MinIdleConns = 10
	}
	if c.KeyTTLSec <= 0 {
		c.KeyTTLSec = 7200
	}
}
