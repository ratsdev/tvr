package httpapi

import (
	"context"
	"net/http"

	"github.com/ratsdev/tvr/internal/core/store"
)

func (s *Server) handleListProxies(w http.ResponseWriter, r *http.Request) {
	proxies, err := s.store.ListProxies(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, proxies)
}

func (s *Server) handleGetProxy(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	p, err := s.store.GetProxy(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleCreateProxy(w http.ResponseWriter, r *http.Request) {
	var in store.ProxyInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	p, err := s.store.CreateProxy(r.Context(), in)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleUpdateProxy(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var in store.ProxyInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.channelMu.Lock()
	defer s.channelMu.Unlock()
	p, affected, err := s.store.UpdateProxy(r.Context(), id, in)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var firstPubErr error
	for _, channelID := range affected {
		ch, err := s.store.GetChannel(context.Background(), channelID)
		if err != nil {
			s.logger.Error("load channel after proxy update", "channel_id", channelID, "err", err)
			if firstPubErr == nil {
				firstPubErr = err
			}
			continue
		}
		if err := s.publishChannel(ch, true); err != nil {
			s.logger.Error("publish channel revision after proxy update", "channel_id", ch.ID, "err", err)
			if firstPubErr == nil {
				firstPubErr = err
			}
		}
	}
	if firstPubErr != nil {
		writeLiveUpdateTimeout(w, "proxy saved", firstPubErr)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleDeleteProxy(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.DeleteProxy(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
