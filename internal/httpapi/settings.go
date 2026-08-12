package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"time"

	"github.com/jqjiang/tvr/internal/relay"
	"github.com/jqjiang/tvr/internal/store"
)

type settingsResponse struct {
	Transcode       store.TranscodeSettings `json:"transcode"`
	FFmpegPath      string                  `json:"ffmpeg_path"`
	FFmpegAvailable bool                    `json:"ffmpeg_available"`
	System          systemSettingsDTO       `json:"system"`
}

type settingsUpdateRequest struct {
	Transcode store.TranscodeSettings `json:"transcode"`
}

type systemSettingsDTO struct {
	ListenAddr         string `json:"listen_addr"`
	BaseURL            string `json:"base_url"`
	TrustProxy         bool   `json:"trust_proxy"`
	DataDir            string `json:"data_dir"`
	DatabasePath       string `json:"database_path"`
	LogLevel           string `json:"log_level"`
	FFmpegPath         string `json:"ffmpeg_path"`
	RelayBufferSize    int    `json:"relay_buffer_size"`
	RelayIdleTimeout   string `json:"relay_idle_timeout"`
	RelayConnTimeout   string `json:"relay_conn_timeout"`
	EPGMaxBytes        int64  `json:"epg_max_bytes"`
	EPGDefaultInterval string `json:"epg_default_interval"`
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.GetTranscodeSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.settingsResponse(settings))
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var in settingsUpdateRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()

	saved, err := s.store.UpdateTranscodeSettings(r.Context(), in.Transcode)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Detach cleanup from the client request so abort/navigation cannot
	// report failure after settings were already committed.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.relay.ApplyProfile(ctx, relay.TranscodeProfile{
		FFmpegPath:       s.cfg.FFmpegPath,
		VideoCRF:         saved.VideoCRF,
		VideoPreset:      saved.VideoPreset,
		AudioBitrateKbps: saved.AudioBitrateKbps,
		MaxHeight:        saved.MaxHeight,
		StartupTimeout:   time.Duration(saved.StartupTimeoutSeconds) * time.Second,
	}); err != nil {
		writeError(w, http.StatusGatewayTimeout, fmt.Errorf("settings saved but active relay cleanup failed: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, s.settingsResponse(saved))
}

func (s *Server) settingsResponse(settings store.TranscodeSettings) settingsResponse {
	path := s.cfg.FFmpegPath
	_, lookErr := exec.LookPath(path)
	return settingsResponse{
		Transcode:       settings,
		FFmpegPath:      path,
		FFmpegAvailable: lookErr == nil,
		System: systemSettingsDTO{
			ListenAddr:         s.cfg.ListenAddr,
			BaseURL:            s.cfg.BaseURL,
			TrustProxy:         s.cfg.TrustProxy,
			DataDir:            s.cfg.DataDir,
			DatabasePath:       s.cfg.DatabasePath,
			LogLevel:           s.cfg.LogLevel,
			FFmpegPath:         s.cfg.FFmpegPath,
			RelayBufferSize:    s.cfg.RelayBufferSize,
			RelayIdleTimeout:   s.cfg.RelayIdleTimeout.String(),
			RelayConnTimeout:   s.cfg.RelayConnTimeout.String(),
			EPGMaxBytes:        s.cfg.EPGMaxBytes,
			EPGDefaultInterval: s.cfg.EPGDefaultEvery.String(),
		},
	}
}

func (s *Server) subscribeChannel(ctx context.Context, ch store.Channel) (io.ReadCloser, error) {
	var last error
	// Retry briefly across the window where a channel save has committed a new
	// updated_at but PublishChannel has not yet installed that revision.
	for attempt := 0; attempt < 8; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * time.Millisecond):
			}
			fresh, getErr := s.store.GetChannel(ctx, ch.ID)
			if getErr != nil {
				return nil, getErr
			}
			ch = fresh
		}
		up := relay.Upstream{
			URL:       ch.UpstreamURL,
			Headers:   ch.Headers,
			Transcode: ch.TranscodeEnabled,
			Revision:  ch.UpdatedAt.UTC().Format(time.RFC3339Nano),
		}
		reader, err := s.relay.Subscribe(ctx, ch.ID, up)
		if err == nil || !errors.Is(err, relay.ErrStaleRevision) {
			return reader, err
		}
		last = err
	}
	return nil, last
}
