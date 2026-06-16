package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromFile(t *testing.T) {
	raw := `{
		"mysql": {"dsn":"root:pwd@tcp(127.0.0.1:3306)/game","max_open_conns":50},
		"redis": {"addr":"127.0.0.1:6379","key_ttl_sec":3600},
		"flush_interval_ms": 200,
		"worker_count": 8
	}`
	tmp := filepath.Join(t.TempDir(), "orm.json")
	if err := os.WriteFile(tmp, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFromFile(tmp)
	if err != nil {
		t.Fatalf("LoadFromFile error: %v", err)
	}
	if cfg.MySQL.MaxOpenConns != 50 {
		t.Errorf("expected 50, got %d", cfg.MySQL.MaxOpenConns)
	}
	// 未配置字段应使用默认值
	if cfg.MySQL.MaxIdleConns != 20 {
		t.Errorf("expected default 20, got %d", cfg.MySQL.MaxIdleConns)
	}
	if cfg.WorkerCount != 8 {
		t.Errorf("expected 8, got %d", cfg.WorkerCount)
	}
}

func TestDefaultORMConfig(t *testing.T) {
	cfg := DefaultORMConfig()
	if cfg.FlushIntervalMs != 500 {
		t.Errorf("expected 500, got %d", cfg.FlushIntervalMs)
	}
}

func TestLoadFromFile_WithGlobalStorage(t *testing.T) {
	raw := `{
		"mysql": {"dsn":"root:pwd@tcp(127.0.0.1:3306)/game"},
		"redis": {"addr":"127.0.0.1:6379"},
		"global_mysql": {"dsn":"root:pwd@tcp(127.0.0.1:3307)/game_global","max_open_conns":64},
		"global_redis": {"addr":"127.0.0.1:6380"}
	}`
	tmp := filepath.Join(t.TempDir(), "orm_global.json")
	if err := os.WriteFile(tmp, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFromFile(tmp)
	if err != nil {
		t.Fatalf("LoadFromFile error: %v", err)
	}
	if cfg.GlobalMySQL == nil || cfg.GlobalRedis == nil {
		t.Fatal("expected global_mysql and global_redis to be loaded")
	}
	if cfg.GlobalMySQL.MaxOpenConns != 64 {
		t.Errorf("expected global max_open_conns=64, got %d", cfg.GlobalMySQL.MaxOpenConns)
	}
	if cfg.GlobalRedis.KeyTTLSec != 7200 {
		t.Errorf("expected global redis default key_ttl_sec=7200, got %d", cfg.GlobalRedis.KeyTTLSec)
	}
}
