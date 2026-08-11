package relay

import (
	"testing"
	"time"
)

func TestMediaPlaylistReloadWait(t *testing.T) {
	// RFC 8216 §6.3.4: after a change, wait the last segment duration.
	if got := mediaPlaylistReloadWait(11, 10, true); got != 10*time.Second {
		t.Fatalf("changed wait=%s, want 10s", got)
	}
	// Unchanged playlist: half target duration.
	wantUnchanged := time.Duration(5.5 * float64(time.Second))
	if got := mediaPlaylistReloadWait(11, 10, false); got != wantUnchanged {
		t.Fatalf("unchanged wait=%s, want %s", got, wantUnchanged)
	}
	// Fall back to target duration when EXTINF is missing.
	if got := mediaPlaylistReloadWait(8, 0, true); got != 8*time.Second {
		t.Fatalf("fallback changed wait=%s, want 8s", got)
	}
	if got := mediaPlaylistReloadWait(0, 0, false); got != 3*time.Second {
		t.Fatalf("default unchanged wait=%s, want 3s", got)
	}
}
