package channeltxt

import (
	"strings"
	"testing"
)

func TestParseSkipsCommentsBlanksAndSplitsOnFirstComma(t *testing.T) {
	in := "# header\n\n  # indented\nCCTV-1,http://example.com/1.ts\nNews,http://example.com/a.ts,extra\n"
	got, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("entries=%d %+v", len(got), got)
	}
	if got[0] != (Entry{Name: "CCTV-1", URL: "http://example.com/1.ts"}) {
		t.Fatalf("first=%+v", got[0])
	}
	if got[1] != (Entry{Name: "News", URL: "http://example.com/a.ts,extra"}) {
		t.Fatalf("first-comma split=%+v", got[1])
	}
}

func TestParseCRLF(t *testing.T) {
	got, err := Parse(strings.NewReader("A,http://example.com/a.ts\r\nB,http://example.com/b.ts\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "A" || got[1].Name != "B" {
		t.Fatalf("crlf=%+v", got)
	}
}

func TestParseBadLineNumber(t *testing.T) {
	_, err := Parse(strings.NewReader("# ok\nnot-a-pair\n"))
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("want line 2 error, got %v", err)
	}
	_, err = Parse(strings.NewReader(",http://example.com/a.ts\n"))
	if err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("empty name: %v", err)
	}
	_, err = Parse(strings.NewReader("Name,\n"))
	if err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("empty url: %v", err)
	}
}
