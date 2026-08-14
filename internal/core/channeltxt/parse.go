package channeltxt

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/ratsdev/tvr/internal/core/upstream"
)

// Entry is one name + target row from a channel list.
type Entry struct {
	Name      string
	URL       string
	ProxyName string
}

// Parse reads CHANNEL_NAME,CHANNEL_URL or CHANNEL_NAME,HOST:PORT@PROXY_NAME.
// Empty lines and lines whose first non-space character is '#' are skipped.
func Parse(r io.Reader) ([]Entry, error) {
	sc := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)

	var out []Entry
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, raw, ok := strings.Cut(line, ",")
		name = strings.TrimSpace(name)
		raw = strings.TrimSpace(raw)
		if !ok || name == "" || raw == "" {
			return nil, fmt.Errorf("line %d: expected CHANNEL_NAME,CHANNEL_URL", lineNo)
		}
		url, proxy, err := splitTarget(raw)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		out = append(out, Entry{Name: name, URL: url, ProxyName: proxy})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func splitTarget(raw string) (url, proxyName string, err error) {
	if isHTTPTarget(raw) {
		return raw, "", nil
	}
	at := strings.LastIndex(raw, "@")
	if at < 0 {
		return raw, "", nil
	}
	link := strings.TrimSpace(raw[:at])
	name := strings.TrimSpace(raw[at+1:])
	if !validHostPort(link) || name == "" {
		return "", "", fmt.Errorf("expected HOST:PORT@PROXY_NAME")
	}
	return link, name, nil
}

func isHTTPTarget(raw string) bool {
	lower := strings.ToLower(raw)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

func validHostPort(link string) bool {
	if !upstream.ValidProxiedLink(link) {
		return false
	}
	i := strings.LastIndex(link, ":")
	return i > 0 && i < len(link)-1
}
