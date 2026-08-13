package epg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jqjiang/tvr/internal/core/store"
)

// ErrAdmissionClosed is returned when the EPG worker no longer accepts work.
var ErrAdmissionClosed = errors.New("epg worker admission closed")

const cleanupRetryDelay = time.Second

type relayCleanupState struct {
	Slugs      map[string]struct{}
	RetryAfter time.Time
}

// AcquireAdmission reserves the right to mutate and enqueue derived-file work.
// Call release after commit+enqueue. ok=false means admission is closed.
func (s *Service) AcquireAdmission() (release func(), ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.admitOpen {
		return nil, false
	}
	s.admitHeld++
	return func() {
		s.mu.Lock()
		if s.admitHeld > 0 {
			s.admitHeld--
		}
		s.mu.Unlock()
	}, true
}

// CloseAdmission rejects new admission and enqueue after in-flight holders finish enqueueing.
func (s *Service) CloseAdmission() {
	s.mu.Lock()
	s.admitOpen = false
	s.mu.Unlock()
}

// WaitAdmissionDrained blocks until no admission holders remain or ctx is done.
func (s *Service) WaitAdmissionDrained(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		held := s.admitHeld
		s.mu.Unlock()
		if held == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// DrainPending runs a quiescent drain of currently queued derived work.
func (s *Service) DrainPending(ctx context.Context) error {
	s.wakeWorker()
	return s.drain(ctx)
}

// Wait blocks until the worker goroutine exits.
func (s *Service) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return nil
	}
}

func (s *Service) enqueueLocked(kind string, id int64) bool {
	if !s.admitOpen && s.admitHeld == 0 {
		return false
	}
	switch kind {
	case "source":
		s.pendingSources[id] = struct{}{}
	case "relay":
		s.pendingRelays[id] = struct{}{}
	case "deleteSource":
		s.pendingDeleteSources[id] = struct{}{}
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return true
}

func (s *Service) EnqueueRefreshSource(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enqueueLocked("source", id) {
		return ErrAdmissionClosed
	}
	return nil
}

func (s *Service) EnqueueRefreshEnabled(ctx context.Context) error {
	sources, err := s.store.ListEnabledEPGSources(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, src := range sources {
		if !s.enqueueLocked("source", src.ID) {
			return ErrAdmissionClosed
		}
	}
	return nil
}

func (s *Service) EnqueueRefreshDue(ctx context.Context) error {
	sources, err := s.store.ListEnabledEPGSources(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, src := range sources {
		interval := src.RefreshInterval
		if interval < time.Minute {
			interval = time.Minute
		}
		due := false
		if src.LastRefreshAt == nil {
			due = true
		} else if now.Sub(*src.LastRefreshAt) >= interval {
			due = true
		}
		if !due {
			continue
		}
		if retry, ok := s.nextRetry[src.ID]; ok && now.Before(retry) {
			continue
		}
		if !s.enqueueLocked("source", src.ID) {
			return ErrAdmissionClosed
		}
	}
	return nil
}

func (s *Service) EnqueueRebuildRelays(ids []int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if !s.enqueueLocked("relay", id) {
			return ErrAdmissionClosed
		}
	}
	return nil
}

func (s *Service) EnqueueRelayCleanup(relayID int64, oldSlug string) error {
	oldSlug = strings.TrimSpace(oldSlug)
	if relayID <= 0 || oldSlug == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.cleanups[relayID]
	if state == nil {
		state = &relayCleanupState{Slugs: map[string]struct{}{}}
		s.cleanups[relayID] = state
	}
	state.Slugs[oldSlug] = struct{}{}
	state.RetryAfter = time.Time{}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	if !s.admitOpen && s.admitHeld == 0 {
		return ErrAdmissionClosed
	}
	return nil
}

func (s *Service) EnqueueDeleteSourceFiles(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sourceRev[id]++
	delete(s.indexes, id)
	if !s.enqueueLocked("deleteSource", id) {
		return ErrAdmissionClosed
	}
	return nil
}

// InvalidateSource makes the current generation ineligible without wiping relay XML.
func (s *Service) InvalidateSource(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sourceRev[id]++
	delete(s.indexes, id)
}

func (s *Service) currentSourceRev(id int64) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sourceRev[id]
}

func (s *Service) wakeWorker() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) loop(ctx context.Context) {
	defer close(s.done)
	timer := time.NewTimer(s.nextDelay(ctx))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			// Bound exit work so shutdown cannot hang on upstream fetches.
			// Primary flush should already have run via DrainPending before cancel.
			drainCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := s.drain(drainCtx); err != nil {
				s.logger.Warn("epg drain on cancel", "err", err)
			}
			cancel()
			return
		case <-s.wake:
			if err := s.drain(ctx); err != nil && !errors.Is(err, context.Canceled) {
				s.logger.Warn("epg drain", "err", err)
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(s.nextDelay(ctx))
		case <-timer.C:
			if err := s.EnqueueRefreshDue(ctx); err != nil && !errors.Is(err, ErrAdmissionClosed) {
				s.logger.Warn("epg enqueue due", "err", err)
			}
			if err := s.drain(ctx); err != nil && !errors.Is(err, context.Canceled) {
				s.logger.Warn("epg drain", "err", err)
			}
			timer.Reset(s.nextDelay(ctx))
		}
	}
}

func (s *Service) drain(ctx context.Context) error {
	s.drainMu.Lock()
	defer s.drainMu.Unlock()
	s.mu.Lock()
	s.busy = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()
	}()

	var firstErr error
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		worked := false

		for {
			id, ok := s.takePending("deleteSource")
			if !ok {
				break
			}
			worked = true
			s.deleteSourceFiles(id)
		}

		for {
			id, ok := s.takePending("source")
			if !ok {
				break
			}
			worked = true
			if err := s.processSource(ctx, id); err != nil && firstErr == nil {
				firstErr = err
			}
		}

		// Rebuild before cleanup so slug renames publish the new file first.
		for {
			id, ok := s.takePending("relay")
			if !ok {
				break
			}
			worked = true
			if err := s.processRelay(ctx, id); err != nil && firstErr == nil {
				firstErr = err
			}
		}

		for {
			relayID, slugs, ok := s.takeCleanupReady()
			if !ok {
				break
			}
			worked = true
			s.processCleanup(relayID, slugs)
		}

		if !worked {
			break
		}
	}
	return firstErr
}

func (s *Service) takePending(kind string) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var m map[int64]struct{}
	switch kind {
	case "source":
		m = s.pendingSources
	case "relay":
		m = s.pendingRelays
	case "deleteSource":
		m = s.pendingDeleteSources
	default:
		return 0, false
	}
	for id := range m {
		delete(m, id)
		return id, true
	}
	return 0, false
}

// takeCleanupReady removes one ready cleanup entry and returns a copy of its slugs.
// Deferred retries (RetryAfter in the future) are skipped so drain cannot live-lock.
func (s *Service) takeCleanupReady() (int64, map[string]struct{}, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, c := range s.cleanups {
		if c == nil || len(c.Slugs) == 0 {
			delete(s.cleanups, id)
			continue
		}
		if !c.RetryAfter.IsZero() && now.Before(c.RetryAfter) {
			continue
		}
		slugs := make(map[string]struct{}, len(c.Slugs))
		for slug := range c.Slugs {
			slugs[slug] = struct{}{}
		}
		delete(s.cleanups, id)
		return id, slugs, true
	}
	return 0, nil, false
}

func (s *Service) deferCleanup(relayID int64, slugs map[string]struct{}) {
	if relayID <= 0 || len(slugs) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.cleanups[relayID]
	if state == nil {
		state = &relayCleanupState{Slugs: map[string]struct{}{}}
		s.cleanups[relayID] = state
	}
	for slug := range slugs {
		state.Slugs[slug] = struct{}{}
	}
	state.RetryAfter = time.Now().Add(cleanupRetryDelay)
}

func (s *Service) processSource(ctx context.Context, id int64) error {
	rev := s.currentSourceRev(id)
	src, err := s.store.GetEPGSource(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		s.deleteSourceFiles(id)
		return nil
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	if retry, ok := s.nextRetry[id]; ok && time.Now().Before(retry) {
		// Leave it out of pendingSources; nextDelay will wake when retry is due.
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	if ctx.Err() != nil {
		return ctx.Err()
	}

	now := time.Now().UTC()
	doc, fetchErr := s.fetchSource(ctx, src)
	if fetchErr != nil {
		_ = s.store.MarkEPGSourceRefreshError(ctx, id, fetchErr.Error())
		s.mu.Lock()
		backoff := s.failBackoff[id]
		if backoff <= 0 {
			backoff = time.Second
		} else if backoff < 5*time.Minute {
			backoff *= 2
		}
		s.failBackoff[id] = backoff
		s.nextRetry[id] = time.Now().Add(backoff)
		s.lastError = fetchErr.Error()
		s.mu.Unlock()
		return fmt.Errorf("source %q: %w", src.Name, fetchErr)
	}

	// Discard stale work if URL/revision changed during fetch.
	src2, err := s.store.GetEPGSource(ctx, id)
	if err != nil {
		return err
	}
	if s.currentSourceRev(id) != rev || normalizeSourceURL(src2.URL) != normalizeSourceURL(src.URL) {
		return nil
	}

	gen, channels, err := s.writeSourceGenerationFiles(id, src2.URL, doc)
	if err != nil {
		_ = s.store.MarkEPGSourceRefreshError(ctx, id, err.Error())
		return err
	}
	// Publish manifest only if the captured revision/URL is still current.
	if s.currentSourceRev(id) != rev {
		_ = os.Remove(s.sourceGenerationXMLPath(id, gen))
		_ = os.Remove(s.sourceGenerationIndexPath(id, gen))
		return nil
	}
	src3, err := s.store.GetEPGSource(ctx, id)
	if err != nil {
		_ = os.Remove(s.sourceGenerationXMLPath(id, gen))
		_ = os.Remove(s.sourceGenerationIndexPath(id, gen))
		return err
	}
	if normalizeSourceURL(src3.URL) != normalizeSourceURL(src2.URL) {
		_ = os.Remove(s.sourceGenerationXMLPath(id, gen))
		_ = os.Remove(s.sourceGenerationIndexPath(id, gen))
		return nil
	}
	if err := s.writeSourceManifest(id, sourceManifest{
		Generation: gen,
		SourceURL:  normalizeSourceURL(src2.URL),
		FetchedAt:  now.Format(time.RFC3339),
	}); err != nil {
		_ = os.Remove(s.sourceGenerationXMLPath(id, gen))
		_ = os.Remove(s.sourceGenerationIndexPath(id, gen))
		_ = s.store.MarkEPGSourceRefreshError(ctx, id, err.Error())
		return err
	}
	s.mu.Lock()
	s.indexes[id] = indexTag{SourceURL: normalizeSourceURL(src2.URL), Generation: gen, Channels: channels}
	delete(s.nextRetry, id)
	delete(s.failBackoff, id)
	s.mu.Unlock()
	_ = s.store.MarkEPGSourceRefresh(ctx, id, now, "")

	relayIDs, err := s.relaysReferencingSource(ctx, id)
	if err != nil {
		return err
	}
	if err := s.EnqueueRebuildRelays(relayIDs); err != nil {
		s.logger.Warn("epg enqueue rebuild after source refresh", "source_id", id, "err", err)
	}
	s.setOK()
	return nil
}

func (s *Service) processCleanup(relayID int64, slugs map[string]struct{}) {
	if len(slugs) == 0 {
		return
	}
	ctx := context.Background()
	relay, relayErr := s.store.GetRelay(ctx, relayID)
	relayExists := relayErr == nil
	if relayErr != nil && !errors.Is(relayErr, store.ErrNotFound) {
		s.logger.Warn("epg cleanup relay lookup", "relay_id", relayID, "err", relayErr)
		s.deferCleanup(relayID, slugs)
		return
	}

	pending := map[string]struct{}{}
	for oldSlug := range slugs {
		oldSlug = strings.TrimSpace(oldSlug)
		if oldSlug == "" {
			continue
		}
		// Never delete a cache owned by a live relay (including slug reuse).
		if _, err := s.store.GetRelayBySlug(ctx, oldSlug); err == nil {
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			s.logger.Warn("epg cleanup slug lookup", "slug", oldSlug, "err", err)
			pending[oldSlug] = struct{}{}
			continue
		}

		if relayExists {
			if relay.Slug == oldSlug {
				continue
			}
			newPath := filepath.Join(s.cacheDir, relay.Slug+".xml")
			if _, err := os.Stat(newPath); err != nil {
				// New cache not published yet — retry later without spinning drain.
				pending[oldSlug] = struct{}{}
				continue
			}
		}
		oldPath := filepath.Join(s.cacheDir, oldSlug+".xml")
		if err := os.Remove(oldPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.logger.Warn("epg cleanup remove", "slug", oldSlug, "err", err)
			pending[oldSlug] = struct{}{}
		}
	}
	if len(pending) > 0 {
		s.deferCleanup(relayID, pending)
	}
}

func (s *Service) reconcileStartup(ctx context.Context) {
	sources, err := s.store.ListEPGSources(ctx)
	if err != nil {
		return
	}
	liveSources := map[int64]string{}
	for _, src := range sources {
		liveSources[src.ID] = normalizeSourceURL(src.URL)
		if m, err := s.readSourceManifest(src.ID); err == nil && m.SourceURL == normalizeSourceURL(src.URL) {
			if _, _, tag, ok := s.eligibleSource(src.ID, src.URL); ok {
				s.mu.Lock()
				s.indexes[src.ID] = tag
				s.mu.Unlock()
			}
		}
	}

	relays, err := s.store.ListRelays(ctx)
	if err != nil {
		return
	}
	liveSlugs := map[string]struct{}{}
	ids := make([]int64, 0, len(relays))
	for _, r := range relays {
		liveSlugs[r.Slug] = struct{}{}
		ids = append(ids, r.ID)
	}
	_ = s.EnqueueRebuildRelays(ids)

	// Orphan sweep for source artifacts and unused relay caches.
	entries, _ := os.ReadDir(s.sourceDir)
	for _, e := range entries {
		name := e.Name()
		var id int64
		if _, err := fmt.Sscanf(name, "%d.", &id); err != nil {
			continue
		}
		if _, ok := liveSources[id]; !ok {
			s.deleteSourceFiles(id)
		}
	}
	entries, _ = os.ReadDir(s.cacheDir)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".xml") {
			continue
		}
		slug := strings.TrimSuffix(name, ".xml")
		if _, ok := liveSlugs[slug]; !ok {
			_ = os.Remove(filepath.Join(s.cacheDir, name))
		}
	}
}
