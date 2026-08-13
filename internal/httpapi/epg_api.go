package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jqjiang/tvr/internal/core/epg"
	"github.com/jqjiang/tvr/internal/core/store"
)

type epgSourceDTO struct {
	ID              int64      `json:"id"`
	Name            string     `json:"name"`
	URL             string     `json:"url"`
	Enabled         bool       `json:"enabled"`
	RefreshInterval string     `json:"refresh_interval"`
	LastRefreshAt   *time.Time `json:"last_refresh_at"`
	LastError       string     `json:"last_error"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func mapEPGSourceDTO(src store.EPGSource) epgSourceDTO {
	return epgSourceDTO{
		ID:              src.ID,
		Name:            src.Name,
		URL:             src.URL,
		Enabled:         src.Enabled,
		RefreshInterval: src.RefreshInterval.String(),
		LastRefreshAt:   src.LastRefreshAt,
		LastError:       src.LastError,
		CreatedAt:       src.CreatedAt,
		UpdatedAt:       src.UpdatedAt,
	}
}

func (s *Server) handleListEPGSources(w http.ResponseWriter, r *http.Request) {
	sources, err := s.store.ListEPGSources(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	items := make([]epgSourceDTO, 0, len(sources))
	for _, src := range sources {
		items = append(items, mapEPGSourceDTO(src))
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleCreateEPGSource(w http.ResponseWriter, r *http.Request) {
	var in store.EPGSourceInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	src, err := s.workflows.CreateEPGSource(r.Context(), in)
	if err != nil {
		s.writeWorkflowError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, mapEPGSourceDTO(src))
}

func (s *Server) handleUpdateEPGSource(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var in store.EPGSourceInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	src, err := s.workflows.UpdateEPGSource(r.Context(), id, in)
	if err != nil {
		s.writeWorkflowError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, mapEPGSourceDTO(src))
}

func (s *Server) handleDeleteEPGSource(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.workflows.DeleteEPGSource(r.Context(), id); err != nil {
		s.writeWorkflowError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRefreshEPG(w http.ResponseWriter, r *http.Request) {
	if err := s.epg.EnqueueRefreshEnabled(r.Context()); err != nil {
		if errors.Is(err, epg.ErrAdmissionClosed) {
			writeError(w, http.StatusServiceUnavailable, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"started": true})
}

func (s *Server) handleRefreshEPGSource(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if _, err := s.store.GetEPGSource(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.epg.EnqueueRefreshSource(id); err != nil {
		if errors.Is(err, epg.ErrAdmissionClosed) {
			writeError(w, http.StatusServiceUnavailable, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"started": true})
}

func (s *Server) handleEPGStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.epg.Status())
}

func (s *Server) handleSearchEPGChannels(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	q := r.URL.Query().Get("q")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	writeJSON(w, http.StatusOK, s.epg.SearchSourceChannels(id, q, limit))
}

func (s *Server) handleEPGSourceGuide(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	src, err := s.store.GetEPGSource(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	fromRaw := strings.TrimSpace(r.URL.Query().Get("from"))
	toRaw := strings.TrimSpace(r.URL.Query().Get("to"))
	if fromRaw == "" || toRaw == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("from and to are required"))
		return
	}
	from, err := time.Parse(time.RFC3339, fromRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid from: %w", err))
		return
	}
	to, err := time.Parse(time.RFC3339, toRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid to: %w", err))
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	result, err := s.epg.QuerySourceGuide(id, src.URL, strings.TrimSpace(src.LastError) != "", epg.GuideQuery{
		From:   from,
		To:     to,
		Q:      r.URL.Query().Get("q"),
		Offset: offset,
		Limit:  limit,
	})
	if err != nil {
		switch {
		case errors.Is(err, epg.ErrGuideRefreshRequired):
			writeError(w, http.StatusConflict, fmt.Errorf("refresh required"))
		case errors.Is(err, epg.ErrGuideInvalidQuery):
			writeError(w, http.StatusBadRequest, err)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}
