package epg_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
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

func TestParseXMLTVTime(t *testing.T) {
	cases := []struct {
		in   string
		want time.Time
	}{
		{"20260101123000 +0000", time.Date(2026, 1, 1, 12, 30, 0, 0, time.UTC)},
		{"20260101123000 +08:00", time.Date(2026, 1, 1, 4, 30, 0, 0, time.UTC)},
		{"20260101123000Z", time.Date(2026, 1, 1, 12, 30, 0, 0, time.UTC)},
		{"20260101123000", time.Date(2026, 1, 1, 12, 30, 0, 0, time.UTC)},
		{"202601011230", time.Date(2026, 1, 1, 12, 30, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		got, err := epg.ParseXMLTVTime(tc.in)
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if !got.Equal(tc.want) {
			t.Fatalf("%q: got %v want %v", tc.in, got, tc.want)
		}
	}
	if _, err := epg.ParseXMLTVTime(""); err == nil {
		t.Fatal("expected error for empty time")
	}
}

func TestQuerySourceGuideWindowAndPagination(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	xml := `<?xml version="1.0" encoding="UTF-8"?>
<tv>
  <channel id="b.id"><display-name lang="zh">中文B</display-name><display-name lang="en">Channel B</display-name></channel>
  <channel id="a.id"><display-name lang="en">Channel A</display-name></channel>
  <channel id="c.id"><display-name>Other</display-name></channel>
  <programme start="20260101110000 +0000" stop="20260101120000 +0000" channel="a.id"><title lang="en">Before</title></programme>
  <programme start="20260101120000 +0000" stop="20260101130000 +0000" channel="a.id"><title lang="en">On Edge</title></programme>
  <programme start="20260101123000 +0000" stop="20260101133000 +0000" channel="a.id"><title lang="en">Inside</title></programme>
  <programme start="20260101130000 +0000" stop="20260101140000 +0000" channel="a.id"><title lang="en">After Start</title></programme>
  <programme start="20260101120000 +0000" stop="20260101120000 +0000" channel="a.id"><title>Bad Duration</title></programme>
  <programme start="bad" stop="20260101130000 +0000" channel="a.id"><title>Bad Start</title></programme>
  <programme start="20260101120000 +0000" stop="20260101130000 +0000" channel="b.id"><title lang="zh">节目</title><title lang="en">Show B</title></programme>
  <programme start="20260101120000 +0000" stop="20260101130000 +0000" channel="c.id"><title>Other Show</title></programme>
</tv>`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(xml))
	}))
	t.Cleanup(upstream.Close)

	src, err := st.CreateEPGSource(ctx, store.EPGSourceInput{Name: "g", URL: upstream.URL, RefreshInterval: "1h"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	svc := epg.New(st, dir, 1<<20, nil)
	if err := svc.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "epg-sources", "1.xml")); err != nil {
		// id may not be 1; find any xml
		entries, _ := os.ReadDir(filepath.Join(dir, "epg-sources"))
		found := false
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".xml") {
				found = true
			}
		}
		if !found {
			t.Fatalf("source cache missing: %v", err)
		}
	}

	from := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	res, err := svc.QuerySourceGuide(src.ID, src.URL, false, epg.GuideQuery{
		From: from, To: to, Q: "channel", Offset: 0, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 2 {
		t.Fatalf("total=%d want 2", res.Total)
	}
	if len(res.Channels) != 1 || res.Channels[0].ID != "a.id" || res.Channels[0].DisplayName != "Channel A" {
		t.Fatalf("page=%+v", res.Channels)
	}
	titles := make([]string, 0, len(res.Channels[0].Programmes))
	for _, p := range res.Channels[0].Programmes {
		titles = append(titles, p.Title)
	}
	want := []string{"On Edge", "Inside"}
	if strings.Join(titles, ",") != strings.Join(want, ",") {
		t.Fatalf("programmes=%v want %v", titles, want)
	}

	page2, err := svc.QuerySourceGuide(src.ID, src.URL, true, epg.GuideQuery{
		From: from, To: to, Q: "channel", Offset: 1, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !page2.Stale || len(page2.Channels) != 1 || page2.Channels[0].ID != "b.id" {
		t.Fatalf("page2=%+v", page2)
	}
	if page2.Channels[0].Programmes[0].Title != "Show B" {
		t.Fatalf("expected english title, got %+v", page2.Channels[0].Programmes[0])
	}
}

func TestSourceCacheStaleRetentionAndURLInvalidation(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	good := `<?xml version="1.0"?><tv><channel id="x"><display-name>X</display-name></channel>
  <programme start="20260101120000 +0000" stop="20260101130000 +0000" channel="x"><title>Live</title></programme></tv>`
	var body string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if body == "fail" {
			http.Error(w, "nope", 500)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(upstream.Close)
	body = good

	src, err := st.CreateEPGSource(ctx, store.EPGSourceInput{Name: "g", URL: upstream.URL, RefreshInterval: "1h"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	svc := epg.New(st, dir, 1<<20, nil)
	if err := svc.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	first, err := svc.QuerySourceGuide(src.ID, src.URL, false, epg.GuideQuery{From: from, To: to})
	if err != nil || len(first.Channels) != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}

	body = "fail"
	_ = svc.Refresh(ctx) // should keep previous cache
	second, err := svc.QuerySourceGuide(src.ID, src.URL, true, epg.GuideQuery{From: from, To: to})
	if err != nil || len(second.Channels) != 1 {
		t.Fatalf("stale cache lost: %+v err=%v", second, err)
	}

	svc.InvalidateSource(src.ID)
	for _, pattern := range []string{
		filepath.Join(dir, "epg-sources", "*"),
		filepath.Join(dir, "epg-index", "*"),
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range matches {
			_ = os.Remove(p)
		}
	}
	if _, err := svc.QuerySourceGuide(src.ID, src.URL, false, epg.GuideQuery{From: from, To: to}); !errors.Is(err, epg.ErrGuideRefreshRequired) {
		t.Fatalf("want refresh required, got %v", err)
	}
}

func TestDecompressedSizeLimit(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	var raw bytes.Buffer
	zw := gzip.NewWriter(&raw)
	// Small gzip, huge decompressed payload of spaces + minimal xml
	payload := append([]byte(`<?xml version="1.0"?><tv>`), bytes.Repeat([]byte(" "), 200000)...)
	payload = append(payload, []byte(`</tv>`)...)
	if _, err := zw.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = zw.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(raw.Bytes())
	}))
	t.Cleanup(upstream.Close)

	src, err := st.CreateEPGSource(ctx, store.EPGSourceInput{Name: "g", URL: upstream.URL, RefreshInterval: "1h"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	svc := epg.New(st, t.TempDir(), 1024, nil)
	_ = svc.Refresh(ctx)
	if _, err := svc.QuerySourceGuide(src.ID, src.URL, false, epg.GuideQuery{
		From: time.Now().UTC(), To: time.Now().UTC().Add(time.Hour),
	}); !errors.Is(err, epg.ErrGuideRefreshRequired) {
		t.Fatalf("expected no cache after oversized decompress, got %v", err)
	}
}

func TestRestartReadsSourceCache(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	xml := `<?xml version="1.0"?><tv><channel id="x"><display-name>X</display-name></channel>
  <programme start="20260101120000 +0000" stop="20260101130000 +0000" channel="x"><title>Live</title></programme></tv>`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(xml))
	}))
	t.Cleanup(upstream.Close)
	src, _ := st.CreateEPGSource(ctx, store.EPGSourceInput{Name: "g", URL: upstream.URL, RefreshInterval: "1h"}, time.Hour)
	dir := t.TempDir()
	svc1 := epg.New(st, dir, 1<<20, nil)
	if err := svc1.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	svc2 := epg.New(st, dir, 1<<20, nil)
	from := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	res, err := svc2.QuerySourceGuide(src.ID, src.URL, false, epg.GuideQuery{From: from, To: from.Add(time.Hour)})
	if err != nil || len(res.Channels) != 1 {
		t.Fatalf("restart read failed: %+v err=%v", res, err)
	}
}
