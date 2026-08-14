package upstream

import "testing"

func TestJoin(t *testing.T) {
	got := Join("http://1.2.3.4:9901/udp/", "239.1.2.3:1234")
	if got != "http://1.2.3.4:9901/udp/239.1.2.3:1234" {
		t.Fatalf("join=%q", got)
	}
	got = Join("http://1.2.3.4:9901/udp", "/239.1.2.3:1234")
	if got != "http://1.2.3.4:9901/udp/239.1.2.3:1234" {
		t.Fatalf("join=%q", got)
	}
}

func TestStablePrimaryIgnoresRandom(t *testing.T) {
	p := &ProxyRef{
		Policy:  PolicyRandom,
		Servers: []string{"http://a/udp/", "http://b/udp/"},
	}
	for i := 0; i < 20; i++ {
		got := StablePrimary("1.2.3.4:1", p)
		if got != "http://a/udp/1.2.3.4:1" {
			t.Fatalf("stable=%q", got)
		}
	}
}

func TestStablePrimaryFixed(t *testing.T) {
	p := &ProxyRef{
		Policy:     PolicyFixed,
		Servers:    []string{"http://a/udp/", "http://b/udp/"},
		FixedIndex: 1,
	}
	if got := StablePrimary("1.2.3.4:1", p); got != "http://b/udp/1.2.3.4:1" {
		t.Fatalf("stable=%q", got)
	}
}

func TestResolveFailoverListsAll(t *testing.T) {
	p := &ProxyRef{
		Policy:  PolicyFailover,
		Servers: []string{"http://a/udp/", "http://b/udp/"},
	}
	got := Resolve("1.2.3.4:1", p)
	if len(got) != 2 || got[0] != "http://a/udp/1.2.3.4:1" || got[1] != "http://b/udp/1.2.3.4:1" {
		t.Fatalf("resolve=%v", got)
	}
}

func TestResolveDirect(t *testing.T) {
	got := Resolve("http://example.com/a.ts", nil)
	if len(got) != 1 || got[0] != "http://example.com/a.ts" {
		t.Fatalf("resolve=%v", got)
	}
}

func TestValidProxiedLink(t *testing.T) {
	if !ValidProxiedLink("239.1.2.3:1234") {
		t.Fatal("expected valid")
	}
	if ValidProxiedLink("http://example.com/x") {
		t.Fatal("scheme should be rejected")
	}
	if ValidProxiedLink("a b") {
		t.Fatal("space should be rejected")
	}
	if ValidProxiedLink("") {
		t.Fatal("blank should be rejected")
	}
}

func TestParseProxyPolicy(t *testing.T) {
	p, err := ParseProxyPolicy(" FAILOVER ")
	if err != nil || p != PolicyFailover {
		t.Fatalf("got %q err=%v", p, err)
	}
	if _, err := ParseProxyPolicy("fallback"); err == nil {
		t.Fatal("expected error")
	}
}
