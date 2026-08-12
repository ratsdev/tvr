package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jqjiang/tvr/internal/config"
	"github.com/jqjiang/tvr/internal/epg"
	"github.com/jqjiang/tvr/internal/httpapi"
	"github.com/jqjiang/tvr/internal/relay"
	"github.com/jqjiang/tvr/internal/store"
	"github.com/jqjiang/tvr/web"
)

func newTestServer(t *testing.T) (*httptest.Server, *store.Store, *epg.Service) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "tvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	rel := relay.NewManager(relay.Options{BufferSize: 64, IdleTimeout: 2 * time.Second})
	t.Cleanup(func() { _ = rel.Close(context.Background()) })
	epgSvc := epg.New(st, dir, 1<<20, nil)
	webFS, err := fs.Sub(web.Content, ".")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not configured", 500)
	}))
	cfg := config.Config{
		BaseURL:          srv.URL,
		DataDir:          dir,
		FFmpegPath:       "ffmpeg",
		RelayBufferSize:  64,
		RelayIdleTimeout: 2 * time.Second,
		RelayConnTimeout: time.Second,
		EPGMaxBytes:      1 << 20,
		EPGDefaultEvery:  time.Hour,
	}
	api := httpapi.New(cfg, st, rel, epgSvc, nil, webFS, nil)
	srv.Config.Handler = api.Handler()
	t.Cleanup(srv.Close)
	return srv, st, epgSvc
}

func TestRelayPlaylistAndStream(t *testing.T) {
	pkt := make([]byte, 188)
	pkt[0] = 0x47
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		for i := 0; i < 20; i++ {
			_, _ = w.Write(pkt)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(5 * time.Millisecond)
		}
	}))
	t.Cleanup(upstream.Close)

	srv, st, _ := newTestServer(t)
	ctx := context.Background()
	ch, err := st.CreateChannel(ctx, store.ChannelInput{Name: "Live", UpstreamURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	relayRow, err := st.CreateRelay(ctx, store.RelayInput{Name: "Home", Slug: "home"})
	if err != nil {
		t.Fatal(err)
	}
	groups, _ := st.ListRelayGroups(ctx, relayRow.ID)
	if _, err := st.AddMembership(ctx, relayRow.ID, store.MembershipInput{
		ChannelID: ch.ID, GroupID: groups[0].ID, Number: 5, TvgID: "live.id",
	}); err != nil {
		t.Fatal(err)
	}

	pres, err := http.Get(srv.URL + "/r/home/playlist.m3u")
	if err != nil {
		t.Fatal(err)
	}
	defer pres.Body.Close()
	playlist, _ := io.ReadAll(pres.Body)
	text := string(playlist)
	if !strings.Contains(text, `tvg-id="live.id"`) || !strings.Contains(text, "/stream/"+ch.ID) {
		t.Fatalf("playlist=%s", text)
	}

	ctx2, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx2, http.MethodGet, srv.URL+"/stream/"+ch.ID, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status=%d", res.StatusCode)
	}
	buf := make([]byte, 188)
	if _, err := io.ReadFull(res.Body, buf); err != nil {
		t.Fatal(err)
	}
	if buf[0] != 0x47 {
		t.Fatal("expected mpeg-ts")
	}
}

func TestImportRelayM3U(t *testing.T) {
	srv, st, _ := newTestServer(t)
	playlist := `#EXTM3U url-tvg="https://epg.example.com/guide.xml"
#EXTINF:-1 tvg-id="a" tvg-chno="1" group-title="News",Alpha
http://example.com/a.ts
#EXTINF:-1 tvg-id="b" tvg-chno="2" group-title="News",Beta
https://example.com/b.ts
`
	body, _ := json.Marshal(map[string]any{
		"content":       playlist,
		"relay_name":    "Imported",
		"relay_slug":    "imported",
		"ignore_groups": true,
		"import_epg":    true,
	})
	res, err := http.Post(srv.URL+"/api/relays/import", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d body=%s", res.StatusCode, raw)
	}
	channels, _ := st.ListChannels(context.Background())
	if len(channels) != 2 {
		t.Fatalf("channels=%d", len(channels))
	}
	detail, err := st.GetRelayBySlug(context.Background(), "imported")
	if err != nil {
		t.Fatal(err)
	}
	groups, _ := st.ListRelayGroups(context.Background(), detail.ID)
	if len(groups) != 1 || groups[0].Name != "Channels" {
		t.Fatalf("groups=%+v", groups)
	}
	full, err := st.GetRelayDetail(context.Background(), detail.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.EPGSourceIDs) != 1 {
		t.Fatalf("epg sources=%v", full.EPGSourceIDs)
	}
	for _, m := range full.Memberships {
		if m.EPGSourceID == nil || *m.EPGSourceID != full.EPGSourceIDs[0] {
			t.Fatalf("membership missing sole EPG source: %+v", m)
		}
		if m.TvgID == "" {
			t.Fatalf("membership missing tvg-id: %+v", m)
		}
	}
	// reuse on second import URL match into another relay
	body2, _ := json.Marshal(map[string]any{
		"content":    playlist,
		"relay_name": "Imported 2",
		"relay_slug": "imported-2",
	})
	res2, err := http.Post(srv.URL+"/api/relays/import", "application/json", bytes.NewReader(body2))
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	raw2, _ := io.ReadAll(res2.Body)
	var out struct {
		ChannelsCreated int `json:"channels_created"`
		ChannelsReused  int `json:"channels_reused"`
	}
	_ = json.Unmarshal(raw2, &out)
	if out.ChannelsCreated != 0 || out.ChannelsReused != 2 {
		t.Fatalf("reuse result=%+v body=%s", out, raw2)
	}
}

func TestImportRelayMultiEPGUnmatchedWhenUncached(t *testing.T) {
	srv, st, _ := newTestServer(t)
	playlist := `#EXTM3U url-tvg="https://epg.example.com/a.xml,https://epg.example.com/b.xml"
#EXTINF:-1 tvg-id="a.id" group-title="News",Alpha
http://example.com/a.ts
#EXTINF:-1 tvg-id="b.id" group-title="News",Beta
http://example.com/b.ts
#EXTINF:-1 group-title="News",Gamma
http://example.com/c.ts
`
	body, _ := json.Marshal(map[string]any{
		"content":    playlist,
		"relay_name": "Multi",
		"relay_slug": "multi",
		"import_epg": true,
	})
	res, err := http.Post(srv.URL+"/api/relays/import", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d body=%s", res.StatusCode, raw)
	}
	var out struct {
		UnmatchedTvgIDs []string `json:"unmatched_tvg_ids"`
		Warnings        []string `json:"warnings"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.UnmatchedTvgIDs) != 2 {
		t.Fatalf("unmatched=%v body=%s", out.UnmatchedTvgIDs, raw)
	}
	wantWarn := false
	for _, w := range out.Warnings {
		if w == "some tvg-id mappings need review after EPG refresh" {
			wantWarn = true
			break
		}
	}
	if !wantWarn {
		t.Fatalf("missing review warning: %v", out.Warnings)
	}
	relay, err := st.GetRelayBySlug(context.Background(), "multi")
	if err != nil {
		t.Fatal(err)
	}
	detail, err := st.GetRelayDetail(context.Background(), relay.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.EPGSourceIDs) != 2 {
		t.Fatalf("epg sources=%v", detail.EPGSourceIDs)
	}
	for _, m := range detail.Memberships {
		if m.EPGSourceID != nil {
			t.Fatalf("uncached multi-source membership must leave EPG unset: %+v", m)
		}
	}
}

func itoa(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestPublicBaseURLTrustProxy(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "tvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	rel := relay.NewManager(relay.Options{BufferSize: 64, IdleTimeout: 2 * time.Second})
	t.Cleanup(func() { _ = rel.Close(context.Background()) })
	epgSvc := epg.New(st, dir, 1<<20, nil)
	webFS, err := fs.Sub(web.Content, ".")
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		BaseURL:          "",
		TrustProxy:       false,
		DataDir:          dir,
		FFmpegPath:       "ffmpeg",
		RelayBufferSize:  64,
		RelayIdleTimeout: 2 * time.Second,
		RelayConnTimeout: time.Second,
		EPGMaxBytes:      1 << 20,
		EPGDefaultEvery:  time.Hour,
	}
	api := httpapi.New(cfg, st, rel, epgSvc, nil, webFS, nil)
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/health", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "evil.example")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	base, _ := body["base_url"].(string)
	if strings.Contains(base, "evil.example") || strings.HasPrefix(base, "https://") {
		t.Fatalf("untrusted proxy headers should be ignored, got %q", base)
	}

	cfg.TrustProxy = true
	api2 := httpapi.New(cfg, st, rel, epgSvc, nil, webFS, nil)
	srv2 := httptest.NewServer(api2.Handler())
	t.Cleanup(srv2.Close)
	req2, _ := http.NewRequest(http.MethodGet, srv2.URL+"/api/health", nil)
	req2.Header.Set("X-Forwarded-Proto", "https")
	req2.Header.Set("X-Forwarded-Host", "tv.example")
	res2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	raw2, _ := io.ReadAll(res2.Body)
	var body2 map[string]any
	_ = json.Unmarshal(raw2, &body2)
	if body2["base_url"] != "https://tv.example" {
		t.Fatalf("trusted proxy base=%v", body2["base_url"])
	}

	cfg.BaseURL = "https://fixed.example/iptv"
	cfg.TrustProxy = false
	api3 := httpapi.New(cfg, st, rel, epgSvc, nil, webFS, nil)
	srv3 := httptest.NewServer(api3.Handler())
	t.Cleanup(srv3.Close)
	res3, err := http.Get(srv3.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer res3.Body.Close()
	raw3, _ := io.ReadAll(res3.Body)
	var body3 map[string]any
	_ = json.Unmarshal(raw3, &body3)
	if body3["base_url"] != "https://fixed.example/iptv" {
		t.Fatalf("configured base=%v", body3["base_url"])
	}
}

func TestEPGSourceAPIContract(t *testing.T) {
	srv, _, _ := newTestServer(t)
	body, _ := json.Marshal(map[string]any{
		"name":             "Guide",
		"url":              "http://example.com/epg.xml",
		"enabled":          true,
		"refresh_interval": "2h",
	})
	res, err := http.Post(srv.URL+"/api/epg/sources", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", res.StatusCode, raw)
	}
	var created map[string]any
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	if _, ok := created["refresh_interval"].(string); !ok {
		t.Fatalf("create refresh_interval should be string, got %#v", created["refresh_interval"])
	}
	if created["refresh_interval"] != "2h0m0s" {
		t.Fatalf("create refresh_interval=%v", created["refresh_interval"])
	}
	if created["last_error"] != "" {
		t.Fatalf("last_error should be empty string, got %#v", created["last_error"])
	}
	if created["last_refresh_at"] != nil {
		t.Fatalf("last_refresh_at should be null, got %#v", created["last_refresh_at"])
	}

	listRes, err := http.Get(srv.URL + "/api/epg/sources")
	if err != nil {
		t.Fatal(err)
	}
	defer listRes.Body.Close()
	listRaw, _ := io.ReadAll(listRes.Body)
	var listed []map[string]any
	if err := json.Unmarshal(listRaw, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0]["refresh_interval"] != "2h0m0s" {
		t.Fatalf("list=%s", listRaw)
	}

	id := int64(created["id"].(float64))
	updBody, _ := json.Marshal(map[string]any{
		"name":             "Guide 2",
		"url":              "http://example.com/epg.xml",
		"enabled":          true,
		"refresh_interval": "90m",
	})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/epg/sources/"+itoa(id), bytes.NewReader(updBody))
	req.Header.Set("Content-Type", "application/json")
	updRes, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer updRes.Body.Close()
	updRaw, _ := io.ReadAll(updRes.Body)
	var updated map[string]any
	if err := json.Unmarshal(updRaw, &updated); err != nil {
		t.Fatal(err)
	}
	if updated["refresh_interval"] != "1h30m0s" {
		t.Fatalf("update refresh_interval=%v body=%s", updated["refresh_interval"], updRaw)
	}
}

func TestEPGSourceGuideAPI(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<tv>
  <channel id="a.id"><display-name>Alpha</display-name></channel>
  <channel id="b.id"><display-name>Beta</display-name></channel>
  <programme start="20260101120000 +0000" stop="20260101130000 +0000" channel="a.id"><title>Show A</title><desc>Desc</desc></programme>
  <programme start="20260101120000 +0000" stop="20260101130000 +0000" channel="b.id"><title>Show B</title></programme>
</tv>`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(xml))
	}))
	t.Cleanup(upstream.Close)

	srv, st, epgSvc := newTestServer(t)
	ctx := context.Background()
	src, err := st.CreateEPGSource(ctx, store.EPGSourceInput{Name: "Guide", URL: upstream.URL, RefreshInterval: "1h"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	from := "2026-01-01T12:00:00Z"
	to := "2026-01-01T13:00:00Z"
	res, err := http.Get(srv.URL + "/api/epg/sources/" + itoa(src.ID) + "/guide?from=" + from + "&to=" + to)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("unrefreshed status=%d", res.StatusCode)
	}

	if err := epgSvc.Refresh(ctx); err != nil {
		t.Fatal(err)
	}

	bad, err := http.Get(srv.URL + "/api/epg/sources/" + itoa(src.ID) + "/guide?from=" + from + "&to=2026-01-03T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized window status=%d", bad.StatusCode)
	}

	missing, err := http.Get(srv.URL + "/api/epg/sources/99999/guide?from=" + from + "&to=" + to)
	if err != nil {
		t.Fatal(err)
	}
	defer missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing status=%d", missing.StatusCode)
	}

	okRes, err := http.Get(srv.URL + "/api/epg/sources/" + itoa(src.ID) + "/guide?from=" + from + "&to=" + to + "&q=alp&limit=10")
	if err != nil {
		t.Fatal(err)
	}
	defer okRes.Body.Close()
	raw, _ := io.ReadAll(okRes.Body)
	if okRes.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", okRes.StatusCode, raw)
	}
	var guide struct {
		Total    int  `json:"total"`
		Offset   int  `json:"offset"`
		Limit    int  `json:"limit"`
		Stale    bool `json:"stale"`
		Channels []struct {
			ID         string `json:"id"`
			Programmes []struct {
				Title string `json:"title"`
				Start string `json:"start"`
			} `json:"programmes"`
		} `json:"channels"`
	}
	if err := json.Unmarshal(raw, &guide); err != nil {
		t.Fatal(err)
	}
	if guide.Total != 1 || len(guide.Channels) != 1 || guide.Channels[0].ID != "a.id" {
		t.Fatalf("guide=%+v body=%s", guide, raw)
	}
	if len(guide.Channels[0].Programmes) != 1 || guide.Channels[0].Programmes[0].Title != "Show A" {
		t.Fatalf("programmes=%+v", guide.Channels[0].Programmes)
	}
	if guide.Channels[0].Programmes[0].Start != "2026-01-01T12:00:00Z" {
		t.Fatalf("start=%s", guide.Channels[0].Programmes[0].Start)
	}
}

func TestEPGSourceEnableWakesRefresh(t *testing.T) {
	xml := `<?xml version="1.0"?><tv><channel id="a.id"><display-name>A</display-name></channel></tv>`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(xml))
	}))
	t.Cleanup(upstream.Close)

	srv, _, epgSvc := newTestServer(t)
	body, _ := json.Marshal(map[string]any{
		"name":             "Guide",
		"url":              upstream.URL,
		"enabled":          false,
		"refresh_interval": "1h",
	})
	res, err := http.Post(srv.URL+"/api/epg/sources", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var created map[string]any
	_ = json.Unmarshal(raw, &created)
	id := int64(created["id"].(float64))

	updBody, _ := json.Marshal(map[string]any{
		"name":             "Guide",
		"url":              upstream.URL,
		"enabled":          true,
		"refresh_interval": "1h",
	})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/epg/sources/"+itoa(id), bytes.NewReader(updBody))
	req.Header.Set("Content-Type", "application/json")
	updRes, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer updRes.Body.Close()
	if updRes.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", updRes.StatusCode)
	}
	if !epgSvc.Status().Refreshing {
		t.Fatal("expected refresh queued after enable")
	}
	if err := epgSvc.DrainPending(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsGetAndPut(t *testing.T) {
	srv, _, _ := newTestServer(t)
	res, err := http.Get(srv.URL + "/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status=%d", res.StatusCode)
	}
	var got map[string]any
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["transcode"].(map[string]any); !ok {
		t.Fatalf("missing transcode: %#v", got)
	}
	body := `{"transcode":{"video_crf":28,"video_preset":"fast","audio_bitrate_kbps":160,"max_height":720,"startup_timeout_seconds":40}}`
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/settings", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	upd, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer upd.Body.Close()
	if upd.StatusCode != 200 {
		b, _ := io.ReadAll(upd.Body)
		t.Fatalf("status=%d body=%s", upd.StatusCode, b)
	}
}

func TestChannelPutOmitsTranscodePreservesFlag(t *testing.T) {
	srv, st, _ := newTestServer(t)
	ctx := context.Background()
	on := true
	ch, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "Live", UpstreamURL: "http://example.com/a.ts", TranscodeEnabled: &on,
	})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"name":"Live 2","logo_url":"","upstream_url":"http://example.com/a.ts","headers":{}}`
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/channels/"+ch.ID, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status=%d body=%s", res.StatusCode, b)
	}
	var out store.Channel
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.TranscodeEnabled || out.Name != "Live 2" {
		t.Fatalf("out=%+v", out)
	}
}
