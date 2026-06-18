package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"five/internal/proxy"
)

const (
	envProxyKey      = "FIVE_PROXY_KEY"
	envProxyPassword = "FIVE_PROXY_PASSWORD"
	defaultEnvFile   = ".env"
)

type proxyConfig struct {
	Key      string
	Password string
}

func needsProxy(mode string) bool {
	switch mode {
	case "crawl", "run-scheduler-once", "daemon":
		return true
	default:
		return false
	}
}

func resolveProxyConfig(flagKey, flagPassword, envFile string) (proxyConfig, error) {
	dotEnv, err := loadDotEnv(envFile)
	if err != nil {
		return proxyConfig{}, err
	}
	key := firstNonEmpty(flagKey, os.Getenv(envProxyKey), dotEnv[envProxyKey])
	password := firstNonEmpty(flagPassword, os.Getenv(envProxyPassword), dotEnv[envProxyPassword])
	if key == "" && password == "" {
		return proxyConfig{}, errors.New("proxy credentials required: set -proxy-key/-proxy-password or FIVE_PROXY_KEY/FIVE_PROXY_PASSWORD in environment or .env")
	}
	if key == "" || password == "" {
		return proxyConfig{}, errors.New("proxy credentials incomplete: both proxy key and proxy password are required")
	}
	return proxyConfig{Key: key, Password: password}, nil
}

func loadDotEnv(path string) (map[string]string, error) {
	if path == "" {
		path = defaultEnvFile
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	defer f.Close()

	values := make(map[string]string)
	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: invalid env line", path, lineNo)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if key == "" {
			return nil, fmt.Errorf("%s:%d: empty env key", path, lineNo)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func newProxyProvider(cfg proxyConfig) *proxy.Provider {
	return &proxy.Provider{
		Key:      cfg.Key,
		Password: cfg.Password,
	}
}
