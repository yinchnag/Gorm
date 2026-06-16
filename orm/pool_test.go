package orm

import (
	"testing"

	"github.com/norm/config"
)

func TestInitPoolWithConfig_BadDSN(t *testing.T) {
	// sql.Open 对 mysql driver 是懒连接，不会立刻报错；但不应 panic
	cfg := config.DefaultORMConfig()
	cfg.MySQL.DSN = "root:@tcp(127.0.0.1:63306)/test"
	cfg.Redis.Addr = "127.0.0.1:63379"
	if err := InitPoolWithConfig(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p := GetPool()
	if p == nil {
		t.Error("pool should not be nil")
	}
}
