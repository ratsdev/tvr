package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/ratsdev/tvr/internal/core/mpegts"
	"github.com/ratsdev/tvr/internal/core/store"
	"github.com/ratsdev/tvr/internal/core/stream"
)

func (s *Server) handleLiveStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.live.AllStatuses())
}

func (s *Server) handleChannelStream(w http.ResponseWriter, r *http.Request) {
	channelID, err := pathChannelID(r, "channelId")
	if err != nil {
		http.Error(w, "invalid channel id", http.StatusBadRequest)
		return
	}
	ch, err := s.store.GetChannel(r.Context(), channelID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.serveChannelStream(w, r, ch)
}

func (s *Server) serveChannelStream(w http.ResponseWriter, r *http.Request, ch store.Channel) {
	reader, err := s.subscribeChannel(r.Context(), ch)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		if errors.Is(err, stream.ErrChannelBlocked) || errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if errors.Is(err, stream.ErrReadyTimeout) {
			http.Error(w, err.Error(), http.StatusGatewayTimeout)
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer reader.Close()

	ctx := r.Context()
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "close")
	// Subscribe already waited for readiness; 200 only after that point.
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)
	writeWait := s.cfg.RelayConnTimeout
	if writeWait <= 0 {
		writeWait = 10 * time.Second
	}
	buf := make([]byte, mpegts.PacketSize*32)
	for {
		if ctx.Err() != nil {
			return
		}
		n, readErr := reader.Read(buf)
		if n > 0 {
			_ = rc.SetWriteDeadline(time.Now().Add(writeWait))
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return
			}
			_ = rc.Flush()
		}
		if readErr != nil {
			return
		}
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
		src := ch.StreamSource()
		reader, err := s.live.Subscribe(ctx, ch.ID, src)
		if err == nil || !errors.Is(err, stream.ErrStaleRevision) {
			return reader, err
		}
		last = err
	}
	return nil, last
}
