package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jqjiang/tvr/internal/core/mpegts"
	"github.com/jqjiang/tvr/internal/core/store"
)

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := s.store.ListChannels(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if channels == nil {
		channels = []store.Channel{}
	}
	writeJSON(w, http.StatusOK, channels)
}

func (s *Server) handleGetChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathChannelID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ch, err := s.store.GetChannel(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	var in store.ChannelInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ch, err := s.store.CreateChannel(r.Context(), in)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ch)
}

func (s *Server) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathChannelID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var in store.ChannelInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.channelMu.Lock()
	defer s.channelMu.Unlock()
	before, err := s.store.GetChannel(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	ch, err := s.store.UpdateChannel(r.Context(), id, in)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	invalidate := store.ChannelRelayInvalidate(before, ch)
	// Detach cleanup from the client request so abort/navigation cannot
	// report failure after the channel row was already committed.
	ctx, cancel := context.WithTimeout(context.Background(), channelStreamCleanupTimeout)
	defer cancel()
	if err := s.live.PublishChannel(ctx, ch.ID, ch.UpdatedAt.UTC().Format(time.RFC3339Nano), invalidate); err != nil {
		s.logger.Error("publish channel revision", "channel_id", ch.ID, "err", err)
		writeError(w, http.StatusGatewayTimeout, fmt.Errorf("channel saved but active relay cleanup failed: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathChannelID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.channelMu.Lock()
	defer s.channelMu.Unlock()
	if err := s.store.DeleteChannel(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), channelStreamCleanupTimeout)
	defer cancel()
	if err := s.live.BlockChannel(ctx, id); err != nil {
		s.logger.Error("block deleted channel", "channel_id", id, "err", err)
		writeError(w, http.StatusGatewayTimeout, fmt.Errorf("channel deleted but active relay cleanup failed: %w", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathChannelID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ch, err := s.store.GetChannel(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var in struct {
		UpstreamID string `json:"upstream_id"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &in); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	up, err := ch.SelectUpstream(in.UpstreamID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	headers := ch.MergedHeaders(up)
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, up.URL, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.Header.Set("User-Agent", "tvr/1.0")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	buf := make([]byte, mpegts.PacketSize*4)
	n, _ := io.ReadFull(resp.Body, buf)
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300 && n > 0
	host := ""
	if parsed, err := url.Parse(up.URL); err == nil {
		host = parsed.Host
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            ok,
		"status_code":   resp.StatusCode,
		"bytes_read":    n,
		"has_sync":      n >= mpegts.PacketSize && buf[0] == mpegts.SyncByte,
		"looks_hls":     n > 7 && strings.Contains(string(buf[:n]), "#EXTM3U"),
		"upstream_id":   up.ID,
		"upstream_host": host,
	})
}
