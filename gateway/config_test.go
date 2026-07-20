package gateway

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, dir, contents string) string {
	t.Helper()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigValidMinimal(t *testing.T) {
	t.Setenv("TEST_PROVIDER_KEY", "sk-test-123")
	dir := t.TempDir()
	path := writeConfig(t, dir, `{
		"listen": "127.0.0.1:8090",
		"audit": {"path": "audit.jsonl"},
		"providers": [
			{"name": "primary", "base_url": "https://api.example.com/v1", "api_key_env": "TEST_PROVIDER_KEY",
			 "pricing": {"prompt_per_1m_usd": 0.05, "completion_per_1m_usd": 0.08}}
		]
	}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Listen != "127.0.0.1:8090" {
		t.Errorf("listen = %q", cfg.Listen)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].Name != "primary" {
		t.Errorf("providers = %+v", cfg.Providers)
	}
	if got := cfg.Timeout.Seconds(); got != DefaultTimeoutSeconds {
		t.Errorf("default timeout = %d, want %d", got, DefaultTimeoutSeconds)
	}
}

func TestLoadConfigRejectsUnknownFields(t *testing.T) {
	t.Setenv("TEST_PROVIDER_KEY", "sk-test-123")
	dir := t.TempDir()
	path := writeConfig(t, dir, `{
		"listen": "127.0.0.1:8090",
		"audit": {"path": "audit.jsonl"},
		"providers": [{"name": "p", "base_url": "https://x", "api_key_env": "TEST_PROVIDER_KEY"}],
		"totally_made_up_field": true
	}`)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected an error for an unknown field, got nil")
	}
}

func TestLoadConfigRejectsMissingAPIKeyEnvVar(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `{
		"listen": "127.0.0.1:8090",
		"audit": {"path": "audit.jsonl"},
		"providers": [{"name": "p", "base_url": "https://x", "api_key_env": "DEFINITELY_NOT_SET_XYZ123"}]
	}`)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected an error for an unset api_key_env, got nil")
	}
}

func TestLoadConfigRejectsDuplicateProviderNames(t *testing.T) {
	t.Setenv("TEST_PROVIDER_KEY", "sk-test-123")
	dir := t.TempDir()
	path := writeConfig(t, dir, `{
		"listen": "127.0.0.1:8090",
		"audit": {"path": "audit.jsonl"},
		"providers": [
			{"name": "dup", "base_url": "https://a", "api_key_env": "TEST_PROVIDER_KEY"},
			{"name": "dup", "base_url": "https://b", "api_key_env": "TEST_PROVIDER_KEY"}
		]
	}`)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected an error for duplicate provider names, got nil")
	}
}

func TestLoadConfigRejectsNoProviders(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `{
		"listen": "127.0.0.1:8090",
		"audit": {"path": "audit.jsonl"},
		"providers": []
	}`)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected an error for zero providers, got nil")
	}
}

func TestLoadConfigRejectsNegativePricing(t *testing.T) {
	t.Setenv("TEST_PROVIDER_KEY", "sk-test-123")
	dir := t.TempDir()
	path := writeConfig(t, dir, `{
		"listen": "127.0.0.1:8090",
		"audit": {"path": "audit.jsonl"},
		"providers": [{"name": "p", "base_url": "https://x", "api_key_env": "TEST_PROVIDER_KEY",
			"pricing": {"prompt_per_1m_usd": -1, "completion_per_1m_usd": 0}}]
	}`)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected an error for negative pricing, got nil")
	}
}

func TestLoadConfigCustomTimeoutHonored(t *testing.T) {
	t.Setenv("TEST_PROVIDER_KEY", "sk-test-123")
	dir := t.TempDir()
	path := writeConfig(t, dir, `{
		"listen": "127.0.0.1:8090",
		"audit": {"path": "audit.jsonl"},
		"timeout_seconds": 5,
		"providers": [{"name": "p", "base_url": "https://x", "api_key_env": "TEST_PROVIDER_KEY"}]
	}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.Timeout.Seconds(); got != 5 {
		t.Errorf("timeout = %d, want 5", got)
	}
}

func TestLoadConfigRejectsZeroTimeoutWhenExplicit(t *testing.T) {
	t.Setenv("TEST_PROVIDER_KEY", "sk-test-123")
	dir := t.TempDir()
	path := writeConfig(t, dir, `{
		"listen": "127.0.0.1:8090",
		"audit": {"path": "audit.jsonl"},
		"timeout_seconds": 0,
		"providers": [{"name": "p", "base_url": "https://x", "api_key_env": "TEST_PROVIDER_KEY"}]
	}`)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected an error for explicit timeout_seconds: 0, got nil")
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	if _, err := LoadConfig("/nonexistent/path/config.json"); err == nil {
		t.Fatal("expected an error for a missing file, got nil")
	}
}
