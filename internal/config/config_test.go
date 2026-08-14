package config_test

import (
	"testing"
	"time"

	"github.com/ratsdev/tvr/internal/config"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("TVR_LISTEN", "")
	t.Setenv("TVR_BASE_URL", "")
	t.Setenv("TVR_TRUST_PROXY", "")
	t.Setenv("TVR_DATA_DIR", "")
	t.Setenv("TVR_DATABASE", "")
	t.Setenv("TVR_LOG_LEVEL", "")
	t.Setenv("TVR_FFMPEG_PATH", "")
	t.Setenv("TVR_RELAY_BUFFER_SIZE", "")
	t.Setenv("TVR_RELAY_IDLE_TIMEOUT", "")
	t.Setenv("TVR_RELAY_CONN_TIMEOUT", "")
	t.Setenv("TVR_EPG_MAX_BYTES", "")
	t.Setenv("TVR_EPG_DEFAULT_INTERVAL", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":8080" {
		t.Fatalf("listen=%q", cfg.ListenAddr)
	}
	if cfg.BaseURL != "" {
		t.Fatalf("base=%q want empty (auto)", cfg.BaseURL)
	}
	if cfg.TrustProxy {
		t.Fatal("TrustProxy should default false")
	}
	if cfg.FFmpegPath != "ffmpeg" {
		t.Fatalf("ffmpeg=%q", cfg.FFmpegPath)
	}
	if cfg.RelayIdleTimeout != 30*time.Second {
		t.Fatalf("idle=%s", cfg.RelayIdleTimeout)
	}
}

func TestLoadRejectsMalformedValues(t *testing.T) {
	t.Setenv("TVR_RELAY_BUFFER_SIZE", "nope")
	if _, err := config.Load(); err == nil {
		t.Fatal("expected invalid buffer size error")
	}
	t.Setenv("TVR_RELAY_BUFFER_SIZE", "4")
	if _, err := config.Load(); err == nil {
		t.Fatal("expected buffer size < 8 error")
	}
	t.Setenv("TVR_RELAY_BUFFER_SIZE", "")
	t.Setenv("TVR_BASE_URL", "ftp://example.com")
	if _, err := config.Load(); err == nil {
		t.Fatal("expected invalid base URL error")
	}
	t.Setenv("TVR_BASE_URL", "https://example.com/iptv?x=1")
	if _, err := config.Load(); err == nil {
		t.Fatal("expected query rejection")
	}
	t.Setenv("TVR_BASE_URL", "https://example.com/iptv")
	t.Setenv("TVR_TRUST_PROXY", "maybe")
	if _, err := config.Load(); err == nil {
		t.Fatal("expected invalid trust proxy error")
	}
	t.Setenv("TVR_TRUST_PROXY", "true")
	t.Setenv("TVR_EPG_DEFAULT_INTERVAL", "30s")
	if _, err := config.Load(); err == nil {
		t.Fatal("expected interval < 1m error")
	}
}

func TestLoadAcceptsValidOverrides(t *testing.T) {
	t.Setenv("TVR_BASE_URL", "https://tv.example.com/iptv")
	t.Setenv("TVR_TRUST_PROXY", "1")
	t.Setenv("TVR_RELAY_BUFFER_SIZE", "64")
	t.Setenv("TVR_EPG_DEFAULT_INTERVAL", "2h")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "https://tv.example.com/iptv" || !cfg.TrustProxy || cfg.RelayBufferSize != 64 {
		t.Fatalf("cfg=%+v", cfg)
	}
}
