package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ratsdev/tvr/internal/config"
	"github.com/ratsdev/tvr/internal/core/epg"
	"github.com/ratsdev/tvr/internal/core/store"
	"github.com/ratsdev/tvr/internal/core/stream"
	"github.com/ratsdev/tvr/internal/core/workflows"
	"github.com/ratsdev/tvr/internal/version"
)

// channelStreamCleanupTimeout bounds waiting for active live sessions after
// channel update/delete. Overridable in tests.
var channelStreamCleanupTimeout = 10 * time.Second

// Server is the HTTP API and admin UI.
type Server struct {
	cfg       config.Config
	store     *store.Store
	live      *stream.Manager
	epg       *epg.Service
	workflows *workflows.Workflows
	logger    *slog.Logger
	mux       *http.ServeMux
	staticFS  fs.FS

	settingsMu sync.Mutex
	channelMu  sync.Mutex
}

// New constructs the HTTP server.
func New(cfg config.Config, st *store.Store, live *stream.Manager, epgSvc *epg.Service, wf *workflows.Workflows, staticFS fs.FS, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if wf == nil {
		wf = &workflows.Workflows{
			Store:              st,
			EPG:                epgSvc,
			DefaultEPGInterval: cfg.EPGDefaultEvery,
			Logger:             logger,
		}
	}
	s := &Server{
		cfg:       cfg,
		store:     st,
		live:      live,
		epg:       epgSvc,
		workflows: wf,
		logger:    logger,
		mux:       http.NewServeMux(),
		staticFS:  staticFS,
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
	s.mux.HandleFunc("POST /api/channels/import", s.handleImportChannels)
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
	s.mux.HandleFunc("PUT /api/relays/{id}/layout", s.handleReplaceRelayLayout)
	s.mux.HandleFunc("POST /api/relays/{id}/groups", s.handleCreateRelayGroup)
	s.mux.HandleFunc("PUT /api/relays/{id}/groups/{groupId}", s.handleUpdateRelayGroup)
	s.mux.HandleFunc("DELETE /api/relays/{id}/groups/{groupId}", s.handleDeleteRelayGroup)
	s.mux.HandleFunc("POST /api/relays/{id}/memberships", s.handleAddMembership)
	s.mux.HandleFunc("PUT /api/relays/{id}/memberships/{membershipId}", s.handleUpdateMembership)
	s.mux.HandleFunc("DELETE /api/relays/{id}/memberships/{membershipId}", s.handleDeleteMembership)
	s.mux.HandleFunc("GET /api/relay/status", s.handleLiveStatus)

	s.mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	s.mux.HandleFunc("PUT /api/settings", s.handlePutSettings)
	s.mux.HandleFunc("GET /brand-icon", s.handleGetBrandIcon)

	s.mux.HandleFunc("GET /r/{slug}/playlist.m3u", s.handleRelayPlaylist)
	s.mux.HandleFunc("GET /r/{slug}/epg.xml", s.handleRelayEPG)
	s.mux.HandleFunc("GET /stream/{channelId}", s.handleChannelStream)

	s.mountStatic()
	s.mux.HandleFunc("GET /{$}", s.handleIndex)
}

func (s *Server) mountStatic() {
	// css/ and js/ live at the static root; images live in static/assets/.
	// Mount the more specific prefixes first so /assets/brand.svg is not
	// looked up as static/brand.svg.
	fileServer := http.FileServer(http.FS(s.staticFS))
	s.mux.Handle("GET /assets/css/", http.StripPrefix("/assets/", fileServer))
	s.mux.Handle("GET /assets/js/", http.StripPrefix("/assets/", fileServer))
	if assetFS, err := fs.Sub(s.staticFS, "assets"); err == nil {
		s.mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(assetFS))))
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(s.staticFS, "index.html")
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
		"version":  version.Label(),
		"commit":   version.ShortCommit(),
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
