package config

import (
	"os"
	"testing"
)

func TestLoadConfigDefaults(t *testing.T) {
	// 清理环境变量保证默认值覆盖
	os.Unsetenv("PORT")
	os.Unsetenv("LITELLM_PORT")
	os.Unsetenv("DEFAULT_USD_TO_CNY_RATE")

	cfg := LoadConfig()

	if cfg.Port != "4000" {
		t.Errorf("expected default port 4000, got %s", cfg.Port)
	}
	if cfg.DefaultUSDtoCNY != 7.2300 {
		t.Errorf("expected default fx rate 7.23, got %f", cfg.DefaultUSDtoCNY)
	}
	if cfg.MySQLDatabase != "litellm_db" {
		t.Errorf("expected default db litellm_db, got %s", cfg.MySQLDatabase)
	}
}

func TestLoadConfigEnvOverrides(t *testing.T) {
	os.Setenv("PORT", "8888")
	os.Setenv("DEFAULT_USD_TO_CNY_RATE", "7.3500")
	defer func() {
		os.Unsetenv("PORT")
		os.Unsetenv("DEFAULT_USD_TO_CNY_RATE")
	}()

	cfg := LoadConfig()
	if cfg.Port != "8888" {
		t.Errorf("expected port 8888, got %s", cfg.Port)
	}
	if cfg.DefaultUSDtoCNY != 7.3500 {
		t.Errorf("expected fx rate 7.35, got %f", cfg.DefaultUSDtoCNY)
	}
}
