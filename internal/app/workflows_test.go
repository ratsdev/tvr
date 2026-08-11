package app_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/jqjiang/tvr/internal/app"
	"github.com/jqjiang/tvr/internal/epg"
	"github.com/jqjiang/tvr/internal/store"
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
	wf := &app.Workflows{Store: st, EPG: epgSvc, DefaultEPGInterval: time.Hour}
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
	wf := &app.Workflows{Store: st, EPG: epgSvc, DefaultEPGInterval: time.Hour}

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
