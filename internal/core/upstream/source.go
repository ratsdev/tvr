package upstream

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"strings"
)

const (
	PolicyFixed    = "fixed"
	PolicyRandom   = "random"
	PolicyFailover = "failover"
	// PolicyFallback is the legacy channel policy name. ParsePolicy maps it to PolicyFailover.
	PolicyFallback = "fallback"
)

// ErrEmpty means Source has no usable HTTP(S) URL.
var ErrEmpty = errors.New("no upstream urls")

// Upstream is one HTTP(S) origin for a channel.
type Upstream struct {
	ID      string
	URL     string
	Headers map[string]string
	// Candidates are resolved fetch URLs. Empty means [URL].
	Candidates []string
	// Transcode and Revision are copied onto Source by Fixed for test helpers.
	Transcode bool
	Revision  string
}

// Source is the ordered set of upstreams and the policy for choosing among them.
type Source struct {
	Policy     string
	Upstreams  []Upstream
	FixedIndex int
	Transcode  bool
	Revision   string
}

// Fixed wraps a single upstream as a fixed Source.
func Fixed(up Upstream) Source {
	return Source{
		Policy:    PolicyFixed,
		Upstreams: []Upstream{up},
		Transcode: up.Transcode,
		Revision:  up.Revision,
	}
}

// ParsePolicy maps empty to fixed. Unknown values are an error.
func ParsePolicy(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", PolicyFixed:
		return PolicyFixed, nil
	case PolicyRandom:
		return PolicyRandom, nil
	case PolicyFallback, PolicyFailover:
		return PolicyFailover, nil
	default:
		return "", fmt.Errorf("upstream policy must be fixed, random, or failover")
	}
}

// Normalize drops blank URLs and canonicalizes policy and fixed index.
func Normalize(src Source) (Source, error) {
	if len(src.Upstreams) == 0 {
		return Source{}, ErrEmpty
	}
	clean := make([]Upstream, 0, len(src.Upstreams))
	for _, up := range src.Upstreams {
		if strings.TrimSpace(up.URL) == "" {
			continue
		}
		clean = append(clean, up)
	}
	if len(clean) == 0 {
		return Source{}, ErrEmpty
	}
	src.Upstreams = clean
	if p, err := ParsePolicy(src.Policy); err != nil {
		src.Policy = PolicyFixed
	} else {
		src.Policy = p
	}
	if src.FixedIndex < 0 || src.FixedIndex >= len(src.Upstreams) {
		src.FixedIndex = 0
	}
	return src, nil
}

// PickIndex chooses a start index for Fixed / Random / first.
func PickIndex(policy string, n, fixed int) int {
	if n <= 0 {
		return 0
	}
	switch policy {
	case PolicyRandom:
		return rand.IntN(n)
	case PolicyFixed:
		if fixed >= 0 && fixed < n {
			return fixed
		}
	}
	return 0
}

// StartIndex is the first URL to try for a new session.
func (s Source) StartIndex() int {
	return PickIndex(s.Policy, len(s.Upstreams), s.FixedIndex)
}

// FetchURLs returns Candidates, or [URL] when Candidates is empty.
func (u Upstream) FetchURLs() []string {
	if len(u.Candidates) > 0 {
		return u.Candidates
	}
	if strings.TrimSpace(u.URL) != "" {
		return []string{u.URL}
	}
	return nil
}

// IsFallback reports whether a dead pump should walk to the next URL.
func (s Source) IsFallback() bool {
	return s.Policy == PolicyFailover || s.Policy == PolicyFallback
}

// Host is the URL host (host:port), never userinfo or query.
func Host(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}
