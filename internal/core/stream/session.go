package stream

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ratsdev/tvr/internal/core/transcode"
	"github.com/ratsdev/tvr/internal/core/upstream"
)

type viewer struct {
	id           int64
	ch           chan []byte
	closed       atomic.Bool
	waitKeyframe bool
}

type session struct {
	channelID    string
	epoch        uint64
	opts         Options
	profile      transcode.Profile
	readyTimeout time.Duration
	onIdle       func()
	onFinish     func(Status)
	source       upstream.Source

	mu                  sync.Mutex
	upstream            upstream.Upstream
	currentIndex        int
	candidateIndex      int
	attemptsBeforeReady int
	readyWatch          *time.Timer
	viewers             map[int64]*viewer
	nextID              int64
	state               string
	lastError           string
	bytesSent           int64
	connected           time.Time
	pat                 []byte
	pmts                map[uint16][]byte
	seenRAI             bool
	pumpCancel          context.CancelFunc
	stopCh              chan struct{}
	doneCh              chan struct{}
	stopped             atomic.Bool
	attemptTimedOut     atomic.Bool

	readyCh   chan struct{}
	readyErr  error
	readyOnce sync.Once
	everReady atomic.Bool
}

func newSession(channelID string, src upstream.Source, opts Options, profile transcode.Profile, onIdle func()) *session {
	src, _ = upstream.Normalize(src)
	readyTimeout := opts.ConnTimeout
	if src.Transcode {
		readyTimeout = profile.StartupTimeout
		if readyTimeout <= 0 {
			readyTimeout = 30 * time.Second
		}
	}
	s := &session{
		channelID:    channelID,
		opts:         opts,
		profile:      profile,
		readyTimeout: readyTimeout,
		onIdle:       onIdle,
		source:       src,
		currentIndex: src.StartIndex(),
		viewers:      make(map[int64]*viewer),
		pmts:         make(map[uint16][]byte),
		state:        "connecting",
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
		readyCh:      make(chan struct{}),
	}
	s.applyCurrentUpstreamLocked()
	return s
}

func (s *session) applyCurrentUpstreamLocked() {
	if len(s.source.Upstreams) == 0 {
		return
	}
	idx := s.currentIndex
	if idx < 0 || idx >= len(s.source.Upstreams) {
		idx = 0
		s.currentIndex = 0
	}
	up := s.source.Upstreams[idx]
	cands := up.FetchURLs()
	if s.candidateIndex < 0 || s.candidateIndex >= len(cands) {
		s.candidateIndex = 0
	}
	if len(cands) > 0 {
		up.URL = cands[s.candidateIndex]
	}
	up.Transcode = s.source.Transcode
	up.Revision = s.source.Revision
	s.upstream = up
	s.pat = nil
	s.pmts = make(map[uint16][]byte)
	s.seenRAI = false
}

func (s *session) advanceIndex() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.advanceLocked()
}

func (s *session) advanceLocked() {
	n := len(s.source.Upstreams)
	if n == 0 {
		return
	}
	idx := s.currentIndex
	if idx < 0 || idx >= n {
		idx = 0
	}
	cands := s.source.Upstreams[idx].FetchURLs()
	if s.candidateIndex+1 < len(cands) {
		s.candidateIndex++
		return
	}
	s.candidateIndex = 0
	if s.source.IsFallback() {
		s.currentIndex = (idx + 1) % n
	}
}

func (s *session) shouldWalk() bool {
	if s.source.IsFallback() {
		return true
	}
	n := len(s.source.Upstreams)
	if n == 0 {
		return false
	}
	idx := s.currentIndex
	if idx < 0 || idx >= n {
		idx = 0
	}
	return len(s.source.Upstreams[idx].FetchURLs()) > 1
}

func (s *session) attemptBudget() int {
	if s.source.IsFallback() {
		n := 0
		for _, u := range s.source.Upstreams {
			n += max(1, len(u.FetchURLs()))
		}
		return n
	}
	n := len(s.source.Upstreams)
	if n == 0 {
		return 1
	}
	idx := s.currentIndex
	if idx < 0 || idx >= n {
		idx = 0
	}
	return max(1, len(s.source.Upstreams[idx].FetchURLs()))
}

func (s *session) waitDone(ctx context.Context) error {
	select {
	case <-s.doneCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// finishStatus reports the terminal status before stop() forces state to idle.
func (s *session) finishStatus() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{
		ChannelID:    s.channelID,
		Viewers:      0,
		LastError:    s.lastError,
		BytesSent:    s.bytesSent,
		UpstreamID:   s.upstream.ID,
		UpstreamHost: upstream.Host(s.upstream.URL),
	}
	if !s.connected.IsZero() {
		st.ConnectedAt = s.connected.UTC().Format(time.RFC3339)
	}
	// Intentional teardown unblocks waiters with errSessionStopped; do not sticky it.
	if errors.Is(s.readyErr, errSessionStopped) {
		st.State = "idle"
		st.LastError = ""
		return st
	}
	if s.readyErr != nil || s.state == "error" || (!s.everReady.Load() && strings.TrimSpace(s.lastError) != "") {
		st.State = "error"
		if st.LastError == "" && s.readyErr != nil {
			st.LastError = s.readyErr.Error()
		}
		return st
	}
	st.State = "idle"
	st.LastError = ""
	return st
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

	// Transcoded late joiners wait for a keyframe and get PAT/PMT with it.
	// Pass-through keeps the old PAT/PMT-then-live behavior so a one-shot RAI
	// cannot stall the viewer until the next (possibly never) random-access packet.
	if s.source.Transcode && s.everReady.Load() && s.seenRAI {
		v.waitKeyframe = true
		return v
	}
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
		s.opts.Logger.Debug("stream channel idle", "channel_id", s.channelID)
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
		// Unblock Subscribe waiters without treating teardown as an upstream failure.
		s.failReady(errSessionStopped)
	}
}

func (s *session) status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.state
	viewers := len(s.viewers)
	lastError := s.lastError
	// Never report "streaming"/"connecting" with zero viewers.
	if viewers == 0 && (state == "streaming" || state == "connecting") {
		state = "idle"
	}
	switch {
	case errors.Is(s.readyErr, errSessionStopped):
		if viewers == 0 {
			state = "idle"
			lastError = ""
		}
	case s.readyErr != nil && !s.everReady.Load():
		// Keep readiness failures visible until reaped into sticky status.
		state = "error"
		if lastError == "" {
			lastError = s.readyErr.Error()
		}
	}
	st := Status{
		ChannelID:    s.channelID,
		State:        state,
		Viewers:      viewers,
		LastError:    lastError,
		BytesSent:    s.bytesSent,
		UpstreamID:   s.upstream.ID,
		UpstreamHost: upstream.Host(s.upstream.URL),
	}
	if !s.connected.IsZero() {
		st.ConnectedAt = s.connected.UTC().Format(time.RFC3339)
	}
	return st
}

// markReady unblocks Subscribe waiters once. Session status is owned by setState.
func (s *session) markReady() {
	s.stopReadyWatch()
	s.readyOnce.Do(func() {
		s.everReady.Store(true)
		s.mu.Lock()
		s.connected = time.Now().UTC()
		s.mu.Unlock()
		close(s.readyCh)
	})
}

func (s *session) stopReadyWatch() {
	s.mu.Lock()
	t := s.readyWatch
	s.readyWatch = nil
	s.mu.Unlock()
	if t != nil {
		t.Stop()
	}
}

func (s *session) armReadyWatch(cancel context.CancelFunc) {
	s.attemptTimedOut.Store(false)
	timeout := s.readyTimeout
	if timeout <= 0 {
		timeout = s.opts.ConnTimeout
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	t := time.AfterFunc(timeout, func() {
		if s.everReady.Load() || s.stopped.Load() {
			return
		}
		s.attemptTimedOut.Store(true)
		cancel()
	})
	s.mu.Lock()
	if s.readyWatch != nil {
		s.readyWatch.Stop()
	}
	s.readyWatch = t
	s.mu.Unlock()
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
		return mapReadyErr(s.readyErr)
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.readyCh:
		return mapReadyErr(s.readyErr)
	}
}

func mapReadyErr(err error) error {
	if errors.Is(err, errSessionStopped) {
		return ErrUpstreamFailed
	}
	return err
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
