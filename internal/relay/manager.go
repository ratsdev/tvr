package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const (
	mpegTSPacketSize = 188
	patPID           = 0x0000
	maxProbeBytes    = 256 << 10
)

// Typed Subscribe/readiness errors for the HTTP layer.
var (
	ErrClosed         = errors.New("relay manager closed")
	ErrUpstreamFailed = errors.New("relay upstream failed")
	ErrReadyTimeout   = errors.New("relay readiness timeout")
)

var errStreamEnded = errors.New("stream ended")

// Status describes the current state of a channel relay session.
type Status struct {
	ChannelID   string `json:"channel_id"`
	State       string `json:"state"`
	Viewers     int    `json:"viewers"`
	LastError   string `json:"last_error,omitempty"`
	BytesSent   int64  `json:"bytes_sent"`
	ConnectedAt string `json:"connected_at,omitempty"`
}

// Upstream describes how to connect to a channel source.
type Upstream struct {
	URL     string
	Headers map[string]string
}

// Options configures relay behavior.
type Options struct {
	BufferSize  int
	IdleTimeout time.Duration
	ConnTimeout time.Duration
	Logger      *slog.Logger
	HTTPClient  *http.Client
}

// Manager owns one shared upstream session per channel.
type Manager struct {
	opts     Options
	mu       sync.Mutex
	sessions map[string]*session
	closed   bool
}

// NewManager creates a relay manager.
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
		opts:     opts,
		sessions: make(map[string]*session),
	}
}

// Subscribe attaches a viewer to the shared upstream for a channel.
// It blocks until the session is ready (first valid media queued) or fails.
// The returned reader yields MPEG-TS bytes until the context is cancelled
// or the viewer is dropped for being too slow.
func (m *Manager) Subscribe(ctx context.Context, channelID string, upstream Upstream) (io.ReadCloser, error) {
	if channelID == "" {
		return nil, fmt.Errorf("invalid channel id")
	}
	if upstream.URL == "" {
		return nil, fmt.Errorf("upstream url is required")
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	s, ok := m.sessions[channelID]
	if ok && s.isStopped() {
		delete(m.sessions, channelID)
		ok = false
	}
	if !ok {
		s = newSession(channelID, upstream, m.opts, nil)
		s.onIdle = func() { m.removeSession(s) }
		m.sessions[channelID] = s
		go s.run()
	} else {
		s.updateUpstream(upstream)
	}
	viewer := s.addViewer(m.opts.BufferSize)
	m.mu.Unlock()

	reader := &viewerReader{ctx: ctx, session: s, viewer: viewer}
	if err := s.waitReady(ctx); err != nil {
		_ = reader.Close()
		return nil, err
	}
	return reader, nil
}

// Status returns the current status for a channel, or idle if no session.
func (m *Manager) Status(channelID string) Status {
	m.mu.Lock()
	s := m.sessions[channelID]
	m.mu.Unlock()
	if s == nil {
		return Status{ChannelID: channelID, State: "idle"}
	}
	return s.status()
}

// AllStatuses returns status for every active session.
func (m *Manager) AllStatuses() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s.status())
	}
	return out
}

// Close stops subscription admission and all sessions.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	sessions := make([]*session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.sessions = make(map[string]*session)
	m.mu.Unlock()
	for _, s := range sessions {
		s.stop()
	}
}

func (m *Manager) removeSession(target *session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.sessions[target.channelID]; ok && cur == target {
		delete(m.sessions, target.channelID)
	}
}

type viewer struct {
	id     int64
	ch     chan []byte
	closed atomic.Bool
}

type session struct {
	channelID string
	opts      Options
	onIdle    func()

	mu         sync.Mutex
	upstream   Upstream
	viewers    map[int64]*viewer
	nextID     int64
	state      string
	lastError  string
	bytesSent  int64
	connected  time.Time
	pat        []byte
	pmts       map[uint16][]byte
	pumpCancel context.CancelFunc
	stopCh     chan struct{}
	stopped    atomic.Bool

	readyCh   chan struct{}
	readyErr  error
	readyOnce sync.Once
	everReady atomic.Bool
}

func newSession(channelID string, upstream Upstream, opts Options, onIdle func()) *session {
	return &session{
		channelID: channelID,
		opts:      opts,
		onIdle:    onIdle,
		upstream:  upstream,
		viewers:   make(map[int64]*viewer),
		pmts:      make(map[uint16][]byte),
		state:     "connecting",
		stopCh:    make(chan struct{}),
		readyCh:   make(chan struct{}),
	}
}

func (s *session) updateUpstream(u Upstream) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upstream = u
}

func (s *session) addViewer(bufferSize int) *viewer {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	v := &viewer{
		id: s.nextID,
		ch: make(chan []byte, bufferSize),
	}
	s.viewers[v.id] = v

	// Late joiners get cached PAT/PMT first.
	startup := s.startupPacketsLocked()
	if len(startup) > 0 {
		select {
		case v.ch <- startup:
		default:
		}
	}
	return v
}

func (s *session) removeViewer(id int64) {
	s.mu.Lock()
	v, ok := s.viewers[id]
	if ok {
		delete(s.viewers, id)
		if !v.closed.Swap(true) {
			close(v.ch)
		}
	}
	empty := len(s.viewers) == 0
	var cancel context.CancelFunc
	if empty {
		s.state = "idle"
		cancel = s.pumpCancel
		s.pumpCancel = nil
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		s.opts.Logger.Debug("relay channel idle", "channel_id", s.channelID)
	}
}

func (s *session) viewerCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.viewers)
}

func (s *session) isStopped() bool {
	return s.stopped.Load()
}

func (s *session) stop() {
	if s.stopped.Swap(true) {
		return
	}
	close(s.stopCh)
	s.mu.Lock()
	cancel := s.pumpCancel
	s.pumpCancel = nil
	for id, v := range s.viewers {
		if !v.closed.Swap(true) {
			close(v.ch)
		}
		delete(s.viewers, id)
	}
	s.state = "idle"
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if !s.everReady.Load() {
		s.failReady(ErrUpstreamFailed)
	}
}

func (s *session) status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.state
	viewers := len(s.viewers)
	// Never report "streaming"/"connecting" with zero viewers.
	if viewers == 0 && (state == "streaming" || state == "connecting") {
		state = "idle"
	}
	st := Status{
		ChannelID: s.channelID,
		State:     state,
		Viewers:   viewers,
		LastError: s.lastError,
		BytesSent: s.bytesSent,
	}
	if !s.connected.IsZero() {
		st.ConnectedAt = s.connected.UTC().Format(time.RFC3339)
	}
	return st
}

// markReady unblocks Subscribe waiters once. Session status is owned by setState.
func (s *session) markReady() {
	s.readyOnce.Do(func() {
		s.everReady.Store(true)
		s.mu.Lock()
		s.connected = time.Now().UTC()
		s.mu.Unlock()
		close(s.readyCh)
	})
}

func (s *session) failReady(err error) {
	s.readyOnce.Do(func() {
		if err == nil {
			err = ErrUpstreamFailed
		}
		s.readyErr = err
		s.mu.Lock()
		s.state = "error"
		s.lastError = err.Error()
		s.mu.Unlock()
		close(s.readyCh)
	})
}

func (s *session) waitReady(ctx context.Context) error {
	select {
	case <-s.readyCh:
		return s.readyErr
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.readyCh:
		return s.readyErr
	}
}

// beginPump returns a context cancelled when the session stops or the last viewer leaves.
func (s *session) beginPump() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if s.pumpCancel != nil {
		prev := s.pumpCancel
		s.pumpCancel = nil
		s.mu.Unlock()
		prev()
		s.mu.Lock()
	}
	s.pumpCancel = cancel
	s.mu.Unlock()

	go func() {
		select {
		case <-s.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func (s *session) setState(state, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.viewers) == 0 && (state == "streaming" || state == "connecting") {
		state = "idle"
	}
	if s.state == state && s.lastError == errMsg {
		return
	}
	s.state = state
	s.lastError = errMsg
}

func (s *session) run() {
	defer func() {
		if !s.everReady.Load() {
			s.failReady(ErrUpstreamFailed)
		}
		s.stop()
		if s.onIdle != nil {
			s.onIdle()
		}
	}()

	go s.readyDeadline()

	backoff := time.Second
	idleTimer := time.NewTimer(s.opts.IdleTimeout)
	defer idleTimer.Stop()

	for {
		if s.viewerCount() == 0 {
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(s.opts.IdleTimeout)
			select {
			case <-s.stopCh:
				return
			case <-idleTimer.C:
				if s.viewerCount() == 0 {
					s.opts.Logger.Debug("relay session idle timeout", "channel_id", s.channelID)
					return
				}
			case <-time.After(200 * time.Millisecond):
				if s.viewerCount() == 0 {
					continue
				}
			}
		}

		err := s.pumpOnce()
		if s.stopped.Load() {
			return
		}
		if s.viewerCount() == 0 {
			continue
		}
		if err != nil {
			if errors.Is(err, errStreamEnded) {
				return
			}
			if !s.everReady.Load() {
				s.failReady(fmt.Errorf("%w: %v", ErrUpstreamFailed, err))
				return
			}
			s.setState("error", err.Error())
			s.opts.Logger.Warn("relay upstream error", "channel_id", s.channelID, "err", err)
			select {
			case <-s.stopCh:
				return
			case <-time.After(backoff):
			}
			if backoff < 15*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
	}
}

func (s *session) readyDeadline() {
	t := time.NewTimer(s.opts.ConnTimeout)
	defer t.Stop()
	select {
	case <-s.readyCh:
	case <-s.stopCh:
	case <-t.C:
		if !s.everReady.Load() {
			s.failReady(ErrReadyTimeout)
			s.stop()
		}
	}
}

func (s *session) pumpOnce() error {
	s.mu.Lock()
	up := s.upstream
	s.mu.Unlock()

	ctx, cancel := s.beginPump()
	defer cancel()

	s.setState("connecting", "")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, up.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "tvr/1.0")
	req.Header.Set("Accept", "*/*")
	for k, v := range up.Headers {
		req.Header.Set(k, v)
	}

	resp, err := s.opts.HTTPClient.Do(req)
	if err != nil {
		if s.stopped.Load() || errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return fmt.Errorf("upstream status %d", resp.StatusCode)
	}

	peek := make([]byte, 1024)
	n, peekErr := io.ReadFull(resp.Body, peek)
	if n > 0 {
		peek = peek[:n]
	} else {
		peek = nil
	}
	if peekErr != nil && !errors.Is(peekErr, io.EOF) && !errors.Is(peekErr, io.ErrUnexpectedEOF) {
		_ = resp.Body.Close()
		if s.stopped.Load() || errors.Is(peekErr, context.Canceled) {
			return nil
		}
		return peekErr
	}

	finalURL := up.URL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}

	if looksLikeHLS(resp.Header.Get("Content-Type"), peek) {
		rest, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if err != nil {
			return err
		}
		body := append(peek, rest...)
		s.opts.Logger.Debug("hls relay", "channel_id", s.channelID, "url", finalURL)
		return s.pumpHLS(ctx, finalURL, req.Header.Clone(), body)
	}

	defer resp.Body.Close()
	body := io.MultiReader(bytes.NewReader(peek), resp.Body)
	return s.pumpMPEGTS(ctx, body)
}

func (s *session) pumpMPEGTS(ctx context.Context, body io.Reader) error {
	return s.copyMPEGTS(ctx, body, mpegTSCopyOptions{})
}

type mpegTSCopyOptions struct {
	// maxBytes limits how much may be read; 0 means unlimited.
	maxBytes int64
	// segment treats EOF as normal completion after media (HLS media segments).
	segment bool
}

// copyMPEGTS frames and broadcasts MPEG-TS as it arrives from body.
func (s *session) copyMPEGTS(ctx context.Context, body io.Reader, opts mpegTSCopyOptions) error {
	buf := make([]byte, mpegTSPacketSize*32)
	carry := make([]byte, 0, mpegTSPacketSize)
	probed := 0
	var total int64
	gotMedia := false

	for {
		select {
		case <-s.stopCh:
			return nil
		case <-ctx.Done():
			return nil
		default:
		}
		if s.viewerCount() == 0 {
			return nil
		}

		n, readErr := body.Read(buf)
		if n > 0 {
			if opts.maxBytes > 0 {
				total += int64(n)
				if total > opts.maxBytes {
					return fmt.Errorf("segment exceeds %d bytes", opts.maxBytes)
				}
			}
			data := append(carry, buf[:n]...)
			framed, rest := extractFramedMPEGTS(data)
			carry = rest
			if len(framed) > 0 {
				s.broadcastFramed(framed)
				gotMedia = true
				s.setState("streaming", "")
				s.markReady()
			} else if !opts.segment && !s.everReady.Load() {
				probed += n
				if probed > maxProbeBytes {
					return fmt.Errorf("no mpeg-ts sync in probe window")
				}
			}
		}
		if readErr != nil {
			if s.stopped.Load() || errors.Is(readErr, context.Canceled) {
				return nil
			}
			if errors.Is(readErr, io.EOF) {
				if opts.segment {
					if !gotMedia {
						return fmt.Errorf("segment is not mpeg-ts")
					}
					return nil
				}
				if !s.everReady.Load() {
					return fmt.Errorf("upstream closed before mpeg-ts ready")
				}
				return fmt.Errorf("upstream closed")
			}
			return readErr
		}
	}
}

// extractFramedMPEGTS returns the longest leading run of complete 188-byte
// packets that start with a sync byte, plus any remaining trailing bytes.
func extractFramedMPEGTS(data []byte) (framed, carry []byte) {
	start := -1
	for i := 0; i < len(data); i++ {
		if data[i] == 0x47 {
			start = i
			break
		}
	}
	if start < 0 {
		if len(data) > mpegTSPacketSize-1 {
			return nil, append([]byte(nil), data[len(data)-(mpegTSPacketSize-1):]...)
		}
		return nil, append([]byte(nil), data...)
	}
	end := start
	for end+mpegTSPacketSize <= len(data) {
		if data[end] != 0x47 {
			break
		}
		end += mpegTSPacketSize
	}
	if end == start {
		return nil, append([]byte(nil), data[start:]...)
	}
	return data[start:end], append([]byte(nil), data[end:]...)
}

// framedMPEGTS validates a complete buffer as MPEG-TS and returns aligned packets.
// Trailing padding after a valid TS prefix is discarded.
func framedMPEGTS(data []byte) ([]byte, bool) {
	framed, _ := extractFramedMPEGTS(data)
	if len(framed) < mpegTSPacketSize {
		return nil, false
	}
	return framed, true
}

func (s *session) observeFramed(data []byte) {
	for i := 0; i+mpegTSPacketSize <= len(data); i += mpegTSPacketSize {
		s.observePacket(data[i : i+mpegTSPacketSize])
	}
}

// broadcastFramed observes PAT/PMT and queues data in live-sized bursts.
func (s *session) broadcastFramed(data []byte) {
	s.observeFramed(data)
	for len(data) >= mpegTSPacketSize {
		burst := mpegTSPacketSize * 32
		if burst > len(data) {
			burst = len(data) - (len(data) % mpegTSPacketSize)
		} else {
			burst = burst - (burst % mpegTSPacketSize)
		}
		if burst < mpegTSPacketSize {
			break
		}
		chunk := append([]byte(nil), data[:burst]...)
		data = data[burst:]
		s.broadcast(chunk)
	}
}

func (s *session) observePacket(pkt []byte) {
	if len(pkt) != mpegTSPacketSize || pkt[0] != 0x47 {
		return
	}
	pid := uint16(pkt[1]&0x1f)<<8 | uint16(pkt[2])
	pusi := pkt[1]&0x40 != 0
	if !pusi {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if pid == patPID {
		s.pat = append([]byte(nil), pkt...)
		return
	}
	// Heuristic: treat packets with table_id 0x02 as PMT.
	payloadStart := 4
	if pkt[3]&0x20 != 0 { // adaptation field
		afLen := int(pkt[4])
		payloadStart = 5 + afLen
	}
	if payloadStart >= len(pkt) {
		return
	}
	// pointer_field
	pointer := int(pkt[payloadStart])
	i := payloadStart + 1 + pointer
	if i >= len(pkt) {
		return
	}
	if pkt[i] == 0x02 {
		s.pmts[pid] = append([]byte(nil), pkt...)
	}
}

func (s *session) startupPacketsLocked() []byte {
	if len(s.pat) == 0 && len(s.pmts) == 0 {
		return nil
	}
	out := make([]byte, 0, mpegTSPacketSize*(1+len(s.pmts)))
	if len(s.pat) == mpegTSPacketSize {
		out = append(out, s.pat...)
	}
	for _, pmt := range s.pmts {
		if len(pmt) == mpegTSPacketSize {
			out = append(out, pmt...)
		}
	}
	return out
}

func (s *session) broadcast(pkt []byte) {
	s.mu.Lock()
	s.bytesSent += int64(len(pkt))
	var cancel context.CancelFunc
	for id, v := range s.viewers {
		select {
		case v.ch <- pkt:
		default:
			// Slow client: drop them so they don't block the upstream.
			s.opts.Logger.Debug("dropping slow viewer", "channel_id", s.channelID, "viewer_id", id)
			if !v.closed.Swap(true) {
				close(v.ch)
			}
			delete(s.viewers, id)
		}
	}
	if len(s.viewers) == 0 {
		s.state = "idle"
		cancel = s.pumpCancel
		s.pumpCancel = nil
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

type viewerReader struct {
	ctx       context.Context
	session   *session
	viewer    *viewer
	buf       []byte
	closeOnce sync.Once
}

func (r *viewerReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		select {
		case <-r.ctx.Done():
			_ = r.Close()
			return 0, r.ctx.Err()
		case chunk, ok := <-r.viewer.ch:
			if !ok {
				_ = r.Close()
				return 0, io.EOF
			}
			r.buf = chunk
		}
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

func (r *viewerReader) Close() error {
	r.closeOnce.Do(func() {
		r.session.removeViewer(r.viewer.id)
	})
	return nil
}
