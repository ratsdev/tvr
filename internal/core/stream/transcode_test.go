package stream_test

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ratsdev/tvr/internal/core/stream"
	"github.com/ratsdev/tvr/internal/core/transcode"
	"github.com/ratsdev/tvr/internal/core/upstream"
)

func TestTranscodeMissingFFmpegFailsReady(t *testing.T) {
	mgr := stream.NewManager(stream.Options{
		BufferSize:  32,
		IdleTimeout: time.Second,
		ConnTimeout: 200 * time.Millisecond,
		TranscodeProfile: transcode.Profile{
			FFmpegPath:       filepath.Join(t.TempDir(), "missing-ffmpeg"),
			StartupTimeout:   300 * time.Millisecond,
			VideoCRF:         23,
			VideoPreset:      "veryfast",
			AudioBitrateKbps: 128,
		},
	})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := mgr.Subscribe(ctx, "ch1", upstream.Fixed(upstream.Upstream{
		URL:       "http://example.invalid/live.ts",
		Transcode: true,
		Revision:  "1",
	}))
	if err == nil {
		t.Fatal("expected readiness failure")
	}

	deadline := time.Now().Add(2 * time.Second)
	var st stream.Status
	for time.Now().Before(deadline) {
		st = mgr.Status("ch1")
		if st.State == "error" && st.LastError != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st.State != "error" || st.LastError == "" {
		t.Fatalf("expected sticky error status, got %+v", st)
	}
	found := false
	for _, s := range mgr.AllStatuses() {
		if s.ChannelID == "ch1" && s.State == "error" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("AllStatuses missing sticky error: %+v", mgr.AllStatuses())
	}
}

func TestTranscodeHelperProducesMPEGTS(t *testing.T) {
	helper := writeTSHelper(t)
	mgr := stream.NewManager(stream.Options{
		BufferSize:  32,
		IdleTimeout: 2 * time.Second,
		ConnTimeout: time.Second,
		TranscodeProfile: transcode.Profile{
			FFmpegPath:       helper,
			StartupTimeout:   2 * time.Second,
			VideoCRF:         23,
			VideoPreset:      "veryfast",
			AudioBitrateKbps: 128,
		},
	})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, err := mgr.Subscribe(ctx, "ch1", upstream.Fixed(upstream.Upstream{
		URL:       "http://example.com/ignored.ts",
		Transcode: true,
		Revision:  "1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	buf := make([]byte, 188*4)
	if _, err := io.ReadAtLeast(r, buf, 188); err != nil {
		t.Fatal(err)
	}
	if buf[0] != 0x47 {
		t.Fatalf("expected mpeg-ts sync, got %#x", buf[0])
	}
}

func TestPublishChannelDoesNotRestickTornDownSession(t *testing.T) {
	mgr := stream.NewManager(stream.Options{
		BufferSize:  32,
		IdleTimeout: time.Second,
		ConnTimeout: 200 * time.Millisecond,
		TranscodeProfile: transcode.Profile{
			FFmpegPath:       filepath.Join(t.TempDir(), "missing-ffmpeg"),
			StartupTimeout:   300 * time.Millisecond,
			VideoCRF:         23,
			VideoPreset:      "veryfast",
			AudioBitrateKbps: 128,
		},
	})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := mgr.Subscribe(ctx, "ch1", upstream.Fixed(upstream.Upstream{
		URL: "http://example.invalid/live.ts", Transcode: true, Revision: "1",
	}))
	if err == nil {
		t.Fatal("expected readiness failure")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st := mgr.Status("ch1"); st.State == "error" && st.LastError != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st := mgr.Status("ch1"); st.State != "error" {
		t.Fatalf("expected sticky error before publish, got %+v", st)
	}

	pubCtx, pubCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer pubCancel()
	if err := mgr.PublishChannel(pubCtx, "ch1", "2", true); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if st := mgr.Status("ch1"); st.State == "idle" && st.LastError == "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected idle after intentional teardown, got %+v", mgr.Status("ch1"))
}

func TestBlockChannelClearsStickyError(t *testing.T) {
	mgr := stream.NewManager(stream.Options{
		BufferSize:  32,
		IdleTimeout: time.Second,
		ConnTimeout: 200 * time.Millisecond,
		TranscodeProfile: transcode.Profile{
			FFmpegPath:       filepath.Join(t.TempDir(), "missing-ffmpeg"),
			StartupTimeout:   300 * time.Millisecond,
			VideoCRF:         23,
			VideoPreset:      "veryfast",
			AudioBitrateKbps: 128,
		},
	})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _ = mgr.Subscribe(ctx, "ch1", upstream.Fixed(upstream.Upstream{
		URL: "http://example.invalid/live.ts", Transcode: true, Revision: "1",
	}))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st := mgr.Status("ch1"); st.State == "error" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	blockCtx, blockCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer blockCancel()
	if err := mgr.BlockChannel(blockCtx, "ch1"); err != nil {
		t.Fatal(err)
	}
	if st := mgr.Status("ch1"); st.State != "idle" || st.LastError != "" {
		t.Fatalf("expected idle after delete/block, got %+v", st)
	}
	for _, st := range mgr.AllStatuses() {
		if st.ChannelID == "ch1" {
			t.Fatalf("AllStatuses still has blocked channel: %+v", st)
		}
	}
}

func TestRetiredFinisherPreservesNewerSticky(t *testing.T) {
	slow := writeIgnoreTermHelper(t)
	missing := filepath.Join(t.TempDir(), "missing-ffmpeg")
	mgr := stream.NewManager(stream.Options{
		BufferSize:  32,
		IdleTimeout: 5 * time.Second,
		ConnTimeout: time.Second,
		TranscodeProfile: transcode.Profile{
			FFmpegPath:       slow,
			StartupTimeout:   2 * time.Second,
			VideoCRF:         23,
			VideoPreset:      "veryfast",
			AudioBitrateKbps: 128,
		},
	})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, err := mgr.Subscribe(ctx, "ch1", upstream.Fixed(upstream.Upstream{
		URL: "http://example.com/a.ts", Transcode: true, Revision: "1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 188)
	if _, err := io.ReadAtLeast(r, buf, 188); err != nil {
		t.Fatal(err)
	}

	// Detach without waiting for the slow SIGKILL reap window.
	canceled, cancelPub := context.WithCancel(context.Background())
	cancelPub()
	_ = mgr.PublishChannel(canceled, "ch1", "2", true)
	_ = r.Close()

	if err := mgr.ApplyProfile(context.Background(), transcode.Profile{
		FFmpegPath:       missing,
		StartupTimeout:   300 * time.Millisecond,
		VideoCRF:         23,
		VideoPreset:      "veryfast",
		AudioBitrateKbps: 128,
	}); err != nil {
		t.Fatal(err)
	}

	subCtx, subCancel := context.WithTimeout(context.Background(), time.Second)
	defer subCancel()
	_, err = mgr.Subscribe(subCtx, "ch1", upstream.Fixed(upstream.Upstream{
		URL: "http://example.com/b.ts", Transcode: true, Revision: "2",
	}))
	if err == nil {
		t.Fatal("expected newer session readiness failure")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st := mgr.Status("ch1"); st.State == "error" && st.LastError != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st := mgr.Status("ch1"); st.State != "error" {
		t.Fatalf("expected sticky error from newer session, got %+v", st)
	}

	// Allow the retired session's delayed finish to run (SIGKILL grace is 2s).
	time.Sleep(2500 * time.Millisecond)
	if st := mgr.Status("ch1"); st.State != "error" || st.LastError == "" {
		t.Fatalf("retired finisher cleared newer sticky: %+v", st)
	}
}

func TestPublishChannelCanceledContextFailsCleanup(t *testing.T) {
	helper := writeTSHelper(t)
	mgr := stream.NewManager(stream.Options{
		BufferSize:  32,
		IdleTimeout: 2 * time.Second,
		ConnTimeout: time.Second,
		TranscodeProfile: transcode.Profile{
			FFmpegPath:       helper,
			StartupTimeout:   2 * time.Second,
			VideoCRF:         23,
			VideoPreset:      "veryfast",
			AudioBitrateKbps: 128,
		},
	})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, err := mgr.Subscribe(ctx, "ch1", upstream.Fixed(upstream.Upstream{
		URL: "http://example.com/a.ts", Transcode: true, Revision: "1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	canceled, cancelPub := context.WithCancel(context.Background())
	cancelPub()
	if err := mgr.PublishChannel(canceled, "ch1", "2", true); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestPublishChannelInvalidatesSession(t *testing.T) {
	helper := writeTSHelper(t)
	mgr := stream.NewManager(stream.Options{
		BufferSize:  32,
		IdleTimeout: 2 * time.Second,
		ConnTimeout: time.Second,
		TranscodeProfile: transcode.Profile{
			FFmpegPath:       helper,
			StartupTimeout:   2 * time.Second,
			VideoCRF:         23,
			VideoPreset:      "veryfast",
			AudioBitrateKbps: 128,
		},
	})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, err := mgr.Subscribe(ctx, "ch1", upstream.Fixed(upstream.Upstream{
		URL: "http://example.com/a.ts", Transcode: true, Revision: "1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.PublishChannel(ctx, "ch1", "2", true); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, r)
	_ = r.Close()
	_, err = mgr.Subscribe(ctx, "ch1", upstream.Fixed(upstream.Upstream{
		URL: "http://example.com/a.ts", Transcode: true, Revision: "1",
	}))
	if !errors.Is(err, stream.ErrStaleRevision) {
		t.Fatalf("expected stale revision, got %v", err)
	}
}

func writeTSHelper(t *testing.T) string {
	t.Helper()
	return buildHelper(t, `package main
import (
  "os"
  "time"
)
func main() {
  pkt := make([]byte, 188)
  pkt[0] = 0x47
  for {
    if _, err := os.Stdout.Write(pkt); err != nil {
      return
    }
    time.Sleep(5 * time.Millisecond)
  }
}
`)
}

func writeIgnoreTermHelper(t *testing.T) string {
	t.Helper()
	return buildHelper(t, `package main
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
`)
}

func buildHelper(t *testing.T, code string) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "helper.go")
	bin := filepath.Join(dir, "helper")
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
