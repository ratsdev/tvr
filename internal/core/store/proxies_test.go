package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ratsdev/tvr/internal/core/store"
	_ "modernc.org/sqlite"
)

func TestOpenRebuildsChannelUpstreamsWithoutURLUnique(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-proxy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE channels (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  logo_url TEXT NOT NULL DEFAULT '',
  upstream_url TEXT NOT NULL UNIQUE,
  headers_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE channel_upstreams (
  id TEXT PRIMARY KEY,
  channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  url TEXT NOT NULL UNIQUE,
  headers_json TEXT NOT NULL DEFAULT '{}',
  sort_order INTEGER NOT NULL DEFAULT 0
);
INSERT INTO channels (id, name, logo_url, upstream_url, headers_json, created_at, updated_at)
VALUES ('c1', 'News', '', 'http://example.com/a.ts', '{}', '2020-01-01T00:00:00Z', '2020-01-01T00:00:00Z');
INSERT INTO channel_upstreams (id, channel_id, url, headers_json, sort_order)
VALUES ('u1', 'c1', 'http://example.com/a.ts', '{}', 0);
`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ch, err := st.GetChannel(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Upstreams) != 1 || ch.Upstreams[0].ProxyID != "" {
		t.Fatalf("migrated=%+v", ch.Upstreams)
	}
	_, err = st.CreateChannel(context.Background(), store.ChannelInput{
		Name: "Other",
		Upstreams: []store.ChannelUpstream{
			{URL: "http://example.com/b.ts"},
			{URL: "http://example.com/a.ts"},
		},
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected direct url unique after rebuild, got %v", err)
	}
}

func TestProxyCRUDAndBlockDelete(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	p, err := st.CreateProxy(ctx, store.ProxyInput{
		Name:   "SHIPTV",
		Policy: store.ProxyPolicyFailover,
		Servers: []store.ProxyServer{
			{URL: "http://1.2.3.4:9901/udp/"},
			{URL: "http://2.3.4.5:9902/udp/"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "SHIPTV" || p.Policy != store.ProxyPolicyFailover || len(p.Servers) != 2 {
		t.Fatalf("proxy=%+v", p)
	}
	ch, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "CCTV",
		Upstreams: []store.ChannelUpstream{
			{URL: "239.1.2.3:1234", ProxyID: p.ID},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ch.UpstreamURL != "http://1.2.3.4:9901/udp/239.1.2.3:1234" {
		t.Fatalf("primary=%q", ch.UpstreamURL)
	}
	if ch.Upstreams[0].ProxyID != p.ID {
		t.Fatalf("proxy_id=%q", ch.Upstreams[0].ProxyID)
	}
	src := ch.StreamSource()
	if len(src.Upstreams) != 1 || len(src.Upstreams[0].Candidates) != 2 {
		t.Fatalf("source=%+v", src.Upstreams)
	}
	if src.Upstreams[0].URL != "http://1.2.3.4:9901/udp/239.1.2.3:1234" {
		t.Fatalf("fetch=%q", src.Upstreams[0].URL)
	}
	if err := st.DeleteProxy(ctx, p.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected in-use conflict, got %v", err)
	}
	if err := st.DeleteChannel(ctx, ch.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteProxy(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSharedSuffixDifferentProxies(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	a, err := st.CreateProxy(ctx, store.ProxyInput{
		Name: "A", Servers: []store.ProxyServer{{URL: "http://1.1.1.1:1/udp/"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateProxy(ctx, store.ProxyInput{
		Name: "B", Servers: []store.ProxyServer{{URL: "http://2.2.2.2:2/udp/"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "One", Upstreams: []store.ChannelUpstream{{URL: "239.0.0.1:1", ProxyID: a.ID}},
	}); err != nil {
		t.Fatal(err)
	}
	ch, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "Two", Upstreams: []store.ChannelUpstream{{URL: "239.0.0.1:1", ProxyID: b.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ch.UpstreamURL != "http://2.2.2.2:2/udp/239.0.0.1:1" {
		t.Fatalf("primary=%q", ch.UpstreamURL)
	}
}

func TestSameResolvedPrimaryConflicts(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	p, err := st.CreateProxy(ctx, store.ProxyInput{
		Name: "P", Servers: []store.ProxyServer{{URL: "http://1.1.1.1:1/udp/"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "One", Upstreams: []store.ChannelUpstream{{URL: "239.0.0.1:1", ProxyID: p.ID}},
	}); err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateChannel(ctx, store.ChannelInput{
		Name: "Two", Upstreams: []store.ChannelUpstream{{URL: "239.0.0.1:1", ProxyID: p.ID}},
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestProxyUpdateRewritesPrimaryAndRollsBackOnCollision(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	p, err := st.CreateProxy(ctx, store.ProxyInput{
		Name: "P", Servers: []store.ProxyServer{{URL: "http://old/udp/"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "Mcast", Upstreams: []store.ChannelUpstream{{URL: "10.0.0.1:1", ProxyID: p.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "Taken", UpstreamURL: "http://new/udp/10.0.0.1:1",
	}); err != nil {
		t.Fatal(err)
	}
	_, _, err = st.UpdateProxy(ctx, p.ID, store.ProxyInput{
		Name: "P", Servers: []store.ProxyServer{{ID: p.Servers[0].ID, URL: "http://new/udp/"}},
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected collision, got %v", err)
	}
	got, err := st.GetChannel(ctx, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.UpstreamURL != "http://old/udp/10.0.0.1:1" {
		t.Fatalf("primary mutated after rollback: %q", got.UpstreamURL)
	}

	_, _, err = st.UpdateProxy(ctx, p.ID, store.ProxyInput{
		Name: "P", Servers: []store.ProxyServer{{ID: p.Servers[0].ID, URL: "http://ok/udp/"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err = st.GetChannel(ctx, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.UpstreamURL != "http://ok/udp/10.0.0.1:1" {
		t.Fatalf("primary=%q", got.UpstreamURL)
	}
}

func TestProxiedLinkValidation(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	p, err := st.CreateProxy(ctx, store.ProxyInput{
		Name: "P", Servers: []store.ProxyServer{{URL: "http://1.1.1.1:1/udp/"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateChannel(ctx, store.ChannelInput{
		Name: "Bad", Upstreams: []store.ChannelUpstream{{URL: "http://example.com/x", ProxyID: p.ID}},
	})
	if !errors.Is(err, store.ErrValidation) || !strings.Contains(err.Error(), "host:port") {
		t.Fatalf("expected suffix validation, got %v", err)
	}
}

func TestImportIgnoresProxiedSuffix(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	p, err := st.CreateProxy(ctx, store.ProxyInput{
		Name: "P", Servers: []store.ProxyServer{{URL: "http://1.1.1.1:1/udp/"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "Mcast", Upstreams: []store.ChannelUpstream{{URL: "239.0.0.1:1", ProxyID: p.ID}},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := st.ImportChannels(ctx, []store.ImportChannelEntry{
		{Name: "Other", URL: "http://example.com/other.ts"},
		{Name: "Mcast", URL: "http://1.1.1.1:1/udp/239.0.0.1:1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 1 || res.Reused != 1 {
		t.Fatalf("created=%d reused=%d", res.Created, res.Reused)
	}
}

func TestImportReusesFailoverCandidate(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	p, err := st.CreateProxy(ctx, store.ProxyInput{
		Name:   "P",
		Policy: store.ProxyPolicyFailover,
		Servers: []store.ProxyServer{
			{URL: "http://1.1.1.1:1/udp/"},
			{URL: "http://2.2.2.2:2/udp/"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "Mcast", Upstreams: []store.ChannelUpstream{{URL: "239.0.0.1:1", ProxyID: p.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := st.ImportChannels(ctx, []store.ImportChannelEntry{
		{Name: "Mcast", URL: "http://2.2.2.2:2/udp/239.0.0.1:1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 0 || res.Reused != 1 || res.UpstreamsAdded != 0 {
		t.Fatalf("created=%d reused=%d added=%d", res.Created, res.Reused, res.UpstreamsAdded)
	}
	got, err := st.GetChannel(ctx, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Upstreams) != 1 {
		t.Fatalf("upstreams=%+v", got.Upstreams)
	}
}

func TestDirectNullProxyID(t *testing.T) {
	st := openTestStore(t)
	ch, err := st.CreateChannel(context.Background(), store.ChannelInput{
		Name: "News", UpstreamURL: "http://example.com/a.ts",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ch.Upstreams[0].ProxyID != "" {
		t.Fatalf("proxy_id=%q", ch.Upstreams[0].ProxyID)
	}
}
