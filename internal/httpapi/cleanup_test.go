package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ratsdev/tvr/internal/config"
	"github.com/ratsdev/tvr/internal/core/epg"
	"github.com/ratsdev/tvr/internal/core/store"
	"github.com/ratsdev/tvr/internal/core/stream"
	"github.com/ratsdev/tvr/internal/core/transcode"
	"github.com/ratsdev/tvr/internal/core/upstream"
	"github.com/ratsdev/tvr/static"
)

func TestUpdateChannelCleanupTimeout(t *testing.T) {
	prev := channelStreamCleanupTimeout
	channelStreamCleanupTimeout = 30 * time.Millisecond
	t.Cleanup(func() { channelStreamCleanupTimeout = prev })

	helper := writeIgnoreTermHelper(t)
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "tvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	rel := stream.NewManager(stream.Options{
		BufferSize:  32,
		IdleTimeout: 5 * time.Second,
		ConnTimeout: time.Second,
		TranscodeProfile: transcode.Profile{
			FFmpegPath:       helper,
			StartupTimeout:   2 * time.Second,
			VideoCRF:         23,
			VideoPreset:      "veryfast",
			AudioBitrateKbps: 128,
		},
	})
	t.Cleanup(func() { _ = rel.Close(context.Background()) })

	staticFS, err := fs.Sub(static.Content, ".")
	if err != nil {
		t.Fatal(err)
	}
	epgSvc := epg.New(st, dir, 1<<20, nil)
	api := New(config.Config{
		BaseURL:          "http://127.0.0.1",
		DataDir:          dir,
		FFmpegPath:       helper,
		RelayBufferSize:  32,
		RelayIdleTimeout: 5 * time.Second,
		RelayConnTimeout: time.Second,
		EPGMaxBytes:      1 << 20,
		EPGDefaultEvery:  time.Hour,
	}, st, rel, epgSvc, nil, staticFS, nil)
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	on := true
	ch, err := st.CreateChannel(context.Background(), store.ChannelInput{
		Name: "Live", UpstreamURL: "http://example.com/a.ts", TranscodeEnabled: &on,
	})
	if err != nil {
		t.Fatal(err)
	}

	subCtx, subCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer subCancel()
	r, err := rel.Subscribe(subCtx, ch.ID, upstream.Fixed(upstream.Upstream{
		URL: ch.UpstreamURL, Transcode: true, Revision: ch.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	buf := make([]byte, 188)
	if _, err := io.ReadAtLeast(r, buf, 188); err != nil {
		t.Fatal(err)
	}

	body := `{"name":"Live","logo_url":"","upstreams":[{"url":"http://example.com/b.ts"}],"headers":{},"transcode_enabled":true}`
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
	if res.StatusCode != http.StatusGatewayTimeout {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status=%d body=%s", res.StatusCode, b)
	}
}

func TestChannelEPGUpdateQueuesRebuildBeforePublish504(t *testing.T) {
	prev := channelStreamCleanupTimeout
	channelStreamCleanupTimeout = 30 * time.Millisecond
	t.Cleanup(func() { channelStreamCleanupTimeout = prev })

	helper := writeIgnoreTermHelper(t)
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "tvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	rel := stream.NewManager(stream.Options{
		BufferSize:  32,
		IdleTimeout: 5 * time.Second,
		ConnTimeout: time.Second,
		TranscodeProfile: transcode.Profile{
			FFmpegPath:       helper,
			StartupTimeout:   2 * time.Second,
			VideoCRF:         23,
			VideoPreset:      "veryfast",
			AudioBitrateKbps: 128,
		},
	})
	t.Cleanup(func() { _ = rel.Close(context.Background()) })

	staticFS, err := fs.Sub(static.Content, ".")
	if err != nil {
		t.Fatal(err)
	}
	epgSvc := epg.New(st, dir, 1<<20, nil)
	api := New(config.Config{
		BaseURL:          "http://127.0.0.1",
		DataDir:          dir,
		FFmpegPath:       helper,
		RelayBufferSize:  32,
		RelayIdleTimeout: 5 * time.Second,
		RelayConnTimeout: time.Second,
		EPGMaxBytes:      1 << 20,
		EPGDefaultEvery:  time.Hour,
	}, st, rel, epgSvc, nil, staticFS, nil)
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	ctx := context.Background()
	src, err := st.CreateEPGSource(ctx, store.EPGSourceInput{Name: "G", URL: "http://example.com/epg.xml"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	on := true
	tvg := "old.id"
	ch, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "Live", UpstreamURL: "http://example.com/a.ts", TranscodeEnabled: &on, EPGSourceID: &src.ID, TvgID: &tvg,
	})
	if err != nil {
		t.Fatal(err)
	}
	relay, err := st.CreateRelay(ctx, store.RelayInput{Name: "Home", Slug: "home"})
	if err != nil {
		t.Fatal(err)
	}
	groups, _ := st.ListRelayGroups(ctx, relay.ID)
	if _, err := st.AddMembership(ctx, relay.ID, store.MembershipInput{ChannelID: ch.ID, GroupID: groups[0].ID}); err != nil {
		t.Fatal(err)
	}
	_ = epgSvc.DrainPending(ctx)

	subCtx, subCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer subCancel()
	r, err := rel.Subscribe(subCtx, ch.ID, upstream.Fixed(upstream.Upstream{
		URL: ch.UpstreamURL, Transcode: true, Revision: ch.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	buf := make([]byte, 188)
	if _, err := io.ReadAtLeast(r, buf, 188); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{
		"name": "Live", "logo_url": "", "headers": map[string]string{},
		"upstreams":         []map[string]any{{"url": "http://example.com/b.ts"}},
		"transcode_enabled": true,
		"epg_source_id":     src.ID,
		"tvg_id":            "new.id",
	})
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/channels/"+ch.ID, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusGatewayTimeout {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status=%d body=%s", res.StatusCode, b)
	}
	if !epgSvc.Status().Refreshing {
		t.Fatal("rebuild must be queued before publish 504")
	}
	got, err := st.GetChannel(ctx, ch.ID)
	if err != nil || got.TvgID != "new.id" {
		t.Fatalf("epg row is committed either way: %+v err=%v", got, err)
	}
}

func writeIgnoreTermHelper(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "helper.go")
	bin := filepath.Join(dir, "helper")
	code := `package main
import (
  "os"
  "os/signal"
  "syscall"
  "time"
)
func main() {
  signal.Ignore(syscall.SIGTERM)
  pkt := make([]byte, 188)
  pkt[0] = 0x47
  for {
    if _, err := os.Stdout.Write(pkt); err != nil {
      return
    }
    time.Sleep(5 * time.Millisecond)
  }
}
`
	if err := os.WriteFile(src, []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build helper: %v\n%s", err, out)
	}
	return bin
}
