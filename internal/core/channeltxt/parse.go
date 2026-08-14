package channeltxt

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Entry is one name,url row from a channel list.
type Entry struct {
	Name string
	URL  string
}

// Parse reads CHANNEL_NAME,CHANNEL_URL lines from r.
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
		name, url, ok := strings.Cut(line, ",")
		name = strings.TrimSpace(name)
		url = strings.TrimSpace(url)
		if !ok || name == "" || url == "" {
			return nil, fmt.Errorf("line %d: expected CHANNEL_NAME,CHANNEL_URL", lineNo)
		}
		out = append(out, Entry{Name: name, URL: url})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
