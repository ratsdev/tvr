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

	"github.com/ratsdev/tvr/internal/core/epg"
	"github.com/ratsdev/tvr/internal/core/store"
)

func TestIncompleteRebuildPreservesRelayXML(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	xmlA := `<?xml version="1.0"?><tv><channel id="a.id"><display-name>A</display-name></channel>
  <programme start="20260101120000 +0000" stop="20260101130000 +0000" channel="a.id"><title>A Show</title></programme></tv>`
	xmlB := `<?xml version="1.0"?><tv><channel id="b.id"><display-name>B</display-name></channel>
  <programme start="20260101120000 +0000" stop="20260101130000 +0000" channel="b.id"><title>B Show</title></programme></tv>`
	var serveB bool
	upA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(xmlA))
	}))
	upB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !serveB {
			http.Error(w, "down", 500)
			return
		}
		_, _ = w.Write([]byte(xmlB))
	}))
	t.Cleanup(upA.Close)
	t.Cleanup(upB.Close)

	srcA, _ := st.CreateEPGSource(ctx, store.EPGSourceInput{Name: "A", URL: upA.URL, RefreshInterval: "1h"}, time.Hour)
	srcB, _ := st.CreateEPGSource(ctx, store.EPGSourceInput{Name: "B", URL: upB.URL, RefreshInterval: "1h"}, time.Hour)
	chA, _ := st.CreateChannel(ctx, store.ChannelInput{Name: "A", UpstreamURL: "http://example.com/a.ts"})
	chB, _ := st.CreateChannel(ctx, store.ChannelInput{Name: "B", UpstreamURL: "http://example.com/b.ts"})
	relay, _ := st.CreateRelay(ctx, store.RelayInput{Name: "Home", Slug: "home"})
	groups, _ := st.ListRelayGroups(ctx, relay.ID)
	idA, idB := srcA.ID, srcB.ID
	_, _ = st.AddMembership(ctx, relay.ID, store.MembershipInput{ChannelID: chA.ID, GroupID: groups[0].ID, EPGSourceID: &idA, TvgID: "a.id"})
	_, _ = st.AddMembership(ctx, relay.ID, store.MembershipInput{ChannelID: chB.ID, GroupID: groups[0].ID, EPGSourceID: &idB, TvgID: "b.id"})

	dir := t.TempDir()
	svc := epg.New(st, dir, 1<<20, nil)
	serveB = true
	if err := svc.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "epg", "home.xml")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(before), "A Show") || !strings.Contains(string(before), "B Show") {
		t.Fatalf("expected both shows: %s", before)
	}

	serveB = false
	_ = svc.RefreshSource(ctx, srcB.ID)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("incomplete rebuild overwrote relay XML")
	}
}

func TestRefreshDuringBackoffDoesNotHang(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", 500)
	}))
	t.Cleanup(upstream.Close)
	src, err := st.CreateEPGSource(ctx, store.EPGSourceInput{Name: "G", URL: upstream.URL, RefreshInterval: "1h"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	svc := epg.New(st, t.TempDir(), 1<<20, nil)
	_ = svc.RefreshSource(ctx, src.ID)

	done := make(chan error, 1)
	go func() {
		done <- svc.RefreshSource(ctx, src.ID)
	}()
	select {
	case <-done:
		// ok: must not busy-loop
	case <-time.After(2 * time.Second):
		t.Fatal("RefreshSource hung while source is in backoff")
	}
}

func TestRelayCleanupMultiSlugAndOwnership(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	xml := `<?xml version="1.0"?><tv><channel id="a.id"><display-name>A</display-name></channel>
  <programme start="20260101120000 +0000" stop="20260101130000 +0000" channel="a.id"><title>A Show</title></programme></tv>`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(xml))
	}))
	t.Cleanup(upstream.Close)

	src, _ := st.CreateEPGSource(ctx, store.EPGSourceInput{Name: "G", URL: upstream.URL, RefreshInterval: "1h"}, time.Hour)
	ch, _ := st.CreateChannel(ctx, store.ChannelInput{Name: "A", UpstreamURL: "http://example.com/a.ts"})
	relayA, _ := st.CreateRelay(ctx, store.RelayInput{Name: "A", Slug: "slug-a"})
	groups, _ := st.ListRelayGroups(ctx, relayA.ID)
	id := src.ID
	_, _ = st.AddMembership(ctx, relayA.ID, store.MembershipInput{ChannelID: ch.ID, GroupID: groups[0].ID, EPGSourceID: &id, TvgID: "a.id"})

	dir := t.TempDir()
	svc := epg.New(st, dir, 1<<20, nil)
	if err := svc.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Join(dir, "epg")
	if _, err := os.Stat(filepath.Join(cacheDir, "slug-a.xml")); err != nil {
		t.Fatal(err)
	}

	// Rapid rename a -> b -> c; both old slugs must be cleaned after rebuild.
	relayA, err = st.UpdateRelay(ctx, relayA.ID, store.RelayInput{Name: "A", Slug: "slug-b"})
	if err != nil {
		t.Fatal(err)
	}
	_ = svc.EnqueueRelayCleanup(relayA.ID, "slug-a")
	relayA, err = st.UpdateRelay(ctx, relayA.ID, store.RelayInput{Name: "A", Slug: "slug-c"})
	if err != nil {
		t.Fatal(err)
	}
	_ = svc.EnqueueRelayCleanup(relayA.ID, "slug-b")
	_ = svc.EnqueueRebuildRelays([]int64{relayA.ID})
	if err := svc.DrainPending(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "slug-c.xml")); err != nil {
		t.Fatalf("expected new cache: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "slug-a.xml")); !os.IsNotExist(err) {
		t.Fatalf("slug-a.xml should be removed, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "slug-b.xml")); !os.IsNotExist(err) {
		t.Fatalf("slug-b.xml should be removed, err=%v", err)
	}

	// Reuse old slug on another relay — cleanup must not delete the new owner's file.
	ch2, _ := st.CreateChannel(ctx, store.ChannelInput{Name: "B", UpstreamURL: "http://example.com/b.ts"})
	relayB, _ := st.CreateRelay(ctx, store.RelayInput{Name: "B", Slug: "slug-a"})
	groupsB, _ := st.ListRelayGroups(ctx, relayB.ID)
	_, _ = st.AddMembership(ctx, relayB.ID, store.MembershipInput{ChannelID: ch2.ID, GroupID: groupsB[0].ID, EPGSourceID: &id, TvgID: "a.id"})
	_ = svc.EnqueueRebuildRelays([]int64{relayB.ID})
	if err := svc.DrainPending(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "slug-a.xml")); err != nil {
		t.Fatalf("relay B cache missing: %v", err)
	}
	// Stale cleanup for A's old slug must be a no-op against B's ownership.
	_ = svc.EnqueueRelayCleanup(relayA.ID, "slug-a")
	if err := svc.DrainPending(ctx); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(cacheDir, "slug-a.xml"))
	if err != nil {
		t.Fatalf("ownership violated: slug-a.xml deleted: %v", err)
	}
	if !strings.Contains(string(raw), "A Show") {
		t.Fatalf("unexpected cache contents: %s", raw)
	}
}

func TestWaitAdmissionDrainedAndDrainPending(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := epg.New(st, t.TempDir(), 1<<20, nil)

	release, ok := svc.AcquireAdmission()
	if !ok {
		t.Fatal("expected admission")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := svc.WaitAdmissionDrained(ctx); err == nil {
		t.Fatal("expected timeout while admission held")
	}
	release()
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := svc.WaitAdmissionDrained(ctx2); err != nil {
		t.Fatal(err)
	}
	svc.CloseAdmission()
	if err := svc.DrainPending(ctx2); err != nil {
		t.Fatal(err)
	}
}
