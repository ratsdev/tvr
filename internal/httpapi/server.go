package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jqjiang/tvr/internal/app"
	"github.com/jqjiang/tvr/internal/config"
	"github.com/jqjiang/tvr/internal/epg"
	"github.com/jqjiang/tvr/internal/relay"
	"github.com/jqjiang/tvr/internal/store"
)

// Server is the HTTP API and admin UI.
type Server struct {
	cfg    config.Config
	store  *store.Store
	relay  *relay.Manager
	epg    *epg.Service
	app    *app.Workflows
	logger *slog.Logger
	mux    *http.ServeMux
	webFS  fs.FS
}

// New constructs the HTTP server.
func New(cfg config.Config, st *store.Store, rel *relay.Manager, epgSvc *epg.Service, workflows *app.Workflows, webFS fs.FS, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if workflows == nil {
		workflows = &app.Workflows{
			Store:              st,
			EPG:                epgSvc,
			DefaultEPGInterval: cfg.EPGDefaultEvery,
			Logger:             logger,
		}
	}
	s := &Server{
		cfg:    cfg,
		store:  st,
		relay:  rel,
		epg:    epgSvc,
		app:    workflows,
		logger: logger,
		mux:    http.NewServeMux(),
		webFS:  webFS,
	}
	s.routes()
	return s
}

func (s *Server) writeWorkflowError(w http.ResponseWriter, err error) {
	if errors.Is(err, epg.ErrAdmissionClosed) {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("epg worker shutting down"))
		return
	}
	writeStoreError(w, err)
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	return s.withLogging(s.mux)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/health", s.handleHealth)

	s.mux.HandleFunc("GET /api/channels", s.handleListChannels)
	s.mux.HandleFunc("POST /api/channels", s.handleCreateChannel)
	s.mux.HandleFunc("GET /api/channels/{id}", s.handleGetChannel)
	s.mux.HandleFunc("PUT /api/channels/{id}", s.handleUpdateChannel)
	s.mux.HandleFunc("DELETE /api/channels/{id}", s.handleDeleteChannel)
	s.mux.HandleFunc("POST /api/channels/{id}/test", s.handleTestChannel)

	s.mux.HandleFunc("GET /api/epg/sources", s.handleListEPGSources)
	s.mux.HandleFunc("POST /api/epg/sources", s.handleCreateEPGSource)
	s.mux.HandleFunc("PUT /api/epg/sources/{id}", s.handleUpdateEPGSource)
	s.mux.HandleFunc("DELETE /api/epg/sources/{id}", s.handleDeleteEPGSource)
	s.mux.HandleFunc("POST /api/epg/refresh", s.handleRefreshEPG)
	s.mux.HandleFunc("GET /api/epg/status", s.handleEPGStatus)
	s.mux.HandleFunc("GET /api/epg/sources/{id}/channels", s.handleSearchEPGChannels)
	s.mux.HandleFunc("GET /api/epg/sources/{id}/guide", s.handleEPGSourceGuide)
	s.mux.HandleFunc("POST /api/epg/sources/{id}/refresh", s.handleRefreshEPGSource)

	s.mux.HandleFunc("GET /api/relays", s.handleListRelays)
	s.mux.HandleFunc("POST /api/relays", s.handleCreateRelay)
	s.mux.HandleFunc("POST /api/relays/import", s.handleImportRelay)
	s.mux.HandleFunc("GET /api/relays/{id}", s.handleGetRelay)
	s.mux.HandleFunc("PUT /api/relays/{id}", s.handleUpdateRelay)
	s.mux.HandleFunc("DELETE /api/relays/{id}", s.handleDeleteRelay)
	s.mux.HandleFunc("PUT /api/relays/{id}/epg-sources", s.handleSetRelayEPGSources)
	s.mux.HandleFunc("PUT /api/relays/{id}/layout", s.handleReplaceRelayLayout)
	s.mux.HandleFunc("POST /api/relays/{id}/groups", s.handleCreateRelayGroup)
	s.mux.HandleFunc("PUT /api/relays/{id}/groups/{groupId}", s.handleUpdateRelayGroup)
	s.mux.HandleFunc("DELETE /api/relays/{id}/groups/{groupId}", s.handleDeleteRelayGroup)
	s.mux.HandleFunc("POST /api/relays/{id}/memberships", s.handleAddMembership)
	s.mux.HandleFunc("PUT /api/relays/{id}/memberships/{membershipId}", s.handleUpdateMembership)
	s.mux.HandleFunc("DELETE /api/relays/{id}/memberships/{membershipId}", s.handleDeleteMembership)
	s.mux.HandleFunc("GET /api/relay/status", s.handleRelayStatus)

	s.mux.HandleFunc("GET /r/{slug}/playlist.m3u", s.handleRelayPlaylist)
	s.mux.HandleFunc("GET /r/{slug}/epg.xml", s.handleRelayEPG)
	s.mux.HandleFunc("GET /stream/{channelId}", s.handleChannelStream)

	fileServer := http.FileServer(http.FS(s.webFS))
	s.mux.Handle("GET /assets/", http.StripPrefix("/assets/", fileServer))
	s.mux.HandleFunc("GET /{$}", s.handleIndex)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(s.webFS, "index.html")
	if err != nil {
		http.Error(w, "ui not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"base_url": s.publicBaseURL(r),
	})
}

// publicBaseURL returns the configured public origin, or derives one from the request
// when TVR_BASE_URL is unset. X-Forwarded-* headers are used only when TrustProxy is enabled.
func (s *Server) publicBaseURL(r *http.Request) string {
	if base := strings.TrimRight(strings.TrimSpace(s.cfg.BaseURL), "/"); base != "" {
		return base
	}
	proto := "http"
	if r.TLS != nil {
		proto = "https"
	}
	host := r.Host
	if s.cfg.TrustProxy {
		if v := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])); v == "http" || v == "https" {
			proto = v
		}
		if v := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); v != "" &&
			!strings.ContainsAny(v, "/ \t\r\n") {
			host = v
		}
	}
	if host == "" {
		return ""
	}
	return proto + "://" + host
}

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
	ch, err := s.store.UpdateChannel(r.Context(), id, in)
	if err != nil {
		writeStoreError(w, err)
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
	if err := s.store.DeleteChannel(r.Context(), id); err != nil {
		writeStoreError(w, err)
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
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ch.UpstreamURL, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.Header.Set("User-Agent", "tvr/1.0")
	for k, v := range ch.Headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	buf := make([]byte, 188*4)
	n, _ := io.ReadFull(resp.Body, buf)
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300 && n > 0
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          ok,
		"status_code": resp.StatusCode,
		"bytes_read":  n,
		"has_sync":    n >= 188 && buf[0] == 0x47,
		"looks_hls":   n > 7 && strings.Contains(string(buf[:n]), "#EXTM3U"),
	})
}

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
	src, err := s.app.CreateEPGSource(r.Context(), in)
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
	src, err := s.app.UpdateEPGSource(r.Context(), id, in)
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
	if err := s.app.DeleteEPGSource(r.Context(), id); err != nil {
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

func (s *Server) handleRelayStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.relay.AllStatuses())
}

func (s *Server) handleListRelays(w http.ResponseWriter, r *http.Request) {
	relays, err := s.store.ListRelays(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if relays == nil {
		relays = []store.Relay{}
	}
	type item struct {
		store.Relay
		PlaylistURL string `json:"playlist_url"`
		EPGURL      string `json:"epg_url"`
	}
	out := make([]item, 0, len(relays))
	base := s.publicBaseURL(r)
	for _, rel := range relays {
		out = append(out, item{
			Relay:       rel,
			PlaylistURL: fmt.Sprintf("%s/r/%s/playlist.m3u", base, rel.Slug),
			EPGURL:      fmt.Sprintf("%s/r/%s/epg.xml", base, rel.Slug),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateRelay(w http.ResponseWriter, r *http.Request) {
	var in store.RelayInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	rel, err := s.store.CreateRelay(r.Context(), in)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	detail, err := s.store.GetRelayDetail(r.Context(), rel.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, detail)
}

func (s *Server) handleGetRelay(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	detail, err := s.store.GetRelayDetail(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleUpdateRelay(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var in store.RelayInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if _, err := s.app.UpdateRelay(r.Context(), id, in); err != nil {
		s.writeWorkflowError(w, err)
		return
	}
	detail, err := s.store.GetRelayDetail(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleDeleteRelay(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.app.DeleteRelay(r.Context(), id); err != nil {
		s.writeWorkflowError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSetRelayEPGSources(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var body struct {
		EPGSourceIDs []int64 `json:"epg_source_ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.SetRelayEPGSources(r.Context(), id, body.EPGSourceIDs); err != nil {
		writeStoreError(w, err)
		return
	}
	detail, err := s.store.GetRelayDetail(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleReplaceRelayLayout(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var layout store.RelayLayout
	if err := decodeJSON(r, &layout); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	detail, err := s.store.ReplaceRelayLayout(r.Context(), id, layout)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleCreateRelayGroup(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	g, err := s.store.CreateRelayGroup(r.Context(), id, body.Name)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

func (s *Server) handleUpdateRelayGroup(w http.ResponseWriter, r *http.Request) {
	relayID, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	groupID, err := pathID(r, "groupId")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	g, err := s.store.UpdateRelayGroup(r.Context(), relayID, groupID, body.Name)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) handleDeleteRelayGroup(w http.ResponseWriter, r *http.Request) {
	relayID, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	groupID, err := pathID(r, "groupId")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.DeleteRelayGroup(r.Context(), relayID, groupID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAddMembership(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var in store.MembershipInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	m, err := s.app.AddMembership(r.Context(), id, in)
	if err != nil {
		s.writeWorkflowError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) handleUpdateMembership(w http.ResponseWriter, r *http.Request) {
	relayID, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	membershipID, err := pathID(r, "membershipId")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var in store.MembershipInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	m, err := s.app.UpdateMembership(r.Context(), relayID, membershipID, in)
	if err != nil {
		s.writeWorkflowError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) handleDeleteMembership(w http.ResponseWriter, r *http.Request) {
	relayID, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	membershipID, err := pathID(r, "membershipId")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.app.DeleteMembership(r.Context(), relayID, membershipID); err != nil {
		s.writeWorkflowError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRelayPlaylist(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	_, lineup, err := s.store.ListRelayLineup(r.Context(), slug)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	base := s.publicBaseURL(r)
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	fmt.Fprintf(w, `#EXTM3U url-tvg="%s/r/%s/epg.xml"`+"\n", base, slug)
	for _, e := range lineup {
		attrs := make([]string, 0, 5)
		if e.TvgID != "" {
			attrs = append(attrs, fmt.Sprintf(`tvg-id="%s"`, escapeAttr(e.TvgID)))
		}
		attrs = append(attrs, fmt.Sprintf(`tvg-name="%s"`, escapeAttr(e.Name)))
		if e.LogoURL != "" {
			attrs = append(attrs, fmt.Sprintf(`tvg-logo="%s"`, escapeAttr(e.LogoURL)))
		}
		if e.Number > 0 {
			attrs = append(attrs, fmt.Sprintf(`tvg-chno="%d"`, e.Number))
		}
		if e.GroupTitle != "" {
			attrs = append(attrs, fmt.Sprintf(`group-title="%s"`, escapeAttr(e.GroupTitle)))
		}
		fmt.Fprintf(w, "#EXTINF:-1 %s,%s\n", strings.Join(attrs, " "), sanitizePlaylistText(e.Name))
		fmt.Fprintf(w, "%s/stream/%s\n", base, e.ChannelID)
	}
}

func (s *Server) handleRelayEPG(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if _, err := s.store.GetRelayBySlug(r.Context(), slug); err != nil {
		writeStoreError(w, err)
		return
	}
	f, fi, err := s.epg.OpenRelayCache(slug)
	if err != nil {
		http.Error(w, "epg not available yet", http.StatusServiceUnavailable)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=60")
	http.ServeContent(w, r, "epg.xml", fi.ModTime(), f)
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
	reader, err := s.relay.Subscribe(r.Context(), ch.ID, relay.Upstream{
		URL:     ch.UpstreamURL,
		Headers: ch.Headers,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		if errors.Is(err, relay.ErrReadyTimeout) {
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
	buf := make([]byte, 188*32)
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

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		path := r.URL.Path
		attrs := []any{
			"method", r.Method,
			"path", path,
			"status", rw.status,
			"duration", time.Since(start).String(),
		}
		if strings.HasPrefix(path, "/stream/") {
			attrs = append(attrs, "remote", r.RemoteAddr)
			s.logger.Info("stream", attrs...)
			return
		}
		switch {
		case rw.status >= 500:
			s.logger.Error("http", attrs...)
		case rw.status >= 400:
			s.logger.Info("http", attrs...)
		case r.Method != http.MethodGet && r.Method != http.MethodHead:
			s.logger.Info("http", attrs...)
		default:
			// Successful GETs (UI, assets, polls) are debug-only.
			s.logger.Debug("http", attrs...)
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func pathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid %s", name)
	}
	return id, nil
}

func pathChannelID(r *http.Request, name string) (string, error) {
	id := strings.TrimSpace(r.PathValue(name))
	if _, err := uuid.Parse(id); err != nil {
		return "", fmt.Errorf("invalid %s", name)
	}
	return id, nil
}

func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, store.ErrValidation):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

func sanitizePlaylistText(v string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\r', '\n', 0:
			return ' '
		default:
			return r
		}
	}, v)
}

func escapeAttr(v string) string {
	return strings.NewReplacer(`"`, `\"`).Replace(sanitizePlaylistText(v))
}
