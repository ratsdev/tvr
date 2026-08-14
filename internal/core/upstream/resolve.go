package upstream

import (
	"strings"
	"unicode"
)

// ProxyRef is the resolved proxy used to build fetch URLs.
type ProxyRef struct {
	Policy     string
	Servers    []string
	FixedIndex int
}

// ParseProxyPolicy maps empty to fixed. Unknown values are an error.
func ParseProxyPolicy(raw string) (string, error) {
	return parsePickPolicy(raw, "proxy", false)
}

// Join prepends a proxy server prefix to a stream link.
func Join(server, link string) string {
	return strings.TrimRight(strings.TrimSpace(server), "/") + "/" + strings.TrimLeft(strings.TrimSpace(link), "/")
}

// StablePrimary is the deterministic fetch URL stored as channels.upstream_url.
func StablePrimary(link string, proxy *ProxyRef) string {
	link = strings.TrimSpace(link)
	if proxy == nil || len(proxy.Servers) == 0 {
		return link
	}
	idx := 0
	if proxy.Policy == PolicyFixed {
		idx = PickIndex(PolicyFixed, len(proxy.Servers), proxy.FixedIndex)
	}
	return Join(proxy.Servers[idx], link)
}

// Resolve returns session/test fetch URLs for a link and optional proxy.
func Resolve(link string, proxy *ProxyRef) []string {
	link = strings.TrimSpace(link)
	if proxy == nil || len(proxy.Servers) == 0 {
		if link == "" {
			return nil
		}
		return []string{link}
	}
	switch proxy.Policy {
	case PolicyRandom:
		i := PickIndex(PolicyRandom, len(proxy.Servers), 0)
		return []string{Join(proxy.Servers[i], link)}
	case PolicyFixed:
		i := PickIndex(PolicyFixed, len(proxy.Servers), proxy.FixedIndex)
		return []string{Join(proxy.Servers[i], link)}
	default:
		out := make([]string, 0, len(proxy.Servers))
		for _, srv := range proxy.Servers {
			out = append(out, Join(srv, link))
		}
		return out
	}
}

// ValidProxiedLink reports whether a channel link may be stored with a proxy.
func ValidProxiedLink(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	if strings.Contains(raw, "://") {
		return false
	}
	for _, r := range raw {
		if unicode.IsSpace(r) || r == 0 {
			return false
		}
	}
	return true
}
