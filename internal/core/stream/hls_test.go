package stream_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ratsdev/tvr/internal/core/stream"
	"github.com/ratsdev/tvr/internal/core/upstream"
)

func TestHLSLiveStartsAtNewestSegment(t *testing.T) {
	segOld := bytesRepeat(makeTSPacket(0x00, 0x01), 8)
	segNew := bytesRepeat(makeTSPacket(0x00, 0x02), 8)
	segNew[1] |= 0x40

	var hitsOld, hitsNew atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/index.m3u8"):
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, `#EXTM3U
#EXT-X-TARGETDURATION:2
#EXT-X-MEDIA-SEQUENCE:10
#EXTINF:2,
old.ts
#EXTINF:2,
new.ts
`)
		case strings.HasSuffix(r.URL.Path, "/old.ts"):
			hitsOld.Add(1)
			_, _ = w.Write(segOld)
		case strings.HasSuffix(r.URL.Path, "/new.ts"):
			hitsNew.Add(1)
			_, _ = w.Write(segNew)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	mgr := stream.NewManager(stream.Options{BufferSize: 64, IdleTimeout: 2 * time.Second, ConnTimeout: 2 * time.Second})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, err := mgr.Subscribe(ctx, "live-edge", upstream.Fixed(upstream.Upstream{URL: srv.URL + "/index.m3u8"}))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	buf := make([]byte, 188)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatal(err)
	}
	if hitsOld.Load() != 0 {
		t.Fatalf("live start fetched old segment %d times", hitsOld.Load())
	}
	if hitsNew.Load() == 0 {
		t.Fatal("expected newest segment fetch")
	}
}

func TestHLSRelayConcatenatesSegments(t *testing.T) {
	seg1 := bytesRepeat(makeTSPacket(0x00, 0x00), 20)
	seg1[0] = 0x47
	seg1[1] |= 0x40
	seg2 := bytesRepeat(makeTSPacket(0x00, 0x11), 20)
	seg2[0] = 0x47

	var playlistHits int
	mux := http.NewServeMux()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/index.m3u8"):
			playlistHits++
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			seq := 100 + playlistHits
			_, _ = fmt.Fprintf(w, `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:2
#EXT-X-MEDIA-SEQUENCE:%d
#EXTINF:2,
seg1.ts
#EXTINF:2,
seg2.ts
`, seq)
		case strings.HasSuffix(r.URL.Path, "/seg1.ts"):
			w.Header().Set("Content-Type", "video/mp2t")
			_, _ = w.Write(seg1)
		case strings.HasSuffix(r.URL.Path, "/seg2.ts"):
			w.Header().Set("Content-Type", "video/mp2t")
			_, _ = w.Write(seg2)
		default:
			http.NotFound(w, r)
		}
	}))
	_ = mux
	t.Cleanup(srv.Close)

	mgr := stream.NewManager(stream.Options{BufferSize: 128, IdleTimeout: 2 * time.Second})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := mgr.Subscribe(ctx, "42", upstream.Fixed(upstream.Upstream{URL: srv.URL + "/index.m3u8"}))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	buf := make([]byte, 188*40)
	total := 0
	deadline := time.Now().Add(4 * time.Second)
	for total < len(seg1) && time.Now().Before(deadline) {
		n, err := r.Read(buf[total:])
		if n > 0 {
			total += n
			continue
		}
		if err != nil && err != io.EOF {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if total < 188 || buf[0] != 0x47 {
		t.Fatalf("expected mpeg-ts output, got %d bytes", total)
	}
	st := mgr.Status("42")
	if st.State != "streaming" && st.BytesSent == 0 {
		t.Fatalf("status=%+v", st)
	}
}

func TestHLSMasterWithEXTXMedia(t *testing.T) {
	seg := bytesRepeat(makeTSPacket(0x00, 0x00), 16)
	seg[1] |= 0x40

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/master.m3u8"):
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME="English",DEFAULT=YES,URI="audio.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=800000,AUDIO="audio"
media.m3u8
`)
		case strings.HasSuffix(r.URL.Path, "/media.m3u8"):
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, `#EXTM3U
#EXT-X-TARGETDURATION:2
#EXT-X-MEDIA-SEQUENCE:1
#EXTINF:2,
seg.ts
#EXT-X-ENDLIST
`)
		case strings.HasSuffix(r.URL.Path, "/seg.ts"):
			w.Header().Set("Content-Type", "video/mp2t")
			_, _ = w.Write(seg)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	mgr := stream.NewManager(stream.Options{BufferSize: 64, IdleTimeout: 2 * time.Second, ConnTimeout: 2 * time.Second})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := mgr.Subscribe(ctx, "77", upstream.Fixed(upstream.Upstream{URL: srv.URL + "/master.m3u8"}))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	buf := make([]byte, 188)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatal(err)
	}
	if buf[0] != 0x47 {
		t.Fatalf("expected mpeg-ts")
	}
}

func TestHLSSegmentSizeLimits(t *testing.T) {
	exact := make([]byte, stream.MaxSegmentBytes)
	pkt := makeTSPacket(0x00, 0x00)
	for i := 0; i+188 <= len(exact); i += 188 {
		copy(exact[i:], pkt)
	}
	over := append(append([]byte(nil), exact...), 0x47)

	t.Run("content-length-at-limit", func(t *testing.T) {
		assertHLSSegmentSubscribe(t, exact, true, true)
	})
	t.Run("content-length-over-limit", func(t *testing.T) {
		assertHLSSegmentSubscribe(t, over, true, false)
	})
	t.Run("chunked-at-limit", func(t *testing.T) {
		assertHLSSegmentSubscribe(t, exact, false, true)
	})
}

func assertHLSSegmentSubscribe(t *testing.T, segment []byte, withContentLength, wantOK bool) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/index.m3u8"):
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, `#EXTM3U
#EXT-X-TARGETDURATION:2
#EXT-X-MEDIA-SEQUENCE:1
#EXTINF:2,
seg.ts
#EXT-X-ENDLIST
`)
		case strings.HasSuffix(r.URL.Path, "/seg.ts"):
			w.Header().Set("Content-Type", "video/mp2t")
			if withContentLength {
				w.Header().Set("Content-Length", fmt.Sprintf("%d", len(segment)))
			}
			_, _ = w.Write(segment)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	mgr := stream.NewManager(stream.Options{BufferSize: 4096, IdleTimeout: 2 * time.Second, ConnTimeout: 3 * time.Second})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := mgr.Subscribe(ctx, "short-seg", upstream.Fixed(upstream.Upstream{URL: srv.URL + "/index.m3u8"}))
	if wantOK {
		if err != nil {
			t.Fatalf("expected accept: %v", err)
		}
		defer r.Close()
		buf := make([]byte, 188)
		if _, err := io.ReadFull(r, buf); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err == nil {
		_ = r.Close()
		t.Fatal("expected segment size rejection")
	}
}
