package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"time"

	"github.com/ratsdev/tvr/internal/core/store"
	"github.com/ratsdev/tvr/internal/core/transcode"
)

type settingsResponse struct {
	Brand           store.BrandSettings     `json:"brand"`
	Transcode       store.TranscodeSettings `json:"transcode"`
	FFmpegPath      string                  `json:"ffmpeg_path"`
	FFmpegAvailable bool                    `json:"ffmpeg_available"`
	System          systemSettingsDTO       `json:"system"`
}

type settingsUpdateRequest struct {
	Brand     *store.BrandSettings    `json:"brand,omitempty"`
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
	brand, err := s.store.GetBrandSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.settingsResponse(settings, brand))
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var in settingsUpdateRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()

	if err := store.ValidateTranscodeSettings(in.Transcode); err != nil {
		writeStoreError(w, err)
		return
	}
	brandPlan, err := s.prepareBrandUpdate(in.Brand)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	brandPlan, err = s.stageBrandIcon(brandPlan)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("could not write brand icon: %w", err))
		return
	}

	saved, brand, err := s.store.UpdateAppSettings(r.Context(), in.Transcode, in.Brand)
	if err != nil {
		s.abandonBrandIcon(brandPlan)
		writeStoreError(w, err)
		return
	}
	if err := s.commitBrandIcon(brandPlan); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("settings saved but brand icon file update failed: %w", err))
		return
	}
	// Detach cleanup from the client request so abort/navigation cannot
	// report failure after settings were already committed.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.live.ApplyProfile(ctx, transcode.Profile{
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
	writeJSON(w, http.StatusOK, s.settingsResponse(saved, brand))
}

func (s *Server) settingsResponse(settings store.TranscodeSettings, brand store.BrandSettings) settingsResponse {
	path := s.cfg.FFmpegPath
	_, lookErr := exec.LookPath(path)
	return settingsResponse{
		Brand: store.BrandSettings{
			Icon:  s.publicBrandIcon(brand.Icon),
			Title: brand.Title,
		},
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
