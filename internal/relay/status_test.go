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

func TestStatusIdleAfterViewerCloses(t *testing.T) {
	pkt := makeTSPacket(0x00, 0x00)
	pkt[1] |= 0x40
	payload := bytesRepeat(pkt, 32)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		flusher, _ := w.(http.Flusher)
		for {
			select {
			case <-r.Context().Done():
				return
			default:
				_, _ = w.Write(payload)
				if flusher != nil {
					flusher.Flush()
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
	}))
	t.Cleanup(srv.Close)

	mgr := relay.NewManager(relay.Options{
		BufferSize:  64,
		IdleTimeout: 3 * time.Second,
	})
	t.Cleanup(mgr.Close)

	ctx, cancel := context.WithCancel(context.Background())
	r, err := mgr.Subscribe(ctx, "9", relay.Upstream{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 188*4)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st := mgr.Status("9")
		if st.State == "streaming" && st.Viewers == 1 {
			break
		}
		_, _ = r.Read(buf)
		time.Sleep(10 * time.Millisecond)
	}
	if st := mgr.Status("9"); st.State != "streaming" || st.Viewers != 1 {
		t.Fatalf("expected streaming with 1 viewer, got %+v", st)
	}

	cancel()
	_ = r.Close()

	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		st := mgr.Status("9")
		if st.Viewers == 0 && st.State == "idle" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	st := mgr.Status("9")
	t.Fatalf("expected idle with 0 viewers after close, got %+v", st)
}

func TestStatusStreamingAfterUpstreamReconnect(t *testing.T) {
	pkt := makeTSPacket(0x00, 0x00)
	pkt[1] |= 0x40
	payload := bytesRepeat(pkt, 16)

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		w.Header().Set("Content-Type", "video/mp2t")
		flusher, _ := w.(http.Flusher)
		// First connection: send a burst then close so the session reconnects.
		writes := 8
		if n > 1 {
			writes = 64
		}
		for i := 0; i < writes; i++ {
			if r.Context().Err() != nil {
				return
			}
			_, _ = w.Write(payload)
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(5 * time.Millisecond)
		}
	}))
	t.Cleanup(srv.Close)

	mgr := relay.NewManager(relay.Options{
		BufferSize:  64,
		IdleTimeout: 3 * time.Second,
		ConnTimeout: 3 * time.Second,
	})
	t.Cleanup(mgr.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r, err := mgr.Subscribe(ctx, "reconnect", relay.Upstream{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	buf := make([]byte, 188*4)
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		_, _ = r.Read(buf)
		st := mgr.Status("reconnect")
		if hits.Load() >= 2 && st.State == "streaming" && st.Viewers == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	st := mgr.Status("reconnect")
	t.Fatalf("expected streaming after reconnect, hits=%d status=%+v", hits.Load(), st)
}

func TestStatusIdleDoesNotRequireDrainingReader(t *testing.T) {
	// Ensure Close() alone (without cancel) clears streaming state.
	pkt := makeTSPacket(0x00, 0x00)
	payload := bytesRepeat(pkt, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		for {
			if r.Context().Err() != nil {
				return
			}
			_, _ = w.Write(payload)
			time.Sleep(5 * time.Millisecond)
		}
	}))
	t.Cleanup(srv.Close)

	mgr := relay.NewManager(relay.Options{BufferSize: 32, IdleTimeout: 2 * time.Second})
	t.Cleanup(mgr.Close)

	r, err := mgr.Subscribe(context.Background(), "3", relay.Upstream{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 188)
	_, _ = io.ReadAtLeast(r, buf, 188)
	_ = r.Close()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if st := mgr.Status("3"); st.Viewers == 0 && st.State == "idle" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("status=%+v", mgr.Status("3"))
}
