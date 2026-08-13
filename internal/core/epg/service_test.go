package epg_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jqjiang/tvr/internal/core/epg"
	"github.com/jqjiang/tvr/internal/core/store"
)

func TestRefreshBuildsPerRelayCache(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	xml := `<?xml version="1.0" encoding="UTF-8"?>
<tv>
  <channel id="keep.id"><display-name>Keep</display-name></channel>
  <channel id="drop.id"><display-name>Drop</display-name></channel>
  <programme start="20260101000000 +0000" stop="20260101010000 +0000" channel="keep.id"><title>Show A</title></programme>
  <programme start="20260101000000 +0000" stop="20260101010000 +0000" channel="drop.id"><title>Show B</title></programme>
</tv>`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(xml))
	}))
	t.Cleanup(upstream.Close)

	src, err := st.CreateEPGSource(ctx, store.EPGSourceInput{Name: "g", URL: upstream.URL, RefreshInterval: "1h"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ch, _ := st.CreateChannel(ctx, store.ChannelInput{Name: "Keep", UpstreamURL: "http://example.com/a.ts"})
	relay, _ := st.CreateRelay(ctx, store.RelayInput{Name: "Home", Slug: "home"})
	_ = st.SetRelayEPGSources(ctx, relay.ID, []int64{src.ID})
	groups, _ := st.ListRelayGroups(ctx, relay.ID)
	epgID := src.ID
	_, err = st.AddMembership(ctx, relay.ID, store.MembershipInput{
		ChannelID: ch.ID, GroupID: groups[0].ID, Number: 1, EPGSourceID: &epgID, TvgID: "keep.id",
	})
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	svc := epg.New(st, dir, 1<<20, nil)
	if err := svc.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "epg", "home.xml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, `id="keep.id"`) || strings.Contains(body, "drop.id") || !strings.Contains(body, "Show A") {
		t.Fatalf("bad epg: %s", body)
	}
	found := svc.SearchSourceChannels(src.ID, "keep", 10)
	if len(found) == 0 || found[0].ID != "keep.id" {
		t.Fatalf("search=%+v", found)
	}
}
