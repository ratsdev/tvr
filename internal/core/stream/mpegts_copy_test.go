package stream

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/ratsdev/tvr/internal/core/mpegts"
	"github.com/ratsdev/tvr/internal/core/transcode"
	"github.com/ratsdev/tvr/internal/core/upstream"
	"time"
)

func TestCopyMPEGTSSegmentRejectsOversize(t *testing.T) {
	s := newSession("oversize", upstream.Fixed(upstream.Upstream{URL: "http://example.invalid"}), Options{
		BufferSize:  8192,
		IdleTimeout: time.Second,
		ConnTimeout: time.Second,
		Logger:      slog.Default(),
	}, transcode.DefaultProfile(), nil)
	v := s.addViewer(8192)
	t.Cleanup(func() { s.removeViewer(v.id) })

	data := make([]byte, MaxSegmentBytes+mpegts.PacketSize)
	pkt := make([]byte, mpegts.PacketSize)
	pkt[0] = 0x47
	for i := 0; i+mpegts.PacketSize <= len(data); i += mpegts.PacketSize {
		copy(data[i:], pkt)
	}

	err := s.copyMPEGTS(context.Background(), bytes.NewReader(data), mpegTSCopyOptions{
		maxBytes: MaxSegmentBytes,
		segment:  true,
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("got %v", err)
	}
}

func TestCopyMPEGTSSegmentRequiresMedia(t *testing.T) {
	s := newSession("empty-seg", upstream.Fixed(upstream.Upstream{URL: "http://example.invalid"}), Options{
		BufferSize:  8,
		IdleTimeout: time.Second,
		ConnTimeout: time.Second,
		Logger:      slog.Default(),
	}, transcode.DefaultProfile(), nil)
	v := s.addViewer(8)
	t.Cleanup(func() { s.removeViewer(v.id) })

	err := s.copyMPEGTS(context.Background(), bytes.NewReader([]byte("not-ts")), mpegTSCopyOptions{
		maxBytes: MaxSegmentBytes,
		segment:  true,
	})
	if err == nil || !strings.Contains(err.Error(), "mpeg-ts") {
		t.Fatalf("got %v", err)
	}
}

func TestLateJoinerWaitsForKeyframe(t *testing.T) {
	s := newSession("kf", upstream.Fixed(upstream.Upstream{URL: "http://example.invalid", Transcode: true}), Options{
		BufferSize:  32,
		IdleTimeout: time.Second,
		ConnTimeout: time.Second,
		Logger:      slog.Default(),
	}, transcode.DefaultProfile(), nil)
	v1 := s.addViewer(32)
	t.Cleanup(func() { s.removeViewer(v1.id) })
	s.markReady()

	pat := makeTSPacket(0x00, 0x00)
	pat[1] |= 0x40
	key := makeRAIPacket(0x100)
	delta := makeTSPacket(0x01, 0x00)

	s.broadcastFramed(append(append([]byte{}, pat...), key...))
	drainViewer(v1.ch)

	v2 := s.addViewer(32)
	t.Cleanup(func() { s.removeViewer(v2.id) })
	if !v2.waitKeyframe {
		t.Fatal("transcoded late joiner should wait for a keyframe after RAI was seen")
	}
	if got := drainViewer(v2.ch); len(got) != 0 {
		t.Fatalf("PAT/PMT must wait for the keyframe, got %d bytes", len(got))
	}

	s.broadcastFramed(delta)
	if got := drainViewer(v2.ch); len(got) != 0 {
		t.Fatalf("late joiner received %d bytes before keyframe", len(got))
	}

	s.broadcastFramed(append(append([]byte{}, delta...), key...))
	got := drainViewer(v2.ch)
	if len(got) < 2*mpegts.PacketSize {
		t.Fatalf("expected PAT then keyframe, got %d bytes", len(got))
	}
	if mpegts.PID(got) != 0 {
		t.Fatalf("expected PAT first, got pid=%d", mpegts.PID(got))
	}
	rai := got[mpegts.PacketSize:]
	if mpegts.PID(rai) != 0x100 || !mpegts.HasRandomAccess(rai[:mpegts.PacketSize]) {
		t.Fatalf("expected RAI pid 0x100 after PAT, got pid=%d rai=%v", mpegts.PID(rai), mpegts.HasRandomAccess(rai[:mpegts.PacketSize]))
	}
	if v2.waitKeyframe {
		t.Fatal("waitKeyframe should clear after RAI")
	}
}

func TestPassThroughLateJoinerIgnoresRAI(t *testing.T) {
	s := newSession("pass", upstream.Fixed(upstream.Upstream{URL: "http://example.invalid"}), Options{
		BufferSize:  32,
		IdleTimeout: time.Second,
		ConnTimeout: time.Second,
		Logger:      slog.Default(),
	}, transcode.DefaultProfile(), nil)
	v1 := s.addViewer(32)
	t.Cleanup(func() { s.removeViewer(v1.id) })
	s.markReady()

	pat := makeTSPacket(0x00, 0x00)
	pat[1] |= 0x40
	key := makeRAIPacket(0x100)
	delta := makeTSPacket(0x01, 0x00)
	s.broadcastFramed(append(append([]byte{}, pat...), key...))
	drainViewer(v1.ch)

	v2 := s.addViewer(32)
	t.Cleanup(func() { s.removeViewer(v2.id) })
	if v2.waitKeyframe {
		t.Fatal("pass-through late joiner must not wait for keyframes")
	}
	startup := drainViewer(v2.ch)
	if len(startup) < mpegts.PacketSize || mpegts.PID(startup) != 0 {
		t.Fatalf("expected PAT startup, got %d bytes pid=%d", len(startup), mpegts.PID(startup))
	}
	s.broadcastFramed(delta)
	if got := drainViewer(v2.ch); len(got) < mpegts.PacketSize {
		t.Fatal("pass-through late joiner should receive media immediately")
	}
}

func TestLateJoinerWithoutRAIGetsMedia(t *testing.T) {
	s := newSession("nora", upstream.Fixed(upstream.Upstream{URL: "http://example.invalid"}), Options{
		BufferSize:  32,
		IdleTimeout: time.Second,
		ConnTimeout: time.Second,
		Logger:      slog.Default(),
	}, transcode.DefaultProfile(), nil)
	v1 := s.addViewer(32)
	t.Cleanup(func() { s.removeViewer(v1.id) })
	s.markReady()

	pat := makeTSPacket(0x00, 0x00)
	pat[1] |= 0x40
	media := makeTSPacket(0x01, 0x00)
	s.broadcastFramed(append(append([]byte{}, pat...), media...))
	drainViewer(v1.ch)

	v2 := s.addViewer(32)
	t.Cleanup(func() { s.removeViewer(v2.id) })
	if v2.waitKeyframe {
		t.Fatal("pass-through without RAI must not block late joiners")
	}
	drainViewer(v2.ch)
	s.broadcastFramed(media)
	if got := drainViewer(v2.ch); len(got) < mpegts.PacketSize {
		t.Fatal("late joiner should receive media immediately when RAI was never seen")
	}
}

func makeTSPacket(pidHi, pidLo byte) []byte {
	pkt := make([]byte, mpegts.PacketSize)
	pkt[0] = mpegts.SyncByte
	pkt[1] = pidHi
	pkt[2] = pidLo
	pkt[3] = 0x10
	return pkt
}

func makeRAIPacket(pid uint16) []byte {
	pkt := make([]byte, mpegts.PacketSize)
	pkt[0] = mpegts.SyncByte
	pkt[1] = 0x40 | byte(pid>>8)
	pkt[2] = byte(pid)
	pkt[3] = 0x30
	pkt[4] = 1
	pkt[5] = 0x40
	return pkt
}

func drainViewer(ch <-chan []byte) []byte {
	var out []byte
	for {
		select {
		case b := <-ch:
			out = append(out, b...)
		default:
			return out
		}
	}
}
