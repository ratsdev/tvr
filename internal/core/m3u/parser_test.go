package m3u

import (
	"strings"
	"testing"
)

func TestParseBasic(t *testing.T) {
	src := `#EXTM3U url-tvg="https://epg.example.com/guide.xml"
#EXTINF:-1 tvg-id="cctv1" tvg-chno="1" tvg-logo="https://logo/1.png" group-title="News",CCTV-1
#EXTVLCOPT:http-user-agent=Mozilla/5.0
http://example.com/cctv1.ts
#EXTINF:-1 tvg-id="cctv2" group-title="News",CCTV-2
https://example.com/cctv2.ts
#EXTINF:-1,UDP Only
udp://@239.1.1.1:1234
`
	pl, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(pl.EPGURLs) != 1 || pl.EPGURLs[0] != "https://epg.example.com/guide.xml" {
		t.Fatalf("epg urls: %#v", pl.EPGURLs)
	}
	if len(pl.Entries) != 3 {
		t.Fatalf("entries=%d", len(pl.Entries))
	}
	e0 := pl.Entries[0]
	if e0.Name != "CCTV-1" || e0.TvgID != "cctv1" || e0.Number != 1 || e0.GroupTitle != "News" {
		t.Fatalf("entry0: %+v", e0)
	}
	if e0.Headers["User-Agent"] != "Mozilla/5.0" {
		t.Fatalf("headers: %#v", e0.Headers)
	}
	if !IsHTTPStream(e0.URL) || IsHTTPStream(pl.Entries[2].URL) {
		t.Fatal("http stream detection failed")
	}
}

func TestParseQuotedCommaInName(t *testing.T) {
	src := `#EXTM3U
#EXTINF:-1 tvg-id="x" group-title="A, B",Channel, Extra
http://example.com/x.ts
`
	pl, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if pl.Entries[0].Name != "Channel, Extra" {
		t.Fatalf("name=%q", pl.Entries[0].Name)
	}
	if pl.Entries[0].GroupTitle != "A, B" {
		t.Fatalf("group=%q", pl.Entries[0].GroupTitle)
	}
}

func TestParseAttrsSpaces(t *testing.T) {
	attrs := parseAttrs(`tvg-id="a b" group-title=News tvg-chno="12"`)
	if attrs["tvg-id"] != "a b" || attrs["group-title"] != "News" || attrs["tvg-chno"] != "12" {
		t.Fatalf("%#v", attrs)
	}
}
