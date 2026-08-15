package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ratsdev/tvr/internal/core/store"
)

func TestLibraryBackupRoundTrip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	proxy, err := st.CreateProxy(ctx, store.ProxyInput{
		Name:    "Edge",
		Policy:  store.ProxyPolicyFailover,
		Servers: []store.ProxyServer{{URL: "http://1.2.3.4:8080/"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	src, err := st.CreateEPGSource(ctx, store.EPGSourceInput{Name: "Guide", URL: "http://epg.example/xml"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tvg := "ch1"
	direct, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "News", UpstreamURL: "http://example.com/news.ts", EPGSourceID: &src.ID, TvgID: &tvg,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxied, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "Sports",
		Upstreams: []store.ChannelUpstream{
			{URL: "239.1.2.3:1234", ProxyID: proxy.ID},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	relay, err := st.CreateRelay(ctx, store.RelayInput{Name: "Home", Slug: "home"})
	if err != nil {
		t.Fatal(err)
	}
	groups, err := st.ListRelayGroups(ctx, relay.ID)
	if err != nil || len(groups) == 0 {
		t.Fatalf("groups: %v", err)
	}
	if _, err := st.AddMembership(ctx, relay.ID, store.MembershipInput{ChannelID: direct.ID, GroupID: groups[0].ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddMembership(ctx, relay.ID, store.MembershipInput{ChannelID: proxied.ID, GroupID: groups[0].ID}); err != nil {
		t.Fatal(err)
	}

	snap, err := st.ExportLibrary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Version != store.LibraryBackupVersion || len(snap.Channels) != 2 || len(snap.Relays) != 1 || len(snap.Proxies) != 1 || len(snap.EPGSources) != 1 {
		t.Fatalf("export=%+v", snap)
	}
	if snap.Relays[0].Slug != "home" || len(snap.Relays[0].Memberships) != 2 {
		t.Fatalf("relay=%+v", snap.Relays[0])
	}
	if snap.Proxies[0].ID != proxy.ID || snap.EPGSources[0].ID != src.ID {
		t.Fatalf("proxy/epg export=%+v %+v", snap.Proxies[0], snap.EPGSources[0])
	}

	temp, err := st.CreateChannel(ctx, store.ChannelInput{Name: "Temp", UpstreamURL: "http://example.com/temp.ts"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateRelay(ctx, store.RelayInput{Name: "Other", Slug: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateProxy(ctx, store.ProxyInput{
		Name: "TempProxy", Servers: []store.ProxyServer{{URL: "http://9.9.9.9:1/"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateEPGSource(ctx, store.EPGSourceInput{Name: "TempGuide", URL: "http://epg.example/temp"}, time.Hour); err != nil {
		t.Fatal(err)
	}

	got, err := st.RestoreLibrary(ctx, snap)
	if err != nil {
		t.Fatal(err)
	}
	if got.Channels != 2 || got.Relays != 1 || got.Proxies != 1 || got.EPGSources != 1 || len(got.ChannelIDs) != 2 || len(got.RestoredRelays) != 1 {
		t.Fatalf("restore=%+v", got)
	}
	if got.RestoredRelays[0].ID != relay.ID || got.RestoredRelays[0].Slug != "home" {
		t.Fatalf("restored relay=%+v", got.RestoredRelays[0])
	}
	if len(got.RemovedChannelIDs) != 1 || got.RemovedChannelIDs[0] != temp.ID {
		t.Fatalf("removed channels=%v", got.RemovedChannelIDs)
	}
	if len(got.RemovedRelays) != 1 || got.RemovedRelays[0].ID != other.ID || got.RemovedRelays[0].Slug != "other" {
		t.Fatalf("removed relays=%+v", got.RemovedRelays)
	}
	if len(got.OldEPGSourceIDs) != 2 {
		t.Fatalf("old epg ids=%v", got.OldEPGSourceIDs)
	}
	if len(got.RefreshEPGSourceIDs) != 1 || got.RefreshEPGSourceIDs[0] != src.ID {
		t.Fatalf("refresh epg ids=%v", got.RefreshEPGSourceIDs)
	}
	channels, err := st.ListChannels(ctx)
	if err != nil || len(channels) != 2 {
		t.Fatalf("channels=%d err=%v", len(channels), err)
	}
	relays, err := st.ListRelays(ctx)
	if err != nil || len(relays) != 1 || relays[0].Slug != "home" || relays[0].ID != relay.ID {
		t.Fatalf("relays=%+v err=%v", relays, err)
	}
	detail, err := st.GetRelayDetail(ctx, relay.ID)
	if err != nil || len(detail.Memberships) != 2 {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
	news, err := st.GetChannel(ctx, direct.ID)
	if err != nil || news.Name != "News" || news.EPGSourceID == nil || *news.EPGSourceID != src.ID {
		t.Fatalf("news=%+v err=%v", news, err)
	}
	sports, err := st.GetChannel(ctx, proxied.ID)
	if err != nil || len(sports.Upstreams) != 1 || sports.Upstreams[0].ProxyID != proxy.ID {
		t.Fatalf("sports=%+v err=%v", sports, err)
	}
	proxies, err := st.ListProxies(ctx)
	if err != nil || len(proxies) != 1 || proxies[0].ID != proxy.ID {
		t.Fatalf("proxies=%+v err=%v", proxies, err)
	}
	epgs, err := st.ListEPGSources(ctx)
	if err != nil || len(epgs) != 1 || epgs[0].ID != src.ID || epgs[0].Name != "Guide" {
		t.Fatalf("epgs=%+v err=%v", epgs, err)
	}
}

func TestLibraryRestoreRecreatesProxyAndEPG(t *testing.T) {
	src := openTestStore(t)
	dst := openTestStore(t)
	ctx := context.Background()
	proxy, err := src.CreateProxy(ctx, store.ProxyInput{
		Name:    "Edge",
		Policy:  store.ProxyPolicyFailover,
		Servers: []store.ProxyServer{{URL: "http://1.2.3.4:8080/"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	epg, err := src.CreateEPGSource(ctx, store.EPGSourceInput{Name: "Guide", URL: "http://epg.example/xml"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tvg := "ch1"
	if _, err := src.CreateChannel(ctx, store.ChannelInput{
		Name: "News", UpstreamURL: "http://example.com/news.ts", EPGSourceID: &epg.ID, TvgID: &tvg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := src.CreateChannel(ctx, store.ChannelInput{
		Name: "Sports",
		Upstreams: []store.ChannelUpstream{
			{URL: "239.1.2.3:1234", ProxyID: proxy.ID},
		},
	}); err != nil {
		t.Fatal(err)
	}
	snap, err := src.ExportLibrary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err := dst.RestoreLibrary(ctx, snap)
	if err != nil {
		t.Fatal(err)
	}
	if got.Proxies != 1 || got.EPGSources != 1 || len(got.RefreshEPGSourceIDs) != 1 || got.RefreshEPGSourceIDs[0] != epg.ID {
		t.Fatalf("restore=%+v", got)
	}
	proxies, err := dst.ListProxies(ctx)
	if err != nil || len(proxies) != 1 || proxies[0].ID != proxy.ID || proxies[0].Name != "Edge" {
		t.Fatalf("proxies=%+v err=%v", proxies, err)
	}
	epgs, err := dst.ListEPGSources(ctx)
	if err != nil || len(epgs) != 1 || epgs[0].ID != epg.ID || epgs[0].URL != "http://epg.example/xml" {
		t.Fatalf("epgs=%+v err=%v", epgs, err)
	}
	channels, err := dst.ListChannels(ctx)
	if err != nil || len(channels) != 2 {
		t.Fatalf("channels=%d err=%v", len(channels), err)
	}
	for _, ch := range channels {
		switch ch.Name {
		case "News":
			if ch.EPGSourceID == nil || *ch.EPGSourceID != epg.ID || ch.TvgID != "ch1" {
				t.Fatalf("news=%+v", ch)
			}
		case "Sports":
			if len(ch.Upstreams) != 1 || ch.Upstreams[0].ProxyID != proxy.ID {
				t.Fatalf("sports=%+v", ch)
			}
		}
	}
}

func TestLibraryRestoreRematchesProxyByNameAndClearsOmittedEPG(t *testing.T) {
	src := openTestStore(t)
	dst := openTestStore(t)
	ctx := context.Background()
	proxy, err := src.CreateProxy(ctx, store.ProxyInput{
		Name:    "Edge",
		Policy:  store.ProxyPolicyFailover,
		Servers: []store.ProxyServer{{URL: "http://1.2.3.4:8080/"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	epg, err := src.CreateEPGSource(ctx, store.EPGSourceInput{Name: "Guide", URL: "http://epg.example/xml"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tvg := "ch1"
	if _, err := src.CreateChannel(ctx, store.ChannelInput{
		Name: "News", UpstreamURL: "http://example.com/news.ts", EPGSourceID: &epg.ID, TvgID: &tvg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := src.CreateChannel(ctx, store.ChannelInput{
		Name: "Sports",
		Upstreams: []store.ChannelUpstream{
			{URL: "239.1.2.3:1234", ProxyID: proxy.ID},
		},
	}); err != nil {
		t.Fatal(err)
	}
	snap, err := src.ExportLibrary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snap.Proxies[0].ID = "11111111-1111-1111-1111-111111111111"
	snap.EPGSources = nil

	if _, err := dst.RestoreLibrary(ctx, snap); err != nil {
		t.Fatal(err)
	}
	channels, err := dst.ListChannels(ctx)
	if err != nil || len(channels) != 2 {
		t.Fatalf("channels=%d err=%v", len(channels), err)
	}
	var news, sports store.Channel
	for _, ch := range channels {
		switch ch.Name {
		case "News":
			news = ch
		case "Sports":
			sports = ch
		}
	}
	if news.EPGSourceID != nil || news.TvgID != "" {
		t.Fatalf("missing EPG should be cleared: %+v", news)
	}
	if len(sports.Upstreams) != 1 || sports.Upstreams[0].ProxyID != snap.Proxies[0].ID {
		t.Fatalf("proxy should rematch by name: %+v", sports.Upstreams)
	}
}

func TestLibraryRestoreRejectsBadVersion(t *testing.T) {
	st := openTestStore(t)
	_, err := st.RestoreLibrary(context.Background(), store.LibraryBackup{Version: 99})
	if !errors.Is(err, store.ErrValidation) {
		t.Fatalf("got %v", err)
	}
}

func TestLibraryRestoreRejectsMissingIDs(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	ch, err := st.CreateChannel(ctx, store.ChannelInput{Name: "News", UpstreamURL: "http://example.com/news.ts"})
	if err != nil {
		t.Fatal(err)
	}
	relay, err := st.CreateRelay(ctx, store.RelayInput{Name: "Home", Slug: "home"})
	if err != nil {
		t.Fatal(err)
	}
	groups, err := st.ListRelayGroups(ctx, relay.ID)
	if err != nil || len(groups) == 0 {
		t.Fatalf("groups: %v", err)
	}
	if _, err := st.AddMembership(ctx, relay.ID, store.MembershipInput{ChannelID: ch.ID, GroupID: groups[0].ID}); err != nil {
		t.Fatal(err)
	}
	snap, err := st.ExportLibrary(ctx)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		edit func(*store.LibraryBackup)
		want string
	}{
		{"channel", func(b *store.LibraryBackup) { b.Channels[0].ID = "" }, "channel id"},
		{"proxy", func(b *store.LibraryBackup) {
			if len(b.Proxies) == 0 {
				b.Proxies = []store.BackupProxy{{Name: "X", Servers: []store.BackupProxyServer{{URL: "http://1.1.1.1/"}}}}
			}
			b.Proxies[0].ID = ""
		}, "proxy id"},
		{"epg", func(b *store.LibraryBackup) {
			if len(b.EPGSources) == 0 {
				b.EPGSources = []store.BackupEPGSource{{Name: "G", URL: "http://epg.example/xml"}}
			}
			b.EPGSources[0].ID = 0
		}, "epg source id"},
		{"relay", func(b *store.LibraryBackup) { b.Relays[0].ID = 0 }, "relay id"},
		{"group", func(b *store.LibraryBackup) { b.Relays[0].Groups[0].ID = 0 }, "group id"},
		{"membership", func(b *store.LibraryBackup) { b.Relays[0].Memberships[0].ID = 0 }, "membership id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := snap
			in.Channels = append([]store.BackupChannel(nil), snap.Channels...)
			in.Relays = append([]store.BackupRelay(nil), snap.Relays...)
			in.Relays[0].Groups = append([]store.BackupGroup(nil), snap.Relays[0].Groups...)
			in.Relays[0].Memberships = append([]store.BackupMembership(nil), snap.Relays[0].Memberships...)
			tc.edit(&in)
			_, err := st.RestoreLibrary(ctx, in)
			if err == nil || !errors.Is(err, store.ErrValidation) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v", err)
			}
		})
	}
	if _, err := st.GetChannel(ctx, ch.ID); err != nil {
		t.Fatalf("failed restore must roll back: %v", err)
	}
	if _, err := st.GetRelayDetail(ctx, relay.ID); err != nil {
		t.Fatalf("failed restore must keep relay: %v", err)
	}
}
