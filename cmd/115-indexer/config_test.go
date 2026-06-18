package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveProxyConfigPrefersFlagsOverEnvAndDotEnv(t *testing.T) {
	t.Setenv("FIVE_PROXY_KEY", "env-key")
	t.Setenv("FIVE_PROXY_PASSWORD", "env-password")

	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("FIVE_PROXY_KEY=dot-key\nFIVE_PROXY_PASSWORD=dot-password\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	cfg, err := resolveProxyConfig("flag-key", "flag-password", envPath)
	if err != nil {
		t.Fatalf("resolve proxy config: %v", err)
	}
	if cfg.Key != "flag-key" || cfg.Password != "flag-password" {
		t.Fatalf("resolved cfg = %#v", cfg)
	}
}

func TestResolveProxyConfigFallsBackToEnvBeforeDotEnv(t *testing.T) {
	t.Setenv("FIVE_PROXY_KEY", "env-key")
	t.Setenv("FIVE_PROXY_PASSWORD", "env-password")

	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("FIVE_PROXY_KEY=dot-key\nFIVE_PROXY_PASSWORD=dot-password\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	cfg, err := resolveProxyConfig("", "", envPath)
	if err != nil {
		t.Fatalf("resolve proxy config: %v", err)
	}
	if cfg.Key != "env-key" || cfg.Password != "env-password" {
		t.Fatalf("resolved cfg = %#v", cfg)
	}
}

func TestResolveProxyConfigFallsBackToDotEnv(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("FIVE_PROXY_KEY=dot-key\nFIVE_PROXY_PASSWORD=dot-password\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	cfg, err := resolveProxyConfig("", "", envPath)
	if err != nil {
		t.Fatalf("resolve proxy config: %v", err)
	}
	if cfg.Key != "dot-key" || cfg.Password != "dot-password" {
		t.Fatalf("resolved cfg = %#v", cfg)
	}
}

func TestResolveProxyConfigRequiresBothValues(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("FIVE_PROXY_KEY=dot-key\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	_, err := resolveProxyConfig("", "", envPath)
	if err == nil {
		t.Fatal("expected missing proxy password to fail")
	}
}

func TestNeedsProxyForMode(t *testing.T) {
	for _, mode := range []string{"crawl", "run-scheduler-once", "daemon"} {
		if !needsProxy(mode) {
			t.Fatalf("needsProxy(%q) = false, want true", mode)
		}
	}
	for _, mode := range []string{"register-share", "import-shares", "rebuild-index"} {
		if needsProxy(mode) {
			t.Fatalf("needsProxy(%q) = true, want false", mode)
		}
	}
}
