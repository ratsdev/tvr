package relay_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jqjiang/tvr/internal/relay"
)

func TestSharedUpstreamAndSlowClient(t *testing.T) {
	var upstreams atomic.Int32
	pat := makeTSPacket(0x00, 0x00)
	pat[1] |= 0x40
	payload := bytesRepeat(pat, 8)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreams.Add(1)
		w.Header().Set("Content-Type", "video/mp2t")
		flusher, _ := w.(http.Flusher)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if _, err := w.Write(payload); err != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
		}
	}))
	t.Cleanup(srv.Close)

	mgr := relay.NewManager(relay.Options{
		BufferSize:  4,
		IdleTimeout: 2 * time.Second,
		ConnTimeout: time.Second,
	})
	t.Cleanup(mgr.Close)

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	r1, err := mgr.Subscribe(ctx1, "1", relay.Upstream{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer r1.Close()

	ctx2, cancel2 := context.WithCancel(context.Background())
	r2, err := mgr.Subscribe(ctx2, "1", relay.Upstream{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if upstreams.Load() >= 1 && mgr.Status("1").Viewers == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := upstreams.Load(); got != 1 {
		t.Fatalf("expected 1 shared upstream, got %d", got)
	}
	if mgr.Status("1").Viewers != 2 {
		t.Fatalf("expected 2 viewers, got %d", mgr.Status("1").Viewers)
	}

	buf := make([]byte, 188*4)
	if _, err := io.ReadAtLeast(r1, buf, 188); err != nil {
		t.Fatalf("reader1: %v", err)
	}
	if _, err := io.ReadAtLeast(r2, buf, 188); err != nil {
		t.Fatalf("reader2: %v", err)
	}

	// Stop reading r2 so its tiny queue overflows and it is dropped.
	cancel2()
	_ = r2.Close()

	// Keep reading the fast viewer; it must survive.
	for i := 0; i < 50; i++ {
		if _, err := r1.Read(buf); err != nil {
			t.Fatalf("fast reader failed after slow drop: %v", err)
		}
	}
	if mgr.Status("1").Viewers != 1 {
		t.Fatalf("expected 1 remaining viewer, got %d", mgr.Status("1").Viewers)
	}
}

func TestPATStartupForLateJoiner(t *testing.T) {
	pat := makeTSPacket(0x00, 0x00)
	pat[1] |= 0x40
	block := bytesRepeat(pat, 50)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		for {
			if _, err := w.Write(block); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(15 * time.Millisecond)
			if r.Context().Err() != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)

	mgr := relay.NewManager(relay.Options{BufferSize: 64, IdleTimeout: 2 * time.Second})
	t.Cleanup(mgr.Close)

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	r1, err := mgr.Subscribe(ctx1, "7", relay.Upstream{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer r1.Close()

	buf := make([]byte, 188*4)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		n, err := r1.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		if n >= 188 && buf[0] == 0x47 {
			break
		}
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	r2, err := mgr.Subscribe(ctx2, "7", relay.Upstream{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()

	n, err := r2.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n < 188 || buf[0] != 0x47 {
		t.Fatalf("late joiner did not receive startup TS packet")
	}
	pid := uint16(buf[1]&0x1f)<<8 | uint16(buf[2])
	if pid != 0 {
		t.Fatalf("expected PAT pid 0, got %d", pid)
	}
}

func makeTSPacket(pidHi, pidLo byte) []byte {
	pkt := make([]byte, 188)
	pkt[0] = 0x47
	pkt[1] = pidHi
	pkt[2] = pidLo
	pkt[3] = 0x10 // payload only, continuity 0
	return pkt
}

func bytesRepeat(pkt []byte, n int) []byte {
	out := make([]byte, 0, len(pkt)*n)
	for i := 0; i < n; i++ {
		out = append(out, pkt...)
	}
	return out
}
