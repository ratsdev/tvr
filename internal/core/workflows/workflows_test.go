package workflows_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/ratsdev/tvr/internal/core/epg"
	"github.com/ratsdev/tvr/internal/core/store"
	"github.com/ratsdev/tvr/internal/core/workflows"
)

func TestUpdateEPGSourceEnableQueuesRefresh(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	xml := `<?xml version="1.0"?><tv><channel id="a.id"><display-name>A</display-name></channel></tv>`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(xml))
	}))
	t.Cleanup(upstream.Close)

	epgSvc := epg.New(st, t.TempDir(), 1<<20, nil)
	wf := &workflows.Workflows{Store: st, EPG: epgSvc, DefaultEPGInterval: time.Hour}
	enabled := false
	src, err := wf.CreateEPGSource(ctx, store.EPGSourceInput{
		Name: "G", URL: upstream.URL, Enabled: &enabled, RefreshInterval: "1h",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = epgSvc.DrainPending(ctx)
	if epgSvc.Status().Refreshing {
		t.Fatal("disabled create should not leave pending refresh")
	}

	on := true
	if _, err := wf.UpdateEPGSource(ctx, src.ID, store.EPGSourceInput{
		Name: "G", URL: upstream.URL, Enabled: &on, RefreshInterval: "1h",
	}); err != nil {
		t.Fatal(err)
	}
	if !epgSvc.Status().Refreshing {
		t.Fatal("enable should queue refresh")
	}
}

func TestUpdateRelaySlugQueuesRebuildAndCleanup(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	epgSvc := epg.New(st, t.TempDir(), 1<<20, nil)
	wf := &workflows.Workflows{Store: st, EPG: epgSvc, DefaultEPGInterval: time.Hour}

	relay, err := st.CreateRelay(ctx, store.RelayInput{Name: "Home", Slug: "home"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wf.UpdateRelay(ctx, relay.ID, store.RelayInput{Name: "Home", Slug: "home-2"}); err != nil {
		t.Fatal(err)
	}
	if !epgSvc.Status().Refreshing {
		t.Fatal("slug change should queue derived work")
	}
}

func TestUpdateMembershipChannelSwapRebuilds(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	epgSvc := epg.New(st, t.TempDir(), 1<<20, nil)
	wf := &workflows.Workflows{Store: st, EPG: epgSvc, DefaultEPGInterval: time.Hour}

	src, err := st.CreateEPGSource(ctx, store.EPGSourceInput{Name: "G", URL: "http://example.com/epg.xml"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tvg := "a.id"
	bound, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "Bound", UpstreamURL: "http://example.com/a.ts", EPGSourceID: &src.ID, TvgID: &tvg,
	})
	if err != nil {
		t.Fatal(err)
	}
	unbound, err := st.CreateChannel(ctx, store.ChannelInput{Name: "Free", UpstreamURL: "http://example.com/b.ts"})
	if err != nil {
		t.Fatal(err)
	}
	relay, err := st.CreateRelay(ctx, store.RelayInput{Name: "Home", Slug: "home"})
	if err != nil {
		t.Fatal(err)
	}
	groups, _ := st.ListRelayGroups(ctx, relay.ID)
	m, err := wf.AddMembership(ctx, relay.ID, store.MembershipInput{ChannelID: unbound.ID, GroupID: groups[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	_ = epgSvc.DrainPending(ctx)

	if _, err := wf.UpdateMembership(ctx, relay.ID, m.ID, store.MembershipInput{
		ChannelID: bound.ID, GroupID: groups[0].ID,
	}); err != nil {
		t.Fatal(err)
	}
	if !epgSvc.Status().Refreshing {
		t.Fatal("swap onto a bound channel should rebuild")
	}
}

func TestUpdateChannelEPGRebuildsOwningRelays(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	epgSvc := epg.New(st, t.TempDir(), 1<<20, nil)
	wf := &workflows.Workflows{Store: st, EPG: epgSvc, DefaultEPGInterval: time.Hour}

	src, err := st.CreateEPGSource(ctx, store.EPGSourceInput{Name: "G", URL: "http://example.com/epg.xml"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tvg := "old.id"
	ch, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "Live", UpstreamURL: "http://example.com/a.ts", EPGSourceID: &src.ID, TvgID: &tvg,
	})
	if err != nil {
		t.Fatal(err)
	}
	relay, err := st.CreateRelay(ctx, store.RelayInput{Name: "Home", Slug: "home"})
	if err != nil {
		t.Fatal(err)
	}
	groups, _ := st.ListRelayGroups(ctx, relay.ID)
	if _, err := st.AddMembership(ctx, relay.ID, store.MembershipInput{ChannelID: ch.ID, GroupID: groups[0].ID}); err != nil {
		t.Fatal(err)
	}
	_ = epgSvc.DrainPending(ctx)

	next := "new.id"
	if _, _, err := wf.UpdateChannel(ctx, ch.ID, store.ChannelInput{
		Name: ch.Name, EPGSourceID: &src.ID, TvgID: &next,
	}); err != nil {
		t.Fatal(err)
	}
	if !epgSvc.Status().Refreshing {
		t.Fatal("channel epg change should rebuild owning relays")
	}
}

func TestImportRelayFillRebuildsOwningRelays(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	epgSvc := epg.New(st, t.TempDir(), 1<<20, nil)
	wf := &workflows.Workflows{Store: st, EPG: epgSvc, DefaultEPGInterval: time.Hour}

	src, err := st.CreateEPGSource(ctx, store.EPGSourceInput{Name: "G", URL: "https://epg.example.com/guide.xml"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := st.CreateChannel(ctx, store.ChannelInput{Name: "News", UpstreamURL: "http://example.com/a.ts"})
	if err != nil {
		t.Fatal(err)
	}
	old, err := st.CreateRelay(ctx, store.RelayInput{Name: "Old", Slug: "old"})
	if err != nil {
		t.Fatal(err)
	}
	groups, _ := st.ListRelayGroups(ctx, old.ID)
	if _, err := st.AddMembership(ctx, old.ID, store.MembershipInput{ChannelID: ch.ID, GroupID: groups[0].ID}); err != nil {
		t.Fatal(err)
	}
	_ = epgSvc.DrainPending(ctx)

	id := src.ID
	if _, err := wf.ImportRelay(ctx, store.ImportRelayInput{
		Name:               "New",
		Slug:               "new",
		EPGURLs:            []string{src.URL},
		DefaultEPGInterval: time.Hour,
		Entries: []store.ImportRelayEntry{
			{Name: "News", UpstreamURL: "http://example.com/a.ts", GroupTitle: "News", TvgID: "a.id", EPGSourceID: &id},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if !epgSvc.Status().Refreshing {
		t.Fatal("fill-blank should rebuild owning relays")
	}
}

func TestRestoreLibraryQueuesRebuildAndCleanup(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	epgSvc := epg.New(st, t.TempDir(), 1<<20, nil)
	wf := &workflows.Workflows{Store: st, EPG: epgSvc, DefaultEPGInterval: time.Hour}

	if _, err := st.CreateChannel(ctx, store.ChannelInput{Name: "News", UpstreamURL: "http://example.com/news.ts"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRelay(ctx, store.RelayInput{Name: "Home", Slug: "home"}); err != nil {
		t.Fatal(err)
	}
	snap, err := st.ExportLibrary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRelay(ctx, store.RelayInput{Name: "Other", Slug: "other"}); err != nil {
		t.Fatal(err)
	}
	_ = epgSvc.DrainPending(ctx)

	if _, err := wf.RestoreLibrary(ctx, snap); err != nil {
		t.Fatal(err)
	}
	if !epgSvc.Status().Refreshing {
		t.Fatal("restore should queue playlist rebuild and slug cleanup")
	}
}
