package stream_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jqjiang/tvr/internal/core/stream"
	"github.com/jqjiang/tvr/internal/core/upstream"
)

func liveMPEGTSServer(t *testing.T, pkt []byte) *httptest.Server {
	t.Helper()
	if len(pkt) == 0 {
		pkt = makeTSPacket(0x00, 0x00)
		pkt[1] |= 0x40
	}
	block := bytesRepeat(pkt, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		flusher, _ := w.(http.Flusher)
		for {
			if r.Context().Err() != nil {
				return
			}
			if _, err := w.Write(block); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(10 * time.Millisecond)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func hangAfterHeadersServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func failStatusServer(t *testing.T, code int, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		http.Error(w, "nope", code)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func waitUpstreamID(t *testing.T, mgr *stream.Manager, channelID, wantID string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	var st stream.Status
	for time.Now().Before(deadline) {
		st = mgr.Status(channelID)
		if st.UpstreamID == wantID {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for upstream %s; last=%+v", wantID, st)
}

func TestRandomSticksThroughIdleWait(t *testing.T) {
	srvA := liveMPEGTSServer(t, nil)
	srvB := liveMPEGTSServer(t, makeTSPacket(0x00, 0x11))

	mgr := stream.NewManager(stream.Options{BufferSize: 64, IdleTimeout: 1500 * time.Millisecond, ConnTimeout: time.Second})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })
	src := upstream.Source{
		Policy: upstream.PolicyRandom,
		Upstreams: []upstream.Upstream{
			{ID: "a", URL: srvA.URL},
			{ID: "b", URL: srvB.URL},
		},
	}

	ctx1, cancel1 := context.WithTimeout(context.Background(), 3*time.Second)
	r1, err := mgr.Subscribe(ctx1, "rand", src)
	if err != nil {
		cancel1()
		t.Fatal(err)
	}
	buf := make([]byte, 188)
	if _, err := io.ReadFull(r1, buf); err != nil {
		_ = r1.Close()
		cancel1()
		t.Fatal(err)
	}
	picked := mgr.Status("rand").UpstreamID
	if picked != "a" && picked != "b" {
		_ = r1.Close()
		cancel1()
		t.Fatalf("picked=%q", picked)
	}
	_ = r1.Close()
	cancel1()

	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	r2, err := mgr.Subscribe(ctx2, "rand", src)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	if _, err := io.ReadFull(r2, buf); err != nil {
		t.Fatal(err)
	}
	if got := mgr.Status("rand").UpstreamID; got != picked {
		t.Fatalf("idle-wait resubscribe re-rolled %s -> %s", picked, got)
	}

	_ = r2.Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st := mgr.Status("rand")
		if st.State == "idle" && st.Viewers == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	ctx3, cancel3 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel3()
	r3, err := mgr.Subscribe(ctx3, "rand", src)
	if err != nil {
		t.Fatal(err)
	}
	defer r3.Close()
	if _, err := io.ReadFull(r3, buf); err != nil {
		t.Fatal(err)
	}
	if mgr.Status("rand").UpstreamID == "" {
		t.Fatal("expected a new session after idle")
	}
}

func TestFallbackSkipsDeadFirstWithoutFailingSession(t *testing.T) {
	var hitsDead atomic.Int64
	dead := failStatusServer(t, http.StatusBadGateway, &hitsDead)
	live := liveMPEGTSServer(t, nil)

	mgr := stream.NewManager(stream.Options{BufferSize: 64, IdleTimeout: 2 * time.Second, ConnTimeout: 400 * time.Millisecond})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })
	src := upstream.Source{
		Policy: upstream.PolicyFallback,
		Upstreams: []upstream.Upstream{
			{ID: "dead", URL: dead.URL},
			{ID: "live", URL: live.URL},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, err := mgr.Subscribe(ctx, "fb", src)
	if err != nil {
		t.Fatalf("fallback should skip dead #1: %v", err)
	}
	defer r.Close()
	buf := make([]byte, 188)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatal(err)
	}
	st := mgr.Status("fb")
	if st.UpstreamID != "live" {
		t.Fatalf("status=%+v", st)
	}
	if hitsDead.Load() == 0 {
		t.Fatal("expected a probe of the dead url")
	}
}

func TestFallbackExhaustBeforeReadyEndsSession(t *testing.T) {
	a := failStatusServer(t, http.StatusBadGateway, nil)
	b := failStatusServer(t, http.StatusBadGateway, nil)
	mgr := stream.NewManager(stream.Options{BufferSize: 64, IdleTimeout: 2 * time.Second, ConnTimeout: 300 * time.Millisecond})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })
	src := upstream.Source{
		Policy: upstream.PolicyFallback,
		Upstreams: []upstream.Upstream{
			{ID: "a", URL: a.URL},
			{ID: "b", URL: b.URL},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := mgr.Subscribe(ctx, "ex", src)
	if err == nil {
		t.Fatal("expected fail after exhausting urls")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("session did not failReady: %v", err)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	_, err = mgr.Subscribe(ctx2, "ex", src)
	if err == nil {
		t.Fatal("second subscribe should start a fresh attempt and fail again")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("stale session left in map; subscribe did not retry")
	}
}

func TestHangingFirstTimesOutAsError(t *testing.T) {
	hang := hangAfterHeadersServer(t)
	mgr := stream.NewManager(stream.Options{BufferSize: 64, IdleTimeout: 2 * time.Second, ConnTimeout: 200 * time.Millisecond})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	_, err := mgr.Subscribe(ctx, "hang", upstream.Fixed(upstream.Upstream{URL: hang.URL}))
	if !errors.Is(err, stream.ErrReadyTimeout) {
		t.Fatalf("expected ready timeout, got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("watchdog spun too long: %s", time.Since(start))
	}
}

func TestReadyStreamNotKilledByAttemptWatchdog(t *testing.T) {
	live := liveMPEGTSServer(t, nil)
	mgr := stream.NewManager(stream.Options{BufferSize: 64, IdleTimeout: 2 * time.Second, ConnTimeout: 150 * time.Millisecond})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := mgr.Subscribe(ctx, "live", upstream.Fixed(upstream.Upstream{URL: live.URL}))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	buf := make([]byte, 188)
	deadline := time.Now().Add(600 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := io.ReadFull(r, buf); err != nil {
			t.Fatalf("healthy stream killed by watchdog: %v", err)
		}
	}
}

func TestFallbackAfterReadyClearsPAT(t *testing.T) {
	patA := makeTSPacket(0x00, 0x00)
	patA[1] |= 0x40
	patA[5] = 0xAA
	mediaB := makeTSPacket(0x01, 0x00)
	mediaB[1] |= 0x40
	mediaB[5] = 0xBB

	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		_, _ = w.Write(bytesRepeat(patA, 24))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(srvA.Close)
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		flusher, _ := w.(http.Flusher)
		for {
			if r.Context().Err() != nil {
				return
			}
			_, _ = w.Write(bytesRepeat(mediaB, 8))
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(10 * time.Millisecond)
		}
	}))
	t.Cleanup(srvB.Close)

	mgr := stream.NewManager(stream.Options{BufferSize: 64, IdleTimeout: 3 * time.Second, ConnTimeout: time.Second})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })
	src := upstream.Source{
		Policy: upstream.PolicyFallback,
		Upstreams: []upstream.Upstream{
			{ID: "a", URL: srvA.URL},
			{ID: "b", URL: srvB.URL},
		},
	}
	ctx1, cancel1 := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel1()
	r1, err := mgr.Subscribe(ctx1, "pat", src)
	if err != nil {
		t.Fatal(err)
	}
	defer r1.Close()
	buf := make([]byte, 188)
	if _, err := io.ReadFull(r1, buf); err != nil {
		t.Fatal(err)
	}
	waitUpstreamID(t, mgr, "pat", "b", 5*time.Second)

	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	r2, err := mgr.Subscribe(ctx2, "pat", src)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	n, err := r2.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n < 188 {
		t.Fatalf("short read %d", n)
	}
	pid := uint16(buf[1]&0x1f)<<8 | uint16(buf[2])
	if pid == 0 {
		t.Fatal("late joiner received leftover PAT from the previous upstream")
	}
	if pid != 0x100 {
		t.Fatalf("expected pid 0x100 from second upstream, got 0x%x", pid)
	}
}

func TestFallbackHLSEndlistTriesNext(t *testing.T) {
	seg := bytesRepeat(makeTSPacket(0x00, 0x00), 8)
	seg[1] |= 0x40
	hls := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/index.m3u8") || r.URL.Path == "/":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, `#EXTM3U
#EXT-X-TARGETDURATION:1
#EXTINF:1,
seg.ts
#EXT-X-ENDLIST
`)
		case strings.HasSuffix(r.URL.Path, "/seg.ts"):
			_, _ = w.Write(seg)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(hls.Close)
	live := liveMPEGTSServer(t, makeTSPacket(0x00, 0x22))

	mgr := stream.NewManager(stream.Options{BufferSize: 64, IdleTimeout: 3 * time.Second, ConnTimeout: 2 * time.Second})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })
	src := upstream.Source{
		Policy: upstream.PolicyFallback,
		Upstreams: []upstream.Upstream{
			{ID: "hls", URL: hls.URL + "/index.m3u8"},
			{ID: "live", URL: live.URL},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	r, err := mgr.Subscribe(ctx, "hlsfb", src)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	buf := make([]byte, 188)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatal(err)
	}
	waitUpstreamID(t, mgr, "hlsfb", "live", 6*time.Second)
}

func TestFixedIgnoresOtherURLs(t *testing.T) {
	var hitsLive atomic.Int64
	dead := failStatusServer(t, http.StatusBadGateway, nil)
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsLive.Add(1)
		w.Header().Set("Content-Type", "video/mp2t")
		pkt := makeTSPacket(0x00, 0x00)
		pkt[1] |= 0x40
		_, _ = w.Write(bytesRepeat(pkt, 8))
	}))
	t.Cleanup(live.Close)

	mgr := stream.NewManager(stream.Options{BufferSize: 64, IdleTimeout: 2 * time.Second, ConnTimeout: 300 * time.Millisecond})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })
	src := upstream.Source{
		Policy:     upstream.PolicyFixed,
		FixedIndex: 0,
		Upstreams: []upstream.Upstream{
			{ID: "dead", URL: dead.URL},
			{ID: "live", URL: live.URL},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := mgr.Subscribe(ctx, "fix", src)
	if err == nil {
		t.Fatal("fixed should not walk to the backup")
	}
	if hitsLive.Load() != 0 {
		t.Fatalf("backup was contacted %d times", hitsLive.Load())
	}
}
