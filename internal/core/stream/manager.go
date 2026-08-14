package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ratsdev/tvr/internal/core/transcode"
	"github.com/ratsdev/tvr/internal/core/upstream"
)

const (
	patPID        = 0x0000
	maxProbeBytes = 256 << 10
)

// Typed Subscribe/readiness errors for the HTTP layer.
var (
	ErrClosed         = errors.New("stream manager closed")
	ErrUpstreamFailed = errors.New("stream upstream failed")
	ErrReadyTimeout   = errors.New("stream readiness timeout")
	ErrStaleRevision  = errors.New("stream channel revision is stale")
	ErrChannelBlocked = errors.New("stream channel is blocked")
)

var (
	errStreamEnded    = errors.New("stream ended")
	errSessionStopped = errors.New("stream session stopped")
)

// Status describes the current state of a channel live session.
type Status struct {
	ChannelID    string `json:"channel_id"`
	State        string `json:"state"`
	Viewers      int    `json:"viewers"`
	LastError    string `json:"last_error,omitempty"`
	BytesSent    int64  `json:"bytes_sent"`
	ConnectedAt  string `json:"connected_at,omitempty"`
	UpstreamID   string `json:"upstream_id,omitempty"`
	UpstreamHost string `json:"upstream_host,omitempty"`
}

// Options configures live session behavior.
type Options struct {
	BufferSize       int
	IdleTimeout      time.Duration
	ConnTimeout      time.Duration
	Logger           *slog.Logger
	HTTPClient       *http.Client
	TranscodeProfile transcode.Profile
}

// Manager owns one shared upstream session per channel.
type Manager struct {
	opts      Options
	mu        sync.Mutex
	sessions  map[string]*session
	revisions map[string]string
	blocked   map[string]struct{}
	sticky    map[string]Status // terminal error status after a session ends
	epochs    map[string]uint64 // per-channel generation for sticky ownership
	profile   transcode.Profile
	closed    bool
}

// NewManager creates a live session manager.
func NewManager(opts Options) *Manager {
	if opts.BufferSize < 8 {
		opts.BufferSize = 1024
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 30 * time.Second
	}
	if opts.ConnTimeout <= 0 {
		opts.ConnTimeout = 10 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	opts.TranscodeProfile = transcode.Normalize(opts.TranscodeProfile)
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{
			Timeout: 0, // live streams must not have a total timeout
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				ResponseHeaderTimeout: opts.ConnTimeout,
				IdleConnTimeout:       90 * time.Second,
				ForceAttemptHTTP2:     true,
			},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return errors.New("too many redirects")
				}
				return nil
			},
		}
	}
	return &Manager{
		opts:      opts,
		sessions:  make(map[string]*session),
		revisions: make(map[string]string),
		blocked:   make(map[string]struct{}),
		sticky:    make(map[string]Status),
		epochs:    make(map[string]uint64),
		profile:   opts.TranscodeProfile,
	}
}

// Subscribe attaches a viewer to the shared upstream for a channel.
// It blocks until the session is ready (first valid media queued) or fails.
// The returned reader yields MPEG-TS bytes until the context is cancelled
// or the viewer is dropped for being too slow.
func (m *Manager) Subscribe(ctx context.Context, channelID string, src upstream.Source) (io.ReadCloser, error) {
	if channelID == "" {
		return nil, fmt.Errorf("invalid channel id")
	}
	src, err := upstream.Normalize(src)
	if err != nil {
		return nil, fmt.Errorf("upstream url is required")
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	if _, blocked := m.blocked[channelID]; blocked {
		m.mu.Unlock()
		return nil, ErrChannelBlocked
	}
	if rev, ok := m.revisions[channelID]; ok && src.Revision != "" && rev != src.Revision {
		m.mu.Unlock()
		return nil, ErrStaleRevision
	}
	if src.Revision != "" {
		m.revisions[channelID] = src.Revision
	}

	var stale *session
	s, ok := m.sessions[channelID]
	if ok && s.isStopped() {
		delete(m.sessions, channelID)
		ok = false
	}
	if ok && s.source.Transcode != src.Transcode {
		stale = m.retireSession(channelID)
		ok = false
	}
	if !ok {
		s = m.startSession(channelID, src)
	}
	viewer := s.addViewer(m.opts.BufferSize)
	m.mu.Unlock()
	if stale != nil {
		stale.stop()
	}

	reader := &viewerReader{ctx: ctx, session: s, viewer: viewer}
	if err := s.waitReady(ctx); err != nil {
		_ = reader.Close()
		return nil, err
	}
	return reader, nil
}

// PublishChannel records the latest channel revision and optionally detaches the active session.
func (m *Manager) PublishChannel(ctx context.Context, channelID, revision string, invalidate bool) error {
	if channelID == "" {
		return fmt.Errorf("invalid channel id")
	}
	m.mu.Lock()
	delete(m.blocked, channelID)
	if revision != "" {
		m.revisions[channelID] = revision
	}
	var stale *session
	if invalidate {
		stale = m.retireSession(channelID)
	}
	m.mu.Unlock()
	return stopAndWait(ctx, stale)
}

// BlockChannel rejects future subscriptions and stops any active session for the channel.
func (m *Manager) BlockChannel(ctx context.Context, channelID string) error {
	if channelID == "" {
		return fmt.Errorf("invalid channel id")
	}
	m.mu.Lock()
	m.blocked[channelID] = struct{}{}
	delete(m.revisions, channelID)
	stale := m.retireSession(channelID)
	m.mu.Unlock()
	return stopAndWait(ctx, stale)
}

// ApplyProfile installs a new transcoder profile and stops active transcoded sessions.
func (m *Manager) ApplyProfile(ctx context.Context, profile transcode.Profile) error {
	profile = transcode.Normalize(profile)
	m.mu.Lock()
	m.profile = profile
	var stale []*session
	for id, s := range m.sessions {
		if !s.source.Transcode {
			continue
		}
		if retired := m.retireSession(id); retired != nil {
			stale = append(stale, retired)
		}
	}
	m.mu.Unlock()
	return stopAndWaitAll(ctx, stale)
}

// Profile returns a copy of the current transcoder profile.
func (m *Manager) Profile() transcode.Profile {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.profile
}

// Status returns the current status for a channel, or idle if no session.
func (m *Manager) Status(channelID string) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sessions[channelID]; s != nil {
		return s.status()
	}
	if st, ok := m.sticky[channelID]; ok {
		return st
	}
	return Status{ChannelID: channelID, State: "idle"}
}

// AllStatuses returns status for every active session plus sticky terminal errors.
func (m *Manager) AllStatuses() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0, len(m.sessions)+len(m.sticky))
	seen := make(map[string]struct{}, len(m.sessions))
	for _, s := range m.sessions {
		st := s.status()
		out = append(out, st)
		seen[st.ChannelID] = struct{}{}
	}
	for id, st := range m.sticky {
		if _, ok := seen[id]; ok {
			continue
		}
		out = append(out, st)
	}
	return out
}

func (m *Manager) rememberFinish(s *session, st Status) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Only the latest session epoch may publish or clear sticky status.
	if m.epochs[s.channelID] != s.epoch {
		return
	}
	if _, active := m.sessions[s.channelID]; active {
		return
	}
	if st.State == "error" && strings.TrimSpace(st.LastError) != "" {
		st.ChannelID = s.channelID
		st.Viewers = 0
		m.sticky[s.channelID] = st
		return
	}
	delete(m.sticky, s.channelID)
}

// startSession creates and starts a new session. Caller must hold m.mu.
func (m *Manager) startSession(channelID string, src upstream.Source) *session {
	delete(m.sticky, channelID)
	m.epochs[channelID]++
	s := newSession(channelID, src, m.opts, m.profile, nil)
	s.epoch = m.epochs[channelID]
	s.onIdle = func() { m.removeSession(s) }
	s.onFinish = func(st Status) { m.rememberFinish(s, st) }
	m.sessions[channelID] = s
	go s.run()
	return s
}

// retireSession detaches a channel session and retires its sticky epoch.
// Caller must hold m.mu. The returned session still needs stop/waitDone.
func (m *Manager) retireSession(channelID string) *session {
	delete(m.sticky, channelID)
	m.epochs[channelID]++
	s := m.sessions[channelID]
	if s != nil {
		delete(m.sessions, channelID)
	}
	return s
}

func stopAndWait(ctx context.Context, s *session) error {
	if s == nil {
		return nil
	}
	return stopAndWaitAll(ctx, []*session{s})
}

func stopAndWaitAll(ctx context.Context, sessions []*session) error {
	for _, s := range sessions {
		s.stop()
	}
	var first error
	for _, s := range sessions {
		if err := s.waitDone(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Close stops subscription admission and waits for all sessions to finish.
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	sessions := make([]*session, 0, len(m.sessions))
	for id := range m.sessions {
		if s := m.retireSession(id); s != nil {
			sessions = append(sessions, s)
		}
	}
	m.mu.Unlock()
	return stopAndWaitAll(ctx, sessions)
}

func (m *Manager) removeSession(target *session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.sessions[target.channelID]; ok && cur == target {
		delete(m.sessions, target.channelID)
	}
}
