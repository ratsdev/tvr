package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ratsdev/tvr/internal/core/mpegts"
	"github.com/ratsdev/tvr/internal/core/store"
	"github.com/ratsdev/tvr/internal/core/workflows"
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
	if store.ChannelEPGKey(before) != store.ChannelEPGKey(ch) {
		ids, err := s.store.ChannelRelayIDs(r.Context(), ch.ID)
		if err != nil {
			s.logger.Error("channel epg owners", "channel_id", ch.ID, "err", err)
		} else {
			_ = s.workflows.ApplyDerivedWork(r.Context(), workflows.DerivedWork{RebuildRelays: ids})
		}
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
	cands := up.ResolveCandidates()
	if len(cands) == 0 && strings.TrimSpace(up.URL) != "" {
		cands = []string{up.URL}
	}
	headers := ch.MergedHeaders(up)
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	var (
		fetchURL string
		status   int
		buf      []byte
		lastErr  error
	)
	for _, cand := range cands {
		fetchURL = cand
		status, buf, lastErr = probeFetchURL(ctx, cand, headers)
		if lastErr != nil {
			if ctx.Err() != nil {
				break
			}
			continue
		}
		if status >= 200 && status < 300 && len(buf) > 0 {
			lastErr = nil
			break
		}
	}
	if lastErr != nil && status == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": lastErr.Error()})
		return
	}
	ok := lastErr == nil && status >= 200 && status < 300 && len(buf) > 0
	host := ""
	if parsed, err := url.Parse(fetchURL); err == nil {
		host = parsed.Host
	}
	n := len(buf)
	hasSync := n >= mpegts.PacketSize && buf[0] == mpegts.SyncByte
	looksHLS := n > 7 && strings.Contains(string(buf[:n]), "#EXTM3U")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            ok,
		"status_code":   status,
		"bytes_read":    n,
		"has_sync":      hasSync,
		"looks_hls":     looksHLS,
		"upstream_id":   up.ID,
		"upstream_host": host,
	})
}

func probeFetchURL(ctx context.Context, fetchURL string, headers map[string]string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", "tvr/1.0")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	buf := make([]byte, mpegts.PacketSize*4)
	n, _ := io.ReadFull(resp.Body, buf)
	return resp.StatusCode, buf[:n], nil
}
