package stream

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/jqjiang/tvr/internal/core/mpegts"
	"github.com/jqjiang/tvr/internal/core/transcode"
	"github.com/jqjiang/tvr/internal/core/upstream"
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
