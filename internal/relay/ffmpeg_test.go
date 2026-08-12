package relay

import (
	"strings"
	"testing"
)

func TestBuildFFmpegArgsOrderAndScale(t *testing.T) {
	profile := DefaultTranscodeProfile()
	profile.MaxHeight = 720
	args, err := buildFFmpegArgs(profile, Upstream{
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

func TestBuildFFmpegArgsEvenScaleWithoutCap(t *testing.T) {
	args, err := buildFFmpegArgs(DefaultTranscodeProfile(), Upstream{URL: "http://example.com/a.ts"})
	if err != nil {
		t.Fatal(err)
	}
	vf := args[indexOf(args, "-vf")+1]
	if vf != "scale=trunc(iw/2)*2:trunc(ih/2)*2" {
		t.Fatalf("vf=%q", vf)
	}
}

func TestBuildFFmpegArgsRejectsInjectedHeaders(t *testing.T) {
	_, err := buildFFmpegArgs(DefaultTranscodeProfile(), Upstream{
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
