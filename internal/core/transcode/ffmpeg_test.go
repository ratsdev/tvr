package transcode

import (
	"strings"
	"testing"

	"github.com/ratsdev/tvr/internal/core/upstream"
)

func TestBuildArgsOrderAndScale(t *testing.T) {
	profile := DefaultProfile()
	profile.MaxHeight = 720
	args, err := BuildArgs(profile, upstream.Upstream{
		URL: "https://example.com/live.m3u8",
		Headers: map[string]string{
			"Authorization": "Bearer secret",
			"User-Agent":    "custom-ua",
			"X-B":           "2",
			"X-A":           "1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\x00")
	if !strings.Contains(joined, "-nostdin") || !strings.Contains(joined, "-f\x00mpegts\x00pipe:1") {
		t.Fatalf("missing required options: %v", args)
	}
	iPos := indexOf(args, "-i")
	mapPos := indexOf(args, "-map")
	if iPos < 0 || mapPos < 0 || mapPos < iPos {
		t.Fatalf("mapping must follow -i: %v", args)
	}
	if got := args[indexOf(args, "-user_agent")+1]; got != "custom-ua" {
		t.Fatalf("user_agent=%q", got)
	}
	hdr := args[indexOf(args, "-headers")+1]
	if !strings.HasPrefix(hdr, "Authorization: Bearer secret\r\n") && !strings.Contains(hdr, "X-A: 1\r\n") {
		t.Fatalf("headers=%q", hdr)
	}
	if !strings.HasSuffix(hdr, "\r\n") {
		t.Fatalf("headers must end with CRLF: %q", hdr)
	}
	vf := args[indexOf(args, "-vf")+1]
	if !strings.Contains(vf, "720") {
		t.Fatalf("scale filter=%q", vf)
	}
	wl := args[indexOf(args, "-protocol_whitelist")+1]
	if !strings.Contains(wl, "crypto") || strings.Contains(wl, "file") {
		t.Fatalf("whitelist=%q", wl)
	}
}

func TestBuildArgsLiveMPEGTSTiming(t *testing.T) {
	args, err := BuildArgs(DefaultProfile(), upstream.Upstream{URL: "http://example.com/a.ts"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\x00")
	if strings.Contains(joined, "zerolatency") {
		t.Fatal("zerolatency causes MPEG-TS PCR/PTS jitter")
	}
	if got := args[indexOf(args, "-vsync")+1]; got != "cfr" {
		t.Fatalf("vsync=%q", got)
	}
	if got := args[indexOf(args, "-g")+1]; got != "50" {
		t.Fatalf("g=%q", got)
	}
	if got := args[indexOf(args, "-muxdelay")+1]; got != "0.1" {
		t.Fatalf("muxdelay=%q", got)
	}
	if got := args[indexOf(args, "-pcr_period")+1]; got != "20" {
		t.Fatalf("pcr_period=%q", got)
	}
}

func TestBuildArgsPlayerCompatible(t *testing.T) {
	args, err := BuildArgs(DefaultProfile(), upstream.Upstream{URL: "http://example.com/a.ts"})
	if err != nil {
		t.Fatal(err)
	}
	if got := args[indexOf(args, "-profile:v")+1]; got != "main" {
		t.Fatalf("profile=%q", got)
	}
	if got := args[indexOf(args, "-bf")+1]; got != "0" {
		t.Fatalf("bf=%q", got)
	}
	if got := args[indexOf(args, "-x264-params")+1]; !strings.Contains(got, "repeat-headers=1") {
		t.Fatalf("x264-params=%q", got)
	}
	if got := args[indexOf(args, "-bsf:v")+1]; got != "dump_extra" {
		t.Fatalf("bsf=%q", got)
	}
	if got := args[indexOf(args, "-ar")+1]; got != "48000" {
		t.Fatalf("ar=%q", got)
	}
	if got := args[indexOf(args, "-ac")+1]; got != "2" {
		t.Fatalf("ac=%q", got)
	}
}

func TestBuildArgsEvenScaleWithoutCap(t *testing.T) {
	args, err := BuildArgs(DefaultProfile(), upstream.Upstream{URL: "http://example.com/a.ts"})
	if err != nil {
		t.Fatal(err)
	}
	vf := args[indexOf(args, "-vf")+1]
	if vf != "scale=trunc(iw/2)*2:trunc(ih/2)*2" {
		t.Fatalf("vf=%q", vf)
	}
}

func TestBuildArgsRejectsInjectedHeaders(t *testing.T) {
	_, err := BuildArgs(DefaultProfile(), upstream.Upstream{
		URL:     "http://example.com/a.ts",
		Headers: map[string]string{"X-Test": "bad\r\nInjected: 1"},
	})
	if err == nil {
		t.Fatal("expected header rejection")
	}
}

func indexOf(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}
