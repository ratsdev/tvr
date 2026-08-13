package epg_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jqjiang/tvr/internal/core/epg"
	"github.com/jqjiang/tvr/internal/core/store"
	"path/filepath"
)

func BenchmarkGuideQueryMediumSource(b *testing.B) {
	st, err := store.Open(filepath.Join(b.TempDir(), "tvr.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0"?><tv>`)
	const channels = 500
	for i := 0; i < channels; i++ {
		fmt.Fprintf(&sb, `<channel id="c%d"><display-name>Channel %d</display-name></channel>`, i, i)
		fmt.Fprintf(&sb, `<programme start="20260101120000 +0000" stop="20260101130000 +0000" channel="c%d"><title>Show %d</title></programme>`, i, i)
	}
	sb.WriteString(`</tv>`)
	body := sb.String()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	b.Cleanup(upstream.Close)

	src, err := st.CreateEPGSource(ctx, store.EPGSourceInput{Name: "G", URL: upstream.URL, RefreshInterval: "1h"}, time.Hour)
	if err != nil {
		b.Fatal(err)
	}
	svc := epg.New(st, b.TempDir(), 16<<20, nil)
	if err := svc.Refresh(ctx); err != nil {
		b.Fatal(err)
	}
	from := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := svc.QuerySourceGuide(src.ID, src.URL, false, epg.GuideQuery{
			From: from, To: to, Offset: 0, Limit: 50,
		}); err != nil {
			b.Fatal(err)
		}
	}
}
