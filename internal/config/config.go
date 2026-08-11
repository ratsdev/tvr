package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds process-wide settings loaded from the environment.
type Config struct {
	ListenAddr       string
	BaseURL          string
	TrustProxy       bool
	DataDir          string
	DatabasePath     string
	LogLevel         string
	RelayBufferSize  int
	RelayIdleTimeout time.Duration
	RelayConnTimeout time.Duration
	EPGMaxBytes      int64
	EPGDefaultEvery  time.Duration
}

// Load reads configuration from environment variables with sensible defaults.
func Load() (Config, error) {
	cfg := Config{
		ListenAddr: envOr("TVR_LISTEN", ":8080"),
		// Empty BaseURL means derive from each request.
		BaseURL:          strings.TrimRight(strings.TrimSpace(os.Getenv("TVR_BASE_URL")), "/"),
		DataDir:          envOr("TVR_DATA_DIR", "./data"),
		LogLevel:         strings.ToLower(envOr("TVR_LOG_LEVEL", "info")),
		RelayBufferSize:  envIntOr("TVR_RELAY_BUFFER_SIZE", 1024),
		RelayIdleTimeout: envDurationOr("TVR_RELAY_IDLE_TIMEOUT", 30*time.Second),
		RelayConnTimeout: envDurationOr("TVR_RELAY_CONN_TIMEOUT", 10*time.Second),
		EPGMaxBytes:      envInt64Or("TVR_EPG_MAX_BYTES", 64<<20),
		EPGDefaultEvery:  envDurationOr("TVR_EPG_DEFAULT_INTERVAL", time.Hour),
	}
	cfg.DatabasePath = envOr("TVR_DATABASE", cfg.DataDir+"/tvr.db")

	trustProxy, err := envBool("TVR_TRUST_PROXY", false)
	if err != nil {
		return Config{}, err
	}
	cfg.TrustProxy = trustProxy

	if err := validateEnvInts(); err != nil {
		return Config{}, err
	}
	if err := validateEnvDurations(); err != nil {
		return Config{}, err
	}

	if cfg.RelayBufferSize < 8 {
		return Config{}, fmt.Errorf("TVR_RELAY_BUFFER_SIZE must be >= 8")
	}
	if cfg.RelayIdleTimeout <= 0 {
		return Config{}, fmt.Errorf("TVR_RELAY_IDLE_TIMEOUT must be > 0")
	}
	if cfg.RelayConnTimeout <= 0 {
		return Config{}, fmt.Errorf("TVR_RELAY_CONN_TIMEOUT must be > 0")
	}
	if cfg.EPGMaxBytes <= 0 {
		return Config{}, fmt.Errorf("TVR_EPG_MAX_BYTES must be > 0")
	}
	if cfg.EPGDefaultEvery < time.Minute {
		return Config{}, fmt.Errorf("TVR_EPG_DEFAULT_INTERVAL must be >= 1m")
	}
	if cfg.BaseURL != "" {
		if err := validateBaseURL(cfg.BaseURL); err != nil {
			return Config{}, err
		}
	}
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return Config{}, fmt.Errorf("TVR_LOG_LEVEL must be debug, info, warn, or error")
	}
	return cfg, nil
}

func validateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("TVR_BASE_URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("TVR_BASE_URL must be an absolute http(s) URL")
	}
	if u.Host == "" {
		return fmt.Errorf("TVR_BASE_URL must include a host")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("TVR_BASE_URL must not include query or fragment")
	}
	if u.User != nil {
		return fmt.Errorf("TVR_BASE_URL must not include userinfo")
	}
	return nil
}

func validateEnvInts() error {
	for _, key := range []string{"TVR_RELAY_BUFFER_SIZE", "TVR_EPG_MAX_BYTES"} {
		v := strings.TrimSpace(os.Getenv(key))
		if v == "" {
			continue
		}
		if _, err := strconv.ParseInt(v, 10, 64); err != nil {
			return fmt.Errorf("%s: invalid integer %q", key, v)
		}
	}
	return nil
}

func validateEnvDurations() error {
	for _, key := range []string{"TVR_RELAY_IDLE_TIMEOUT", "TVR_RELAY_CONN_TIMEOUT", "TVR_EPG_DEFAULT_INTERVAL"} {
		v := strings.TrimSpace(os.Getenv(key))
		if v == "" {
			continue
		}
		if _, err := time.ParseDuration(v); err != nil {
			return fmt.Errorf("%s: invalid duration %q", key, v)
		}
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) (bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback, nil
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s: invalid boolean %q", key, v)
	}
}

func envIntOr(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func envInt64Or(key string, fallback int64) int64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

func envDurationOr(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}
