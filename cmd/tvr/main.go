package main

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ratsdev/tvr/internal/config"
	"github.com/ratsdev/tvr/internal/core/epg"
	"github.com/ratsdev/tvr/internal/core/store"
	"github.com/ratsdev/tvr/internal/core/stream"
	"github.com/ratsdev/tvr/internal/core/transcode"
	"github.com/ratsdev/tvr/internal/core/workflows"
	"github.com/ratsdev/tvr/internal/httpapi"
	"github.com/ratsdev/tvr/internal/version"
	"github.com/ratsdev/tvr/static"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		logger.Error("create data dir", "err", err)
		os.Exit(1)
	}

	st, err := store.Open(cfg.DatabasePath)
	if err != nil {
		logger.Error("open store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	settings, err := st.GetTranscodeSettings(context.Background())
	if err != nil {
		logger.Error("load transcode settings", "err", err)
		os.Exit(1)
	}

	live := stream.NewManager(stream.Options{
		BufferSize:  cfg.RelayBufferSize,
		IdleTimeout: cfg.RelayIdleTimeout,
		ConnTimeout: cfg.RelayConnTimeout,
		Logger:      logger,
		TranscodeProfile: transcode.Profile{
			FFmpegPath:       cfg.FFmpegPath,
			VideoCRF:         settings.VideoCRF,
			VideoPreset:      settings.VideoPreset,
			AudioBitrateKbps: settings.AudioBitrateKbps,
			MaxHeight:        settings.MaxHeight,
			StartupTimeout:   time.Duration(settings.StartupTimeoutSeconds) * time.Second,
		},
	})

	epgSvc := epg.New(st, cfg.DataDir, cfg.EPGMaxBytes, logger)
	epgCtx, epgCancel := context.WithCancel(context.Background())
	epgSvc.Start(epgCtx)

	staticFS, err := fs.Sub(static.Content, ".")
	if err != nil {
		logger.Error("static assets", "err", err)
		os.Exit(1)
	}

	wf := &workflows.Workflows{
		Store:              st,
		EPG:                epgSvc,
		DefaultEPGInterval: cfg.EPGDefaultEvery,
		Logger:             logger,
	}
	api := httpapi.New(cfg, st, live, epgSvc, wf, staticFS, logger)
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "(auto from request)"
		}
		logger.Info("tvr listening",
			"version", version.Label(),
			"commit", version.ShortCommit(),
			"addr", cfg.ListenAddr,
			"base_url", baseURL,
			"trust_proxy", cfg.TrustProxy,
			"data_dir", cfg.DataDir,
			"ffmpeg", cfg.FFmpegPath,
		)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", "err", err)
			stop()
		}
	}()

	<-sigCtx.Done()
	logger.Info("shutting down")

	// 1) Stop accepting new stream subscriptions and reap active sessions/ffmpeg.
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := live.Close(closeCtx); err != nil {
		logger.Warn("stream manager close", "err", err)
	}
	closeCancel()

	// 2) Shut down HTTP while EPG admission remains open for in-flight mutations.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)

	// 3) Close EPG admission, drain holders + pending work, then cancel worker.
	epgSvc.CloseAdmission()
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer drainCancel()
	if err := epgSvc.WaitAdmissionDrained(drainCtx); err != nil {
		logger.Warn("epg admission drain", "err", err)
	}
	if err := epgSvc.DrainPending(drainCtx); err != nil {
		logger.Warn("epg pending drain", "err", err)
	}
	epgCancel()
	if err := epgSvc.Wait(drainCtx); err != nil {
		logger.Warn("epg worker wait", "err", err)
	}
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lv}))
}
