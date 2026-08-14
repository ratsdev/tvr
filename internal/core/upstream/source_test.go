package upstream

import "testing"

func TestStartIndexRandomVaries(t *testing.T) {
	src := Source{
		Policy: PolicyRandom,
		Upstreams: []Upstream{
			{ID: "a", URL: "http://example.com/a.ts"},
			{ID: "b", URL: "http://example.com/b.ts"},
		},
	}
	seen := map[int]int{}
	for i := 0; i < 80; i++ {
		seen[src.StartIndex()]++
	}
	if len(seen) < 2 {
		t.Fatalf("random start index stuck: %v", seen)
	}
}

func TestStartIndexFixed(t *testing.T) {
	src := Source{
		Policy:     PolicyFixed,
		FixedIndex: 1,
		Upstreams: []Upstream{
			{URL: "http://example.com/a.ts"},
			{URL: "http://example.com/b.ts"},
		},
	}
	if src.StartIndex() != 1 {
		t.Fatalf("fixed index=%d", src.StartIndex())
	}
}

func TestHostOmitsUserinfoAndQuery(t *testing.T) {
	got := Host("http://user:secret@example.com:8080/stream.ts?token=abc")
	if got != "example.com:8080" {
		t.Fatalf("host=%q", got)
	}
}

func TestParsePolicy(t *testing.T) {
	p, err := ParsePolicy(" RANDOM ")
	if err != nil || p != PolicyRandom {
		t.Fatalf("got %q err=%v", p, err)
	}
	if _, err := ParsePolicy("round-robin"); err == nil {
		t.Fatal("expected error")
	}
	p, err = ParsePolicy("fallback")
	if err != nil || p != PolicyFailover {
		t.Fatalf("legacy fallback: got %q err=%v", p, err)
	}
	p, err = ParsePolicy("failover")
	if err != nil || p != PolicyFailover {
		t.Fatalf("got %q err=%v", p, err)
	}
}
