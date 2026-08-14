package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ratsdev/tvr/internal/core/store"
)

func TestImportChannelsCreateAppendReuseSkip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	existing, err := st.CreateChannel(ctx, store.ChannelInput{
		Name:           "News",
		UpstreamPolicy: store.UpstreamPolicyFixed,
		UpstreamURL:    "http://example.com/news-a.ts",
	})
	if err != nil {
		t.Fatal(err)
	}
	fixedID := existing.FixedUpstreamID
	id0 := existing.Upstreams[0].ID
	before := existing.UpdatedAt

	other, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "Sports", UpstreamURL: "http://example.com/sports.ts",
	})
	if err != nil {
		t.Fatal(err)
	}

	out, err := st.ImportChannels(ctx, []store.ImportChannelEntry{
		{Name: "News", URL: "http://example.com/news-b.ts"},
		{Name: "news", URL: "http://example.com/news-a.ts"},
		{Name: "News", URL: "http://example.com/sports.ts"},
		{Name: "Weather", URL: "http://example.com/weather.ts"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Created != 1 || out.Reused != 1 || out.UpstreamsAdded != 1 {
		t.Fatalf("counts=%+v", out)
	}
	if len(out.UpdatedIDs) != 1 || out.UpdatedIDs[0] != existing.ID {
		t.Fatalf("updated=%v", out.UpdatedIDs)
	}
	if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], other.Name) {
		t.Fatalf("warnings=%v", out.Warnings)
	}

	news, err := st.GetChannel(ctx, existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if news.UpstreamPolicy != store.UpstreamPolicyFixed || news.FixedUpstreamID != fixedID {
		t.Fatalf("fixed changed: %+v", news)
	}
	if news.UpstreamURL != "http://example.com/news-a.ts" {
		t.Fatalf("primary=%q", news.UpstreamURL)
	}
	if len(news.Upstreams) != 2 || news.Upstreams[0].ID != id0 {
		t.Fatalf("upstreams=%+v", news.Upstreams)
	}
	if news.Upstreams[1].URL != "http://example.com/news-b.ts" {
		t.Fatalf("appended=%+v", news.Upstreams[1])
	}
	if !news.UpdatedAt.After(before) {
		t.Fatalf("updated_at %v not after %v", news.UpdatedAt, before)
	}

	weather, err := st.ListChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(weather) != 3 {
		t.Fatalf("channels=%d", len(weather))
	}
}

func TestImportChannelsTwoURLsSameName(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	out, err := st.ImportChannels(ctx, []store.ImportChannelEntry{
		{Name: "CCTV-1", URL: "http://example.com/1a.ts"},
		{Name: "CCTV-1", URL: "http://example.com/1b.ts"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Created != 1 || out.UpstreamsAdded != 1 {
		t.Fatalf("counts=%+v", out)
	}
	channels, err := st.ListChannels(ctx)
	if err != nil || len(channels) != 1 {
		t.Fatalf("channels=%+v err=%v", channels, err)
	}
	ch, err := st.GetChannel(ctx, channels[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Upstreams) != 2 {
		t.Fatalf("upstreams=%+v", ch.Upstreams)
	}
}

func TestImportChannelsAtomic(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	_, err := st.ImportChannels(ctx, []store.ImportChannelEntry{
		{Name: "A", URL: "http://example.com/a.ts"},
		{Name: "B", URL: "not-a-url"},
	})
	if err == nil || !errors.Is(err, store.ErrValidation) {
		t.Fatalf("expected validation, got %v", err)
	}
	channels, err := st.ListChannels(ctx)
	if err != nil || len(channels) != 0 {
		t.Fatalf("failed import must leave no channels; channels=%+v err=%v", channels, err)
	}
}
