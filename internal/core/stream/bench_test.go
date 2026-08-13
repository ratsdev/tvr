package stream_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jqjiang/tvr/internal/core/stream"
	"github.com/jqjiang/tvr/internal/core/upstream"
)

func BenchmarkFanout100Viewers(b *testing.B) {
	pkt := make([]byte, 188*8)
	for i := 0; i < len(pkt); i += 188 {
		pkt[i] = 0x47
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		flusher, _ := w.(http.Flusher)
		for {
			select {
			case <-r.Context().Done():
				return
			default:
				if _, err := w.Write(pkt); err != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
				time.Sleep(2 * time.Millisecond)
			}
		}
	}))
	b.Cleanup(origin.Close)

	mgr := stream.NewManager(stream.Options{
		BufferSize:  64,
		IdleTimeout: 2 * time.Second,
		ConnTimeout: 2 * time.Second,
	})
	b.Cleanup(func() { _ = mgr.Close(context.Background()) })

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		var wg sync.WaitGroup
		const viewers = 100
		errs := make(chan error, viewers)
		for v := 0; v < viewers; v++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r, err := mgr.Subscribe(ctx, "1", upstream.Fixed(upstream.Upstream{URL: origin.URL}))
				if err != nil {
					errs <- err
					return
				}
				defer r.Close()
				buf := make([]byte, 188)
				_, err = io.ReadFull(r, buf)
				errs <- err
			}()
		}
		wg.Wait()
		cancel()
		close(errs)
		for err := range errs {
			if err != nil {
				b.Fatal(err)
			}
		}
	}
}
