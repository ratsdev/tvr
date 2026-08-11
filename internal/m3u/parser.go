package m3u

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
)

// Playlist is a parsed M3U/M3U8 extended playlist.
type Playlist struct {
	EPGURLs  []string
	Entries  []Entry
	Warnings []string
}

// Entry is one channel/stream from an M3U playlist.
type Entry struct {
	Name       string
	Number     int
	TvgID      string
	GroupTitle string
	LogoURL    string
	URL        string
	Headers    map[string]string
}

// Parse reads an extended M3U playlist from r.
func Parse(r io.Reader) (Playlist, error) {
	sc := bufio.NewScanner(r)
	// Large M3U lines (long URLs / attrs) are common.
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)

	var (
		out       Playlist
		pending   *Entry
		lineNo    int
		sawHeader bool
	)

	flush := func() {
		if pending == nil {
			return
		}
		if pending.URL == "" {
			out.Warnings = append(out.Warnings, fmt.Sprintf("line %d: EXTINF without URL, skipped", lineNo))
			pending = nil
			return
		}
		if pending.Name == "" {
			pending.Name = pending.URL
		}
		out.Entries = append(out.Entries, *pending)
		pending = nil
	}

	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			upper := strings.ToUpper(line)
			switch {
			case strings.HasPrefix(upper, "#EXTM3U"):
				sawHeader = true
				for _, u := range extractEPGURLs(line) {
					out.EPGURLs = appendUnique(out.EPGURLs, u)
				}
			case strings.HasPrefix(upper, "#EXTINF:"):
				flush()
				ent, err := parseEXTINF(line)
				if err != nil {
					out.Warnings = append(out.Warnings, fmt.Sprintf("line %d: %v", lineNo, err))
					pending = nil
					continue
				}
				pending = &ent
			case strings.HasPrefix(upper, "#EXTVLCOPT:"):
				if pending == nil {
					continue
				}
				applyVLCOpt(pending, strings.TrimSpace(line[len("#EXTVLCOPT:"):]))
			case strings.HasPrefix(upper, "#EXTGRP:"):
				if pending != nil && pending.GroupTitle == "" {
					pending.GroupTitle = strings.TrimSpace(line[len("#EXTGRP:"):])
				}
			default:
				// ignore other tags
			}
			continue
		}

		// URL line
		if pending == nil {
			// Bare URL without EXTINF — still importable.
			pending = &Entry{Name: line}
		}
		pending.URL = line
		flush()
	}
	if err := sc.Err(); err != nil {
		return Playlist{}, err
	}
	flush()

	if !sawHeader && len(out.Entries) == 0 {
		return Playlist{}, fmt.Errorf("not a valid M3U playlist")
	}
	return out, nil
}

func parseEXTINF(line string) (Entry, error) {
	// #EXTINF:duration attrs...,Name
	rest := line[len("#EXTINF:"):]
	meta, name, err := splitEXTINF(rest)
	if err != nil {
		return Entry{}, err
	}

	// Skip duration token.
	attrsPart := meta
	if i := strings.IndexAny(meta, " \t"); i >= 0 {
		attrsPart = strings.TrimSpace(meta[i+1:])
	} else {
		attrsPart = ""
	}

	attrs := parseAttrs(attrsPart)
	ent := Entry{
		Name:       name,
		TvgID:      firstAttr(attrs, "tvg-id", "tvg_id"),
		GroupTitle: firstAttr(attrs, "group-title", "group_title"),
		LogoURL:    firstAttr(attrs, "tvg-logo", "tvg_logo", "logo"),
		Headers:    map[string]string{},
	}
	if chno := firstAttr(attrs, "tvg-chno", "tvg-chnum", "channel-number", "chno"); chno != "" {
		if n, err := strconv.Atoi(chno); err == nil && n >= 0 {
			ent.Number = n
		}
	}
	if ent.Name == "" {
		ent.Name = firstAttr(attrs, "tvg-name", "tvg_name")
	}
	return ent, nil
}

func splitEXTINF(rest string) (meta, name string, err error) {
	inQuote := false
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case '"':
			inQuote = !inQuote
		case ',':
			if !inQuote {
				return strings.TrimSpace(rest[:i]), strings.TrimSpace(rest[i+1:]), nil
			}
		}
	}
	return "", "", fmt.Errorf("malformed EXTINF (missing comma)")
}

func parseAttrs(s string) map[string]string {
	out := map[string]string{}
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}
		eq := strings.IndexByte(s[i:], '=')
		if eq < 0 {
			break
		}
		key := strings.ToLower(strings.TrimSpace(s[i : i+eq]))
		i += eq + 1
		if i >= len(s) {
			break
		}
		var val string
		if s[i] == '"' {
			i++
			end := strings.IndexByte(s[i:], '"')
			if end < 0 {
				val = s[i:]
				i = len(s)
			} else {
				val = s[i : i+end]
				i += end + 1
			}
		} else {
			end := i
			for end < len(s) && s[end] != ' ' && s[end] != '\t' {
				end++
			}
			val = s[i:end]
			i = end
		}
		if key != "" {
			out[key] = val
		}
	}
	return out
}

func firstAttr(attrs map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(attrs[k]); v != "" {
			return v
		}
	}
	return ""
}

func applyVLCOpt(ent *Entry, opt string) {
	if ent.Headers == nil {
		ent.Headers = map[string]string{}
	}
	lower := strings.ToLower(opt)
	switch {
	case strings.HasPrefix(lower, "http-user-agent="):
		ent.Headers["User-Agent"] = strings.TrimSpace(opt[len("http-user-agent="):])
	case strings.HasPrefix(lower, "http-referrer="):
		ent.Headers["Referer"] = strings.TrimSpace(opt[len("http-referrer="):])
	case strings.HasPrefix(lower, "http-referer="):
		ent.Headers["Referer"] = strings.TrimSpace(opt[len("http-referer="):])
	}
}

func extractEPGURLs(header string) []string {
	// Strip "#EXTM3U" so attribute keys parse cleanly.
	rest := strings.TrimSpace(header)
	if len(rest) >= 7 && strings.EqualFold(rest[:7], "#EXTM3U") {
		rest = strings.TrimSpace(rest[7:])
	}
	attrs := parseAttrs(rest)
	var out []string
	for _, key := range []string{"url-tvg", "x-tvg-url", "tvg-url"} {
		raw := strings.TrimSpace(attrs[key])
		if raw == "" {
			continue
		}
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if u, err := url.Parse(part); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
				out = appendUnique(out, part)
			}
		}
	}
	return out
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// IsHTTPStream reports whether the URL is an http(s) stream we can relay.
func IsHTTPStream(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}
