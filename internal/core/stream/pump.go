package stream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ratsdev/tvr/internal/core/upstream"
)

func (s *session) run() {
	defer func() {
		if !s.everReady.Load() {
			s.failReady(ErrUpstreamFailed)
		}
		st := s.finishStatus()
		s.stop()
		if s.onIdle != nil {
			s.onIdle()
		}
		if s.onFinish != nil {
			s.onFinish(st)
		}
		close(s.doneCh)
	}()

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
					s.opts.Logger.Debug("stream session idle timeout", "channel_id", s.channelID)
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
			if s.handlePumpError(err, &backoff) {
				return
			}
			continue
		}
		backoff = time.Second
	}
}

func (s *session) handlePumpError(err error, backoff *time.Duration) (stop bool) {
	walk := s.shouldWalk()
	if !s.everReady.Load() {
		if walk {
			s.attemptsBeforeReady++
			if s.attemptsBeforeReady >= s.attemptBudget() {
				s.failReady(fmt.Errorf("%w: %w", ErrUpstreamFailed, err))
				return true
			}
			s.advanceIndex()
			return false
		}
		s.failReady(fmt.Errorf("%w: %w", ErrUpstreamFailed, err))
		return true
	}
	if errors.Is(err, errStreamEnded) && !walk {
		return true
	}
	if walk {
		s.advanceIndex()
	}
	s.setState("error", err.Error())
	s.opts.Logger.Warn("stream upstream error", "channel_id", s.channelID, "err", err)
	select {
	case <-s.stopCh:
		return true
	case <-time.After(*backoff):
	}
	if *backoff < 15*time.Second {
		*backoff *= 2
	}
	return false
}

func (s *session) pumpOnce() error {
	s.mu.Lock()
	s.applyCurrentUpstreamLocked()
	up := s.upstream
	s.mu.Unlock()

	ctx, cancel := s.beginPump()
	defer cancel()
	s.armReadyWatch(cancel)
	defer s.stopReadyWatch()

	var err error
	if s.source.Transcode {
		err = s.pumpTranscode(ctx)
	} else {
		err = s.pumpPassThrough(ctx, up)
	}
	return s.classifyPumpErr(err)
}

func (s *session) classifyPumpErr(err error) error {
	if s.stopped.Load() || s.viewerCount() == 0 {
		return nil
	}
	if s.attemptTimedOut.Load() && !s.everReady.Load() {
		return ErrReadyTimeout
	}
	return err
}

func (s *session) pumpPassThrough(ctx context.Context, up upstream.Upstream) error {
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
		s.opts.Logger.Debug("hls stream", "channel_id", s.channelID, "url", finalURL)
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
