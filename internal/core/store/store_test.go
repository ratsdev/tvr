package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jqjiang/tvr/internal/core/store"
	_ "modernc.org/sqlite"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "tvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestChannelCRUDAndBlockDelete(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	ch, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "News", UpstreamURL: "http://example.com/a.ts",
	})
	if err != nil {
		t.Fatal(err)
	}
	relay, err := st.CreateRelay(ctx, store.RelayInput{Name: "Home", Slug: "home"})
	if err != nil {
		t.Fatal(err)
	}
	groups, _ := st.ListRelayGroups(ctx, relay.ID)
	m, err := st.AddMembership(ctx, relay.ID, store.MembershipInput{
		ChannelID: ch.ID, GroupID: groups[0].ID, Number: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteChannel(ctx, ch.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	if err := st.DeleteMembership(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteChannel(ctx, ch.ID); err != nil {
		t.Fatal(err)
	}
}

func TestChannelNameUnique(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateChannel(ctx, store.ChannelInput{Name: "News", UpstreamURL: "http://example.com/a.ts"}); err != nil {
		t.Fatal(err)
	}
	_, err := st.CreateChannel(ctx, store.ChannelInput{Name: "news", UpstreamURL: "http://example.com/b.ts"})
	if !errors.Is(err, store.ErrConflict) || !strings.Contains(err.Error(), "name already exists") {
		t.Fatalf("expected name conflict, got %v", err)
	}
	ch, err := st.CreateChannel(ctx, store.ChannelInput{Name: "Sports", UpstreamURL: "http://example.com/c.ts"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpdateChannel(ctx, ch.ID, store.ChannelInput{Name: "NEWS", UpstreamURL: "http://example.com/c.ts"})
	if !errors.Is(err, store.ErrConflict) || !strings.Contains(err.Error(), "name already exists") {
		t.Fatalf("expected name conflict on update, got %v", err)
	}
}

func TestOpenUpgradesLegacyChannelNameIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
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
CREATE INDEX idx_channels_name ON channels(name COLLATE NOCASE);
INSERT INTO channels (id, name, logo_url, upstream_url, headers_json, created_at, updated_at)
VALUES ('c1', 'News', '', 'http://example.com/a.ts', '{}', 't', 't');
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
	_ = st.Close()
	// Re-open must be idempotent (do not rebuild the unique index every time).
	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	_, err = st.CreateChannel(context.Background(), store.ChannelInput{
		Name: "news", UpstreamURL: "http://example.com/b.ts",
	})
	if !errors.Is(err, store.ErrConflict) || !strings.Contains(err.Error(), "name already exists") {
		t.Fatalf("expected upgraded name uniqueness, got %v", err)
	}
}

func TestRelaySlugUnique(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateRelay(ctx, store.RelayInput{Name: "Home", Slug: "home"}); err != nil {
		t.Fatal(err)
	}
	_, err := st.CreateRelay(ctx, store.RelayInput{Name: "Other", Slug: "home"})
	if !errors.Is(err, store.ErrConflict) || !strings.Contains(err.Error(), "slug already exists") {
		t.Fatalf("expected slug conflict, got %v", err)
	}
	r, err := st.CreateRelay(ctx, store.RelayInput{Name: "Work", Slug: "work"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpdateRelay(ctx, r.ID, store.RelayInput{Name: "Work", Slug: "home"})
	if !errors.Is(err, store.ErrConflict) || !strings.Contains(err.Error(), "slug already exists") {
		t.Fatalf("expected slug conflict on update, got %v", err)
	}
}

func TestRelayLayoutAndUniqueMembership(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	ch1, _ := st.CreateChannel(ctx, store.ChannelInput{Name: "A", UpstreamURL: "http://example.com/a.ts"})
	ch2, _ := st.CreateChannel(ctx, store.ChannelInput{Name: "B", UpstreamURL: "http://example.com/b.ts"})
	relay, _ := st.CreateRelay(ctx, store.RelayInput{Name: "R", Slug: "r"})
	g1, _ := st.ListRelayGroups(ctx, relay.ID)
	g2, _ := st.CreateRelayGroup(ctx, relay.ID, "Sports")
	m1, err := st.AddMembership(ctx, relay.ID, store.MembershipInput{ChannelID: ch1.ID, GroupID: g1[0].ID, Number: 1})
	if err != nil {
		t.Fatal(err)
	}
	m2, err := st.AddMembership(ctx, relay.ID, store.MembershipInput{ChannelID: ch2.ID, GroupID: g2.ID, Number: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddMembership(ctx, relay.ID, store.MembershipInput{ChannelID: ch1.ID, GroupID: g2.ID}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected duplicate membership conflict, got %v", err)
	}
	detail, err := st.ReplaceRelayLayout(ctx, relay.ID, store.RelayLayout{
		Groups: []store.RelayLayoutGroup{
			{ID: g2.ID, Name: "Sports", MembershipIDs: []int64{m2.ID, m1.ID}},
			{ID: g1[0].ID, Name: "Channels", MembershipIDs: []int64{}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Groups[0].Name != "Sports" || detail.Memberships[0].ChannelID != ch2.ID {
		t.Fatalf("unexpected layout: %+v", detail)
	}
	_, lineup, err := st.ListRelayLineup(ctx, "r")
	if err != nil || len(lineup) != 2 || lineup[0].ChannelID != ch2.ID {
		t.Fatalf("lineup=%+v err=%v", lineup, err)
	}
	if lineup[0].Number != 1 || lineup[1].Number != 2 {
		t.Fatalf("expected numbers from order, got %d %d", lineup[0].Number, lineup[1].Number)
	}
}

func TestEPGBindingRequiredForMembership(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	ch, _ := st.CreateChannel(ctx, store.ChannelInput{Name: "A", UpstreamURL: "http://example.com/a.ts"})
	epg, _ := st.CreateEPGSource(ctx, store.EPGSourceInput{Name: "G", URL: "http://example.com/epg.xml"}, time.Hour)
	relay, _ := st.CreateRelay(ctx, store.RelayInput{Name: "R", Slug: "r2"})
	groups, _ := st.ListRelayGroups(ctx, relay.ID)
	id := epg.ID
	_, err := st.AddMembership(ctx, relay.ID, store.MembershipInput{
		ChannelID: ch.ID, GroupID: groups[0].ID, EPGSourceID: &id, TvgID: "x",
	})
	if !errors.Is(err, store.ErrValidation) {
		t.Fatalf("expected validation, got %v", err)
	}
	if err := st.SetRelayEPGSources(ctx, relay.ID, []int64{epg.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddMembership(ctx, relay.ID, store.MembershipInput{
		ChannelID: ch.ID, GroupID: groups[0].ID, EPGSourceID: &id, TvgID: "x",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMembershipTvgUniquenessRollsBack(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	ch1, _ := st.CreateChannel(ctx, store.ChannelInput{Name: "A", UpstreamURL: "http://example.com/a.ts"})
	ch2, _ := st.CreateChannel(ctx, store.ChannelInput{Name: "B", UpstreamURL: "http://example.com/b.ts"})
	epg1, _ := st.CreateEPGSource(ctx, store.EPGSourceInput{Name: "G1", URL: "http://example.com/epg1.xml"}, time.Hour)
	epg2, _ := st.CreateEPGSource(ctx, store.EPGSourceInput{Name: "G2", URL: "http://example.com/epg2.xml"}, time.Hour)
	relay, _ := st.CreateRelay(ctx, store.RelayInput{Name: "R", Slug: "tvg-uniq"})
	groups, _ := st.ListRelayGroups(ctx, relay.ID)
	if err := st.SetRelayEPGSources(ctx, relay.ID, []int64{epg1.ID, epg2.ID}); err != nil {
		t.Fatal(err)
	}
	id1, id2 := epg1.ID, epg2.ID
	m1, err := st.AddMembership(ctx, relay.ID, store.MembershipInput{
		ChannelID: ch1.ID, GroupID: groups[0].ID, EPGSourceID: &id1, TvgID: "same",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddMembership(ctx, relay.ID, store.MembershipInput{
		ChannelID: ch2.ID, GroupID: groups[0].ID, EPGSourceID: &id2, TvgID: "same",
	}); !errors.Is(err, store.ErrValidation) {
		t.Fatalf("expected validation, got %v", err)
	}
	members, err := st.ListRelayMemberships(ctx, relay.ID)
	if err != nil || len(members) != 1 || members[0].ID != m1.ID {
		t.Fatalf("add should roll back conflict; members=%+v err=%v", members, err)
	}

	ch3, _ := st.CreateChannel(ctx, store.ChannelInput{Name: "C", UpstreamURL: "http://example.com/c.ts"})
	m2, err := st.AddMembership(ctx, relay.ID, store.MembershipInput{
		ChannelID: ch3.ID, GroupID: groups[0].ID, EPGSourceID: &id1, TvgID: "other",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateMembership(ctx, m2.ID, store.MembershipInput{
		ChannelID: ch3.ID, GroupID: groups[0].ID, EPGSourceID: &id2, TvgID: "same",
	}); !errors.Is(err, store.ErrValidation) {
		t.Fatalf("expected update validation, got %v", err)
	}
	got, err := st.GetMembership(ctx, m2.ID)
	if err != nil || got.TvgID != "other" || got.EPGSourceID == nil || *got.EPGSourceID != id1 {
		t.Fatalf("update should roll back; got=%+v err=%v", got, err)
	}
}

func TestGroupScopedToRelay(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	r1, _ := st.CreateRelay(ctx, store.RelayInput{Name: "R1", Slug: "r1"})
	r2, _ := st.CreateRelay(ctx, store.RelayInput{Name: "R2", Slug: "r2"})
	g1, _ := st.ListRelayGroups(ctx, r1.ID)
	if _, err := st.UpdateRelayGroup(ctx, r2.ID, g1[0].ID, "Hijacked"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected not found for cross-relay update, got %v", err)
	}
	if err := st.DeleteRelayGroup(ctx, r2.ID, g1[0].ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected not found for cross-relay delete, got %v", err)
	}
	got, err := st.ListRelayGroups(ctx, r1.ID)
	if err != nil || got[0].Name != "Channels" {
		t.Fatalf("group should be unchanged: %+v err=%v", got, err)
	}
}

func TestImportRelayAtomic(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	_, err := st.ImportRelay(ctx, store.ImportRelayInput{
		Name: "Bad",
		Slug: "bad-import",
		Entries: []store.ImportRelayEntry{
			{Name: "A", UpstreamURL: "http://example.com/a.ts", GroupTitle: "News"},
			{Name: "B", UpstreamURL: "not-a-url", GroupTitle: "News"},
		},
	})
	if err == nil {
		t.Fatal("expected validation failure for invalid upstream")
	}
	relays, err := st.ListRelays(ctx)
	if err != nil || len(relays) != 0 {
		t.Fatalf("failed import must leave no relay; relays=%+v err=%v", relays, err)
	}
	channels, err := st.ListChannels(ctx)
	if err != nil || len(channels) != 0 {
		t.Fatalf("failed import must leave no channels; channels=%+v err=%v", channels, err)
	}

	out, err := st.ImportRelay(ctx, store.ImportRelayInput{
		Name: "Good",
		Slug: "good-import",
		Entries: []store.ImportRelayEntry{
			{Name: "A", UpstreamURL: "http://example.com/a.ts", GroupTitle: "News"},
			{Name: "B", UpstreamURL: "http://example.com/b.ts", GroupTitle: "Sports"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ChannelsCreated != 2 || out.MembershipsCreated != 2 || out.GroupsCreated != 2 {
		t.Fatalf("unexpected import result: %+v", out)
	}
}

func TestImportRelayAssignsSoleEPGSource(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	out, err := st.ImportRelay(ctx, store.ImportRelayInput{
		Name:               "With EPG",
		Slug:               "with-epg",
		EPGURLs:            []string{"https://epg.example.com/guide.xml"},
		DefaultEPGInterval: time.Hour,
		Entries: []store.ImportRelayEntry{
			{Name: "A", UpstreamURL: "http://example.com/a.ts", GroupTitle: "News", TvgID: "a.id"},
			{Name: "B", UpstreamURL: "http://example.com/b.ts", GroupTitle: "News", TvgID: "b.id"},
			{Name: "C", UpstreamURL: "http://example.com/c.ts", GroupTitle: "News"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.EPGImported != 1 || len(out.EPGSourceIDs) != 1 {
		t.Fatalf("unexpected epg import: %+v", out)
	}
	detail, err := st.GetRelayDetail(ctx, out.RelayID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Memberships) != 3 {
		t.Fatalf("memberships=%d", len(detail.Memberships))
	}
	sole := out.EPGSourceIDs[0]
	byName := map[string]store.RelayMembership{}
	for _, m := range detail.Memberships {
		byName[m.ChannelName] = m
	}
	for _, name := range []string{"A", "B"} {
		m := byName[name]
		if m.EPGSourceID == nil || *m.EPGSourceID != sole {
			t.Fatalf("membership %s missing sole EPG source: %+v", name, m)
		}
	}
	if byName["C"].EPGSourceID != nil {
		t.Fatalf("entry without tvg-id must not get EPG source: %+v", byName["C"])
	}
}

func TestImportRelayKeepsExplicitMultiEPGMatch(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	srcA, err := st.CreateEPGSource(ctx, store.EPGSourceInput{
		Name: "A", URL: "https://epg.example.com/a.xml", RefreshInterval: "1h",
	}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srcB, err := st.CreateEPGSource(ctx, store.EPGSourceInput{
		Name: "B", URL: "https://epg.example.com/b.xml", RefreshInterval: "1h",
	}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	idB := srcB.ID
	out, err := st.ImportRelay(ctx, store.ImportRelayInput{
		Name:               "Multi",
		Slug:               "multi-epg",
		EPGURLs:            []string{srcA.URL, srcB.URL},
		DefaultEPGInterval: time.Hour,
		Entries: []store.ImportRelayEntry{
			{Name: "Matched", UpstreamURL: "http://example.com/m.ts", GroupTitle: "G", TvgID: "m.id", EPGSourceID: &idB},
			{Name: "Unmatched", UpstreamURL: "http://example.com/u.ts", GroupTitle: "G", TvgID: "u.id"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := st.GetRelayDetail(ctx, out.RelayID)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]store.RelayMembership{}
	for _, m := range detail.Memberships {
		byName[m.ChannelName] = m
	}
	matched := byName["Matched"]
	if matched.EPGSourceID == nil || *matched.EPGSourceID != srcB.ID {
		t.Fatalf("explicit match lost: %+v", matched)
	}
	unmatched := byName["Unmatched"]
	if unmatched.EPGSourceID != nil {
		t.Fatalf("ambiguous multi-source import should leave unmatched unset: %+v", unmatched)
	}
}

func TestImportRelayDisambiguatesChannelNames(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateChannel(ctx, store.ChannelInput{Name: "News", UpstreamURL: "http://example.com/existing.ts"}); err != nil {
		t.Fatal(err)
	}
	out, err := st.ImportRelay(ctx, store.ImportRelayInput{
		Name: "Dup Names",
		Slug: "dup-names",
		Entries: []store.ImportRelayEntry{
			{Name: "News", UpstreamURL: "http://example.com/a.ts", GroupTitle: "A"},
			{Name: "news", UpstreamURL: "http://example.com/b.ts", GroupTitle: "B"},
			{Name: "News", UpstreamURL: "http://example.com/existing.ts", GroupTitle: "A"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ChannelsCreated != 2 || out.ChannelsReused != 1 || out.MembershipsCreated != 3 {
		t.Fatalf("unexpected import result: %+v", out)
	}
	channels, err := st.ListChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, ch := range channels {
		names[ch.UpstreamURL] = ch.Name
	}
	if names["http://example.com/existing.ts"] != "News" {
		t.Fatalf("reused channel renamed: %q", names["http://example.com/existing.ts"])
	}
	if names["http://example.com/a.ts"] != "News (2)" {
		t.Fatalf("expected News (2), got %q", names["http://example.com/a.ts"])
	}
	if names["http://example.com/b.ts"] != "news (3)" {
		t.Fatalf("expected news (3), got %q", names["http://example.com/b.ts"])
	}
}

func TestMembershipNumberFollowsOrder(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	ch1, _ := st.CreateChannel(ctx, store.ChannelInput{Name: "A", UpstreamURL: "http://example.com/a.ts"})
	ch2, _ := st.CreateChannel(ctx, store.ChannelInput{Name: "B", UpstreamURL: "http://example.com/b.ts"})
	relay, _ := st.CreateRelay(ctx, store.RelayInput{Name: "R", Slug: "auto-num"})
	groups, _ := st.ListRelayGroups(ctx, relay.ID)
	m1, err := st.AddMembership(ctx, relay.ID, store.MembershipInput{ChannelID: ch1.ID, GroupID: groups[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if m1.Number != 1 {
		t.Fatalf("expected auto number 1, got %d", m1.Number)
	}
	m2, err := st.AddMembership(ctx, relay.ID, store.MembershipInput{ChannelID: ch2.ID, GroupID: groups[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if m2.Number != 2 {
		t.Fatalf("expected auto number 2, got %d", m2.Number)
	}
	detail, err := st.ReplaceRelayLayout(ctx, relay.ID, store.RelayLayout{
		Groups: []store.RelayLayoutGroup{
			{ID: groups[0].ID, Name: groups[0].Name, MembershipIDs: []int64{m2.ID, m1.ID}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]store.RelayMembership{}
	for _, m := range detail.Memberships {
		byID[m.ID] = m
	}
	if byID[m2.ID].Number != 1 || byID[m1.ID].Number != 2 {
		t.Fatalf("expected renumber after reorder, got m2=%d m1=%d", byID[m2.ID].Number, byID[m1.ID].Number)
	}
}

func TestChannelTranscodeEnabled(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	ch, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "News", UpstreamURL: "http://example.com/a.ts",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ch.TranscodeEnabled {
		t.Fatal("expected create default false")
	}
	on := true
	ch, err = st.UpdateChannel(ctx, ch.ID, store.ChannelInput{
		Name: "News", UpstreamURL: "http://example.com/a.ts", TranscodeEnabled: &on,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ch.TranscodeEnabled {
		t.Fatal("expected enabled after update")
	}
	ch, err = st.UpdateChannel(ctx, ch.ID, store.ChannelInput{
		Name: "News HD", UpstreamURL: "http://example.com/a.ts",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ch.TranscodeEnabled || ch.Name != "News HD" {
		t.Fatalf("omitted pointer must preserve flag: %+v", ch)
	}
	off := false
	ch, err = st.CreateChannel(ctx, store.ChannelInput{
		Name: "Sports", UpstreamURL: "http://example.com/b.ts", TranscodeEnabled: &off,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ch.TranscodeEnabled {
		t.Fatal("expected explicit false")
	}
}

func TestOpenMigratesTranscodeColumnAndSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-transcode.db")
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
VALUES ('c1', 'News', '', 'http://example.com/a.ts', '{}', '2020-01-01T00:00:00Z', '2020-01-01T00:00:00Z');
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
	if ch.TranscodeEnabled {
		t.Fatal("migrated channel must default transcode off")
	}
	settings, err := st.GetTranscodeSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings != store.DefaultTranscodeSettings() {
		t.Fatalf("settings=%+v", settings)
	}
}

func TestTranscodeSettingsUpdate(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	got, err := st.UpdateTranscodeSettings(ctx, store.TranscodeSettings{
		VideoCRF: 28, VideoPreset: "fast", AudioBitrateKbps: 192, MaxHeight: 720, StartupTimeoutSeconds: 45,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.VideoCRF != 28 || got.VideoPreset != "fast" || got.AudioBitrateKbps != 192 || got.MaxHeight != 720 || got.StartupTimeoutSeconds != 45 {
		t.Fatalf("got=%+v", got)
	}
	if _, err := st.UpdateTranscodeSettings(ctx, store.TranscodeSettings{
		VideoCRF: 23, VideoPreset: "nope", AudioBitrateKbps: 128, MaxHeight: 0, StartupTimeoutSeconds: 30,
	}); !errors.Is(err, store.ErrValidation) {
		t.Fatalf("expected validation error, got %v", err)
	}
}

func TestImportRelayPreservesTranscodeFlag(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	on := true
	existing, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "News", UpstreamURL: "http://example.com/a.ts", TranscodeEnabled: &on,
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := st.ImportRelay(ctx, store.ImportRelayInput{
		Name: "Reuse",
		Slug: "reuse",
		Entries: []store.ImportRelayEntry{
			{Name: "News", UpstreamURL: "http://example.com/a.ts", GroupTitle: "News"},
			{Name: "Sports", UpstreamURL: "http://example.com/b.ts", GroupTitle: "Sports"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ChannelsCreated != 1 || out.ChannelsReused != 1 {
		t.Fatalf("unexpected import counts: %+v", out)
	}
	reused, err := st.GetChannel(ctx, existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reused.TranscodeEnabled {
		t.Fatal("reuse must preserve transcode_enabled")
	}
	channels, err := st.ListChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var created store.Channel
	for _, ch := range channels {
		if ch.UpstreamURL == "http://example.com/b.ts" {
			created = ch
		}
	}
	if created.ID == "" {
		t.Fatal("missing created channel")
	}
	if created.TranscodeEnabled {
		t.Fatal("imported channel must default false")
	}
}
