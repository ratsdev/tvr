package epg

import "testing"

func TestPublicTvgID(t *testing.T) {
	if got := PublicTvgID(12, "cnn"); got != "epg12-cnn" {
		t.Fatalf("got %q", got)
	}
	if got := PublicTvgID(0, "cnn"); got != "" {
		t.Fatalf("empty source: %q", got)
	}
	if got := PublicTvgID(12, "  "); got != "" {
		t.Fatalf("empty tvg: %q", got)
	}
}
