package epg_test

import (
	"context"
	"fmt"
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
	wantID := fmt.Sprintf(`id="epg%d-keep.id"`, src.ID)
	if !strings.Contains(body, wantID) || strings.Contains(body, `id="keep.id"`) || strings.Contains(body, "drop.id") || !strings.Contains(body, "Show A") {
		t.Fatalf("bad epg: %s", body)
	}
	found := svc.SearchSourceChannels(src.ID, "keep", 10)
	if found.Total != 1 || len(found.Channels) == 0 || found.Channels[0].ID != "keep.id" {
		t.Fatalf("search=%+v", found)
	}
}

func TestRelayEPGRewritesCollidingSourceIDs(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	xmlA := `<?xml version="1.0"?><tv><channel id="cnn"><display-name>CNN A</display-name></channel>
  <programme start="20260101120000 +0000" stop="20260101130000 +0000" channel="cnn"><title>Show A</title></programme></tv>`
	xmlB := `<?xml version="1.0"?><tv><channel id="cnn"><display-name>CNN B</display-name></channel>
  <programme start="20260101120000 +0000" stop="20260101130000 +0000" channel="cnn"><title>Show B</title></programme></tv>`
	upA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(xmlA))
	}))
	upB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(xmlB))
	}))
	t.Cleanup(upA.Close)
	t.Cleanup(upB.Close)

	srcA, _ := st.CreateEPGSource(ctx, store.EPGSourceInput{Name: "A", URL: upA.URL, RefreshInterval: "1h"}, time.Hour)
	srcB, _ := st.CreateEPGSource(ctx, store.EPGSourceInput{Name: "B", URL: upB.URL, RefreshInterval: "1h"}, time.Hour)
	chA, _ := st.CreateChannel(ctx, store.ChannelInput{Name: "A", UpstreamURL: "http://example.com/a.ts"})
	chB, _ := st.CreateChannel(ctx, store.ChannelInput{Name: "B", UpstreamURL: "http://example.com/b.ts"})
	relay, _ := st.CreateRelay(ctx, store.RelayInput{Name: "Home", Slug: "collide"})
	groups, _ := st.ListRelayGroups(ctx, relay.ID)
	idA, idB := srcA.ID, srcB.ID
	_, err = st.AddMembership(ctx, relay.ID, store.MembershipInput{ChannelID: chA.ID, GroupID: groups[0].ID, EPGSourceID: &idA, TvgID: "cnn"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.AddMembership(ctx, relay.ID, store.MembershipInput{ChannelID: chB.ID, GroupID: groups[0].ID, EPGSourceID: &idB, TvgID: "cnn"})
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	svc := epg.New(st, dir, 1<<20, nil)
	if err := svc.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "epg", "collide.xml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	wantA := fmt.Sprintf(`id="epg%d-cnn"`, srcA.ID)
	wantB := fmt.Sprintf(`id="epg%d-cnn"`, srcB.ID)
	if !strings.Contains(body, wantA) || !strings.Contains(body, wantB) {
		t.Fatalf("missing rewritten ids: %s", body)
	}
	if strings.Contains(body, `id="cnn"`) {
		t.Fatalf("raw source id leaked: %s", body)
	}
	if !strings.Contains(body, "Show A") || !strings.Contains(body, "Show B") {
		t.Fatalf("missing programmes: %s", body)
	}
}
