package workflows

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/ratsdev/tvr/internal/core/epg"
	"github.com/ratsdev/tvr/internal/core/store"
	"github.com/ratsdev/tvr/internal/utils"
)

// Workflows owns store mutations that enqueue derived EPG work.
type Workflows struct {
	Store              *store.Store
	EPG                *epg.Service
	DefaultEPGInterval time.Duration
	Logger             *slog.Logger
}

// RelaySlugCleanup records an obsolete relay cache slug to remove.
type RelaySlugCleanup struct {
	RelayID int64
	OldSlug string
}

// DerivedWork is the post-commit EPG side-effect plan for a mutation.
type DerivedWork struct {
	RefreshSources    []int64
	InvalidateSources []int64
	DeleteSourceFiles []int64
	RebuildRelays     []int64
	CleanupRelaySlugs []RelaySlugCleanup
	WakeDue           bool
}

func (w *Workflows) logger() *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}

func (w *Workflows) withAdmission(fn func() error) error {
	release, ok := w.EPG.AcquireAdmission()
	if !ok {
		return epg.ErrAdmissionClosed
	}
	defer release()
	return fn()
}

// ApplyDerivedWork enqueues work in a stable order. Errors are logged and returned
// for the caller to decide; HTTP handlers treat post-commit failures as warnings.
func (w *Workflows) ApplyDerivedWork(ctx context.Context, work DerivedWork) error {
	var first error
	note := func(err error) {
		if err == nil {
			return
		}
		if first == nil {
			first = err
		}
		w.logger().Warn("epg derived work", "err", err)
	}
	for _, id := range work.InvalidateSources {
		w.EPG.InvalidateSource(id)
	}
	for _, id := range work.DeleteSourceFiles {
		note(w.EPG.EnqueueDeleteSourceFiles(id))
	}
	for _, id := range work.RefreshSources {
		note(w.EPG.EnqueueRefreshSource(id))
	}
	if work.WakeDue {
		note(w.EPG.EnqueueRefreshDue(ctx))
	}
	if len(work.RebuildRelays) > 0 {
		note(w.EPG.EnqueueRebuildRelays(work.RebuildRelays))
	}
	for _, c := range work.CleanupRelaySlugs {
		note(w.EPG.EnqueueRelayCleanup(c.RelayID, c.OldSlug))
	}
	return first
}

func (w *Workflows) CreateEPGSource(ctx context.Context, in store.EPGSourceInput) (store.EPGSource, error) {
	var src store.EPGSource
	err := w.withAdmission(func() error {
		var err error
		src, err = w.Store.CreateEPGSource(ctx, in, w.DefaultEPGInterval)
		if err != nil {
			return err
		}
		work := DerivedWork{}
		if src.Enabled {
			work.RefreshSources = []int64{src.ID}
		}
		_ = w.ApplyDerivedWork(ctx, work)
		return nil
	})
	return src, err
}

func (w *Workflows) UpdateEPGSource(ctx context.Context, id int64, in store.EPGSourceInput) (store.EPGSource, error) {
	var src store.EPGSource
	err := w.withAdmission(func() error {
		existing, err := w.Store.GetEPGSource(ctx, id)
		if err != nil {
			return err
		}
		src, err = w.Store.UpdateEPGSource(ctx, id, in, w.DefaultEPGInterval)
		if err != nil {
			return err
		}
		work := DerivedWork{}
		urlChanged := strings.TrimSpace(existing.URL) != strings.TrimSpace(src.URL)
		enabledOn := !existing.Enabled && src.Enabled
		intervalChanged := existing.RefreshInterval != src.RefreshInterval
		if urlChanged {
			work.InvalidateSources = []int64{id}
			work.DeleteSourceFiles = []int64{id}
			if src.Enabled {
				work.RefreshSources = []int64{id}
			}
		} else if enabledOn {
			work.RefreshSources = []int64{id}
		} else if intervalChanged && src.Enabled {
			work.WakeDue = true
		}
		_ = w.ApplyDerivedWork(ctx, work)
		return nil
	})
	return src, err
}

func (w *Workflows) DeleteEPGSource(ctx context.Context, id int64) error {
	return w.withAdmission(func() error {
		if err := w.Store.DeleteEPGSource(ctx, id); err != nil {
			return err
		}
		_ = w.ApplyDerivedWork(ctx, DerivedWork{
			InvalidateSources: []int64{id},
			DeleteSourceFiles: []int64{id},
		})
		return nil
	})
}

func (w *Workflows) UpdateRelay(ctx context.Context, id int64, in store.RelayInput) (store.Relay, error) {
	var updated store.Relay
	err := w.withAdmission(func() error {
		existing, err := w.Store.GetRelay(ctx, id)
		if err != nil {
			return err
		}
		updated, err = w.Store.UpdateRelay(ctx, id, in)
		if err != nil {
			return err
		}
		if existing.Slug != updated.Slug {
			_ = w.ApplyDerivedWork(ctx, DerivedWork{
				RebuildRelays:     []int64{id},
				CleanupRelaySlugs: []RelaySlugCleanup{{RelayID: id, OldSlug: existing.Slug}},
			})
		}
		return nil
	})
	return updated, err
}

func (w *Workflows) DeleteRelay(ctx context.Context, id int64) error {
	return w.withAdmission(func() error {
		existing, err := w.Store.GetRelay(ctx, id)
		if err != nil {
			return err
		}
		if err := w.Store.DeleteRelay(ctx, id); err != nil {
			return err
		}
		_ = w.ApplyDerivedWork(ctx, DerivedWork{
			CleanupRelaySlugs: []RelaySlugCleanup{{RelayID: id, OldSlug: existing.Slug}},
		})
		return nil
	})
}

// UpdateChannel persists a channel and rebuilds owning relays when the EPG pair changes.
// It does not take EPG admission: the HTTP handler still publishes the live revision.
func (w *Workflows) UpdateChannel(ctx context.Context, id string, in store.ChannelInput) (store.Channel, store.Channel, error) {
	before, err := w.Store.GetChannel(ctx, id)
	if err != nil {
		return store.Channel{}, store.Channel{}, err
	}
	after, err := w.Store.UpdateChannel(ctx, id, in)
	if err != nil {
		return before, store.Channel{}, err
	}
	w.enqueueChannelEPGRebuild(ctx, before, after)
	return before, after, nil
}

func (w *Workflows) enqueueChannelEPGRebuild(ctx context.Context, before, after store.Channel) {
	if store.ChannelEPGKey(before) == store.ChannelEPGKey(after) {
		return
	}
	ids, err := w.Store.ChannelRelayIDs(ctx, after.ID)
	if err != nil {
		w.logger().Warn("channel epg owners", "channel_id", after.ID, "err", err)
		return
	}
	_ = w.ApplyDerivedWork(ctx, DerivedWork{RebuildRelays: ids})
}

func (w *Workflows) AddMembership(ctx context.Context, relayID int64, in store.MembershipInput) (store.RelayMembership, error) {
	var m store.RelayMembership
	err := w.withAdmission(func() error {
		var err error
		m, err = w.Store.AddMembership(ctx, relayID, in)
		if err != nil {
			return err
		}
		if store.HasMapping(m.EPGSourceID, m.TvgID) {
			_ = w.ApplyDerivedWork(ctx, DerivedWork{RebuildRelays: []int64{relayID}})
		}
		return nil
	})
	return m, err
}

func (w *Workflows) UpdateMembership(ctx context.Context, relayID, membershipID int64, in store.MembershipInput) (store.RelayMembership, error) {
	var m store.RelayMembership
	err := w.withAdmission(func() error {
		before, err := w.Store.GetMembershipInRelay(ctx, relayID, membershipID)
		if err != nil {
			return err
		}
		m, err = w.Store.UpdateMembership(ctx, membershipID, in)
		if err != nil {
			return err
		}
		if store.MappingKey(before.EPGSourceID, before.TvgID) != store.MappingKey(m.EPGSourceID, m.TvgID) {
			_ = w.ApplyDerivedWork(ctx, DerivedWork{RebuildRelays: []int64{relayID}})
		}
		return nil
	})
	return m, err
}

func (w *Workflows) DeleteMembership(ctx context.Context, relayID, membershipID int64) error {
	return w.withAdmission(func() error {
		before, err := w.Store.GetMembershipInRelay(ctx, relayID, membershipID)
		if err != nil {
			return err
		}
		if err := w.Store.DeleteMembership(ctx, membershipID); err != nil {
			return err
		}
		if store.HasMapping(before.EPGSourceID, before.TvgID) {
			_ = w.ApplyDerivedWork(ctx, DerivedWork{RebuildRelays: []int64{relayID}})
		}
		return nil
	})
}

// ImportRelay persists an already-matched import spec and enqueues derived work.
func (w *Workflows) ImportRelay(ctx context.Context, in store.ImportRelayInput) (store.ImportRelayResult, error) {
	var imported store.ImportRelayResult
	err := w.withAdmission(func() error {
		var err error
		imported, err = w.Store.ImportRelay(ctx, in)
		if err != nil {
			return err
		}
		ids := []int64{imported.RelayID}
		for _, chID := range imported.UpdatedIDs {
			owners, err := w.Store.ChannelRelayIDs(ctx, chID)
			if err != nil {
				w.logger().Warn("import channel owners", "channel_id", chID, "err", err)
				continue
			}
			ids = append(ids, owners...)
		}
		work := DerivedWork{
			RebuildRelays:  utils.UniqueInt64(ids),
			RefreshSources: append([]int64(nil), imported.EPGSourceIDs...),
		}
		_ = w.ApplyDerivedWork(ctx, work)
		return nil
	})
	return imported, err
}
