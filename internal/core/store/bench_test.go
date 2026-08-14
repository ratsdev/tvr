package store_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/ratsdev/tvr/internal/core/store"
)

func BenchmarkListChannels1000(b *testing.B) {
	st, err := store.Open(filepath.Join(b.TempDir(), "tvr.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	for i := 0; i < 1000; i++ {
		if _, err := st.CreateChannel(ctx, store.ChannelInput{
			Name:        fmt.Sprintf("Channel %d", i),
			UpstreamURL: fmt.Sprintf("http://example.com/%d.ts", i),
		}); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := st.ListChannels(ctx); err != nil {
			b.Fatal(err)
		}
	}
}
