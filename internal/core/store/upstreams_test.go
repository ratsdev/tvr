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

func TestOpenMigratesChannelUpstreams(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-upstreams.db")
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
INSERT INTO channels (id, name, logo_url, upstream_url, headers_json, created_at, updated_at)
VALUES ('c1', 'News', '', 'http://example.com/a.ts', '{"User-Agent":"legacy"}', '2020-01-01T00:00:00Z', '2020-01-01T00:00:00Z');
`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	openAndCheck := func(closeNow bool) {
		st, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		ch, err := st.GetChannel(context.Background(), "c1")
		if err != nil {
			_ = st.Close()
			t.Fatal(err)
		}
		if ch.Headers["User-Agent"] != "legacy" {
			_ = st.Close()
			t.Fatalf("channel headers=%v", ch.Headers)
		}
		if ch.UpstreamPolicy != store.UpstreamPolicyFixed {
			_ = st.Close()
			t.Fatalf("policy=%q", ch.UpstreamPolicy)
		}
		if len(ch.Upstreams) != 1 {
			_ = st.Close()
			t.Fatalf("upstreams=%+v", ch.Upstreams)
		}
		if ch.Upstreams[0].URL != "http://example.com/a.ts" {
			_ = st.Close()
			t.Fatalf("child url=%q", ch.Upstreams[0].URL)
		}
		if len(ch.Upstreams[0].Headers) != 0 {
			_ = st.Close()
			t.Fatalf("child overlay should be empty, got %v", ch.Upstreams[0].Headers)
		}
		if ch.FixedUpstreamID != ch.Upstreams[0].ID {
			_ = st.Close()
			t.Fatalf("fixed=%q child=%q", ch.FixedUpstreamID, ch.Upstreams[0].ID)
		}
		if closeNow {
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			return
		}
		t.Cleanup(func() { _ = st.Close() })
	}
	openAndCheck(true)
	openAndCheck(false)
}

func TestChannelMultipleUpstreamsCRUD(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	ch, err := st.CreateChannel(ctx, store.ChannelInput{
		Name:           "News",
		UpstreamPolicy: store.UpstreamPolicyFallback,
		Upstreams: []store.ChannelUpstream{
			{URL: "http://example.com/a.ts", Headers: map[string]string{"X-Row": "1"}},
			{URL: "http://example.com/b.ts"},
		},
		Headers: map[string]string{"User-Agent": "tvr"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ch.UpstreamURL != "http://example.com/a.ts" {
		t.Fatalf("primary=%q", ch.UpstreamURL)
	}
	if ch.UpstreamPolicy != store.UpstreamPolicyFallback || len(ch.Upstreams) != 2 {
		t.Fatalf("ch=%+v", ch)
	}
	if ch.Upstreams[0].Headers["X-Row"] != "1" {
		t.Fatalf("overlay=%v", ch.Upstreams[0].Headers)
	}
	id0, id1 := ch.Upstreams[0].ID, ch.Upstreams[1].ID
	if id0 == "" || id1 == "" {
		t.Fatal("expected persistent child ids")
	}

	updated, err := st.UpdateChannel(ctx, ch.ID, store.ChannelInput{
		Name:            "News",
		UpstreamPolicy:  store.UpstreamPolicyFixed,
		FixedUpstreamID: id1,
		Upstreams: []store.ChannelUpstream{
			{ID: id0, URL: "http://example.com/a.ts"},
			{ID: id1, URL: "http://example.com/b.ts"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.UpstreamURL != "http://example.com/b.ts" || updated.FixedUpstreamID != id1 {
		t.Fatalf("updated=%+v", updated)
	}
	if updated.Upstreams[0].ID != id0 || updated.Upstreams[1].ID != id1 {
		t.Fatalf("ids not reused: %+v", updated.Upstreams)
	}
}

func TestChannelUniqueUpstreamURLAcrossChannels(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "A", UpstreamURL: "http://example.com/a.ts",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "B",
		Upstreams: []store.ChannelUpstream{
			{URL: "http://example.com/b.ts"},
			{URL: "http://example.com/a.ts"},
		},
	})
	if !errors.Is(err, store.ErrConflict) || !strings.Contains(err.Error(), "upstream_url already exists") {
		t.Fatalf("expected url conflict, got %v", err)
	}
}

func TestChannelRejectsDuplicateUpstreamsInPayload(t *testing.T) {
	st := openTestStore(t)
	_, err := st.CreateChannel(context.Background(), store.ChannelInput{
		Name: "News",
		Upstreams: []store.ChannelUpstream{
			{URL: "http://example.com/a.ts"},
			{URL: "http://example.com/a.ts"},
		},
	})
	if !errors.Is(err, store.ErrValidation) || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate validation, got %v", err)
	}
}

func TestChannelCreateAndUpdateWithOnlyUpstreams(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	ch, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "News",
		Upstreams: []store.ChannelUpstream{
			{URL: "http://example.com/a.ts"},
			{URL: "http://example.com/b.ts"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ch.UpstreamURL != "http://example.com/a.ts" || len(ch.Upstreams) != 2 {
		t.Fatalf("create=%+v", ch)
	}
	got, err := st.UpdateChannel(ctx, ch.ID, store.ChannelInput{
		Name:           "News",
		UpstreamPolicy: store.UpstreamPolicyRandom,
		Upstreams: []store.ChannelUpstream{
			{ID: ch.Upstreams[1].ID, URL: "http://example.com/b.ts"},
			{ID: ch.Upstreams[0].ID, URL: "http://example.com/c.ts"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.UpstreamURL != "http://example.com/b.ts" || got.UpstreamPolicy != store.UpstreamPolicyRandom {
		t.Fatalf("update=%+v", got)
	}
	if len(got.Upstreams) != 2 || got.Upstreams[1].URL != "http://example.com/c.ts" {
		t.Fatalf("upstreams=%+v", got.Upstreams)
	}
}

func TestCreateChannelHonorsClientFixedID(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	id0, id1 := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	ch, err := st.CreateChannel(ctx, store.ChannelInput{
		Name:            "News",
		UpstreamPolicy:  store.UpstreamPolicyFixed,
		FixedUpstreamID: id1,
		Upstreams: []store.ChannelUpstream{
			{ID: id0, URL: "http://example.com/a.ts"},
			{ID: id1, URL: "http://example.com/b.ts"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Upstreams) != 2 || ch.Upstreams[0].ID != id0 || ch.Upstreams[1].ID != id1 {
		t.Fatalf("ids not kept: %+v", ch.Upstreams)
	}
	if ch.FixedUpstreamID != id1 || ch.UpstreamURL != "http://example.com/b.ts" {
		t.Fatalf("fixed pick not honored: %+v", ch)
	}
}

func TestUpdateChannelOmitsUpstreamsPreservesChildren(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	ch, err := st.CreateChannel(ctx, store.ChannelInput{
		Name:           "News",
		UpstreamPolicy: store.UpstreamPolicyRandom,
		Upstreams: []store.ChannelUpstream{
			{URL: "http://example.com/a.ts"},
			{URL: "http://example.com/b.ts"},
		},
		Headers: map[string]string{"User-Agent": "tvr"},
	})
	if err != nil {
		t.Fatal(err)
	}
	id0, id1 := ch.Upstreams[0].ID, ch.Upstreams[1].ID
	got, err := st.UpdateChannel(ctx, ch.ID, store.ChannelInput{
		Name:        "News 2",
		UpstreamURL: "http://example.com/ignored.ts",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "News 2" {
		t.Fatalf("name=%q", got.Name)
	}
	if got.UpstreamPolicy != store.UpstreamPolicyRandom {
		t.Fatalf("policy=%q", got.UpstreamPolicy)
	}
	if got.Headers["User-Agent"] != "tvr" {
		t.Fatalf("headers wiped: %v", got.Headers)
	}
	if len(got.Upstreams) != 2 || got.Upstreams[0].ID != id0 || got.Upstreams[1].ID != id1 {
		t.Fatalf("children rewritten: %+v", got.Upstreams)
	}
	if got.Upstreams[0].URL != "http://example.com/a.ts" || got.Upstreams[1].URL != "http://example.com/b.ts" {
		t.Fatalf("urls=%+v", got.Upstreams)
	}
	if got.FixedUpstreamID != ch.FixedUpstreamID {
		t.Fatalf("fixed=%q want %q", got.FixedUpstreamID, ch.FixedUpstreamID)
	}

	cleared, err := st.UpdateChannel(ctx, ch.ID, store.ChannelInput{
		Name:    "News 2",
		Headers: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared.Headers) != 0 {
		t.Fatalf("empty headers map must clear: %v", cleared.Headers)
	}
}

func TestUpdateChannelSingleUpstreamReplacesList(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	ch, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "News",
		Upstreams: []store.ChannelUpstream{
			{URL: "http://example.com/a.ts"},
			{URL: "http://example.com/b.ts"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.UpdateChannel(ctx, ch.ID, store.ChannelInput{
		Name: "News",
		Upstreams: []store.ChannelUpstream{
			{URL: "http://example.com/only.ts"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.UpstreamURL != "http://example.com/only.ts" || len(got.Upstreams) != 1 {
		t.Fatalf("got=%+v", got)
	}
	if got.UpstreamPolicy != store.UpstreamPolicyFixed {
		t.Fatalf("policy=%q", got.UpstreamPolicy)
	}
}

func TestChannelRejectsZeroUpstreams(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	_, err := st.CreateChannel(ctx, store.ChannelInput{
		Name:      "News",
		Upstreams: []store.ChannelUpstream{},
	})
	if !errors.Is(err, store.ErrValidation) {
		t.Fatalf("create empty: %v", err)
	}
	ch, err := st.CreateChannel(ctx, store.ChannelInput{Name: "News", UpstreamURL: "http://example.com/a.ts"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpdateChannel(ctx, ch.ID, store.ChannelInput{
		Name:      "News",
		Upstreams: []store.ChannelUpstream{},
	})
	if !errors.Is(err, store.ErrValidation) {
		t.Fatalf("update empty: %v", err)
	}
	got, err := st.GetChannel(ctx, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Upstreams) != 1 {
		t.Fatalf("rejected update must leave children; got %+v", got.Upstreams)
	}
}

func TestChannelUpdateConflictLeavesChildren(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	a, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "A",
		Upstreams: []store.ChannelUpstream{
			{URL: "http://example.com/a.ts"},
			{URL: "http://example.com/a2.ts"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateChannel(ctx, store.ChannelInput{Name: "B", UpstreamURL: "http://example.com/b.ts"}); err != nil {
		t.Fatal(err)
	}
	_, err = st.UpdateChannel(ctx, a.ID, store.ChannelInput{
		Name: "A",
		Upstreams: []store.ChannelUpstream{
			{URL: "http://example.com/a.ts"},
			{URL: "http://example.com/b.ts"},
		},
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	got, err := st.GetChannel(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Upstreams) != 2 || got.Upstreams[1].URL != "http://example.com/a2.ts" {
		t.Fatalf("conflict must roll back children; got %+v", got.Upstreams)
	}
}

func TestImportRelayCreatesAndReusesUpstreams(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	existing, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "News",
		Upstreams: []store.ChannelUpstream{
			{URL: "http://example.com/primary.ts"},
			{URL: "http://example.com/backup.ts"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	out, err := st.ImportRelay(ctx, store.ImportRelayInput{
		Name: "Imported",
		Slug: "imported",
		Entries: []store.ImportRelayEntry{
			{Name: "News", UpstreamURL: "http://example.com/backup.ts", GroupTitle: "News"},
			{Name: "Sports", UpstreamURL: "http://example.com/new.ts", GroupTitle: "Sports"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ChannelsReused != 1 || out.ChannelsCreated != 1 {
		t.Fatalf("result=%+v", out)
	}

	reused, err := st.GetChannel(ctx, existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reused.Upstreams) != 2 {
		t.Fatalf("reuse must not rewrite children: %+v", reused.Upstreams)
	}

	channels, err := st.ListChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var created store.Channel
	for _, ch := range channels {
		if ch.UpstreamURL == "http://example.com/new.ts" {
			created = ch
		}
	}
	if created.ID == "" || len(created.Upstreams) != 1 {
		t.Fatalf("imported channel missing child rows: %+v", created)
	}
	if created.UpstreamPolicy != store.UpstreamPolicyFixed {
		t.Fatalf("policy=%q", created.UpstreamPolicy)
	}
}

func TestChannelRelayInvalidateComparesFullList(t *testing.T) {
	base := store.Channel{
		UpstreamURL:     "http://example.com/a.ts",
		UpstreamPolicy:  store.UpstreamPolicyFallback,
		FixedUpstreamID: "u1",
		Headers:         map[string]string{"A": "1"},
		Upstreams: []store.ChannelUpstream{
			{ID: "u1", URL: "http://example.com/a.ts"},
			{ID: "u2", URL: "http://example.com/b.ts"},
		},
	}
	samePrimary := base
	samePrimary.Upstreams = []store.ChannelUpstream{
		{ID: "u1", URL: "http://example.com/a.ts"},
		{ID: "u2", URL: "http://example.com/b.ts", Headers: map[string]string{"X": "1"}},
	}
	if !store.ChannelRelayInvalidate(base, samePrimary) {
		t.Fatal("overlay change must invalidate even when primary url is unchanged")
	}
	reordered := base
	reordered.Upstreams = []store.ChannelUpstream{
		{ID: "u1", URL: "http://example.com/a.ts"},
		{ID: "u3", URL: "http://example.com/c.ts"},
	}
	if !store.ChannelRelayInvalidate(base, reordered) {
		t.Fatal("backup url change must invalidate")
	}
	policy := base
	policy.UpstreamPolicy = store.UpstreamPolicyRandom
	if !store.ChannelRelayInvalidate(base, policy) {
		t.Fatal("policy change must invalidate")
	}
	if store.ChannelRelayInvalidate(base, base) {
		t.Fatal("identical channel must not invalidate")
	}
}

func TestSelectUpstreamRandomVaries(t *testing.T) {
	ch := store.Channel{
		UpstreamPolicy: store.UpstreamPolicyRandom,
		Upstreams: []store.ChannelUpstream{
			{ID: "a", URL: "http://example.com/a.ts"},
			{ID: "b", URL: "http://example.com/b.ts"},
		},
	}
	seen := map[string]int{}
	for i := 0; i < 80; i++ {
		u, err := ch.SelectUpstream("")
		if err != nil {
			t.Fatal(err)
		}
		seen[u.ID]++
	}
	if len(seen) < 2 {
		t.Fatalf("random test pick stuck on one url: %v", seen)
	}
	fixed, err := ch.SelectUpstream("b")
	if err != nil {
		t.Fatal(err)
	}
	if fixed.ID != "b" {
		t.Fatalf("explicit id=%q", fixed.ID)
	}
}

func TestChannelStreamSource(t *testing.T) {
	ch := store.Channel{
		UpstreamPolicy:  " random ",
		FixedUpstreamID: "a",
		Upstreams: []store.ChannelUpstream{
			{ID: "a", URL: "http://example.com/a.ts"},
			{ID: "b", URL: "http://example.com/b.ts"},
		},
	}
	src := ch.StreamSource()
	if src.Policy != "random" {
		t.Fatalf("policy=%q", src.Policy)
	}
	if len(src.Upstreams) != 2 || src.FixedIndex != 0 {
		t.Fatalf("src=%+v", src)
	}
	if src.Upstreams[0].URL != "http://example.com/a.ts" {
		t.Fatalf("url=%q", src.Upstreams[0].URL)
	}
}
