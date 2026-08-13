package stream

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// MaxSegmentBytes is the maximum accepted HLS media segment size.
const MaxSegmentBytes = 8 << 20

const maxSeenSegments = 256

type hlsPlaylist struct {
	IsMaster       bool
	EndList        bool
	TargetDuration float64
	MediaSequence  int64
	Segments       []hlsSegment
	Variants       []hlsVariant
}

type hlsSegment struct {
	URI      string
	Duration float64
	Seq      int64
}

type hlsVariant struct {
	URI       string
	Bandwidth int
}

func (s *session) pumpHLS(ctx context.Context, playlistURL string, headers http.Header, initial []byte) error {
	seen := newSeenSet(maxSeenSegments)
	var lastSeq int64 = -1
	var lastSegDuration float64
	started := false

	for {
		select {
		case <-s.stopCh:
			return nil
		case <-ctx.Done():
			return nil
		default:
		}
		if s.viewerCount() == 0 {
			return nil
		}

		cycleStart := time.Now()
		var (
			body []byte
			err  error
		)
		if initial != nil {
			body = initial
			initial = nil
		} else {
			body, err = s.httpGetBytes(ctx, playlistURL, headers, 4<<20)
			if err != nil {
				return err
			}
		}

		pl, err := parseHLSPlaylist(body)
		if err != nil {
			return err
		}
		if pl.IsMaster {
			if len(pl.Variants) == 0 {
				return fmt.Errorf("hls master playlist has no variants")
			}
			variant := pickVariant(pl.Variants)
			playlistURL, err = resolveURL(playlistURL, variant.URI)
			if err != nil {
				return err
			}
			s.opts.Logger.Debug("hls selected variant", "channel_id", s.channelID, "url", playlistURL, "bandwidth", variant.Bandwidth)
			initial = nil
			continue
		}
		if len(pl.Segments) == 0 {
			if pl.EndList {
				if !s.everReady.Load() {
					return fmt.Errorf("hls endlist with no segments")
				}
				return errStreamEnded
			}
			wait := mediaPlaylistReloadWait(pl.TargetDuration, lastSegDuration, false)
			if err := sleepCtx(ctx, s.stopCh, wait); err != nil {
				return nil
			}
			continue
		}

		// Live remux: start at the newest segment (live edge).
		startIdx := 0
		if !started {
			started = true
			if !pl.EndList && len(pl.Segments) > 1 {
				startIdx = len(pl.Segments) - 1
			}
			for i := 0; i < startIdx; i++ {
				seen.add(pl.Segments[i].URI)
				if pl.Segments[i].Seq > lastSeq {
					lastSeq = pl.Segments[i].Seq
				}
			}
		}

		playlistChanged := false
		for i := startIdx; i < len(pl.Segments); i++ {
			seg := pl.Segments[i]
			if seen.has(seg.URI) {
				continue
			}
			if seg.Seq > 0 && lastSeq >= 0 && seg.Seq <= lastSeq {
				seen.add(seg.URI)
				continue
			}
			if !isTSSegmentURI(seg.URI) {
				return fmt.Errorf("unsupported hls media segment %q", seg.URI)
			}
			segURL, err := resolveURL(playlistURL, seg.URI)
			if err != nil {
				return err
			}
			if err := s.fetchAndBroadcastSegment(ctx, segURL, headers); err != nil {
				return err
			}
			seen.add(seg.URI)
			playlistChanged = true
			if seg.Duration > 0 {
				lastSegDuration = seg.Duration
			}
			if seg.Seq > lastSeq {
				lastSeq = seg.Seq
			}
		}

		if pl.EndList {
			return errStreamEnded
		}

		wait := mediaPlaylistReloadWait(pl.TargetDuration, lastSegDuration, playlistChanged)
		if elapsed := time.Since(cycleStart); elapsed < wait {
			wait -= elapsed
		} else {
			wait = 0
		}
		if wait > 0 {
			if err := sleepCtx(ctx, s.stopCh, wait); err != nil {
				return nil
			}
		}
	}
}

// mediaPlaylistReloadWait returns the RFC 8216 §6.3.4 media-playlist reload delay.
// After a playlist change, wait the last segment's duration; otherwise half the target duration.
func mediaPlaylistReloadWait(targetDuration, lastSegDuration float64, playlistChanged bool) time.Duration {
	last := lastSegDuration
	if last <= 0 {
		last = targetDuration
	}
	if last <= 0 {
		last = 6
	}
	target := targetDuration
	if target <= 0 {
		target = last
	}
	sec := target / 2
	if playlistChanged {
		sec = last
	}
	wait := time.Duration(sec * float64(time.Second))
	if wait < 250*time.Millisecond {
		wait = 250 * time.Millisecond
	}
	return wait
}

func (s *session) fetchAndBroadcastSegment(ctx context.Context, segURL string, headers http.Header) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, segURL, nil)
	if err != nil {
		return err
	}
	copyHeaders(req, headers)

	resp, err := s.opts.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("segment status %d", resp.StatusCode)
	}
	if resp.ContentLength > MaxSegmentBytes {
		return fmt.Errorf("segment exceeds %d bytes", MaxSegmentBytes)
	}
	return s.copyMPEGTS(ctx, resp.Body, mpegTSCopyOptions{
		maxBytes: MaxSegmentBytes,
		segment:  true,
	})
}

func (s *session) httpGetBytes(ctx context.Context, rawURL string, headers http.Header, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	copyHeaders(req, headers)
	resp, err := s.opts.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("upstream status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return data, nil
}

func parseHLSPlaylist(body []byte) (hlsPlaylist, error) {
	text := string(body)
	if !strings.Contains(text, "#EXTM3U") {
		return hlsPlaylist{}, fmt.Errorf("not an hls playlist")
	}
	var pl hlsPlaylist
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), 2<<20)
	var pendingDuration float64
	var pendingBandwidth int
	seq := pl.MediaSequence
	sawMedia := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "#EXT-X-STREAM-INF:"):
			pl.IsMaster = true
			pendingBandwidth = attrInt(line, "BANDWIDTH")
		case strings.HasPrefix(upper, "#EXT-X-MEDIA:"):
			// Rendition tags (AUDIO/SUBTITLES/etc.) are ignored; variants come from STREAM-INF.
			continue
		case strings.HasPrefix(upper, "#EXT-X-KEY:"):
			return hlsPlaylist{}, fmt.Errorf("encrypted hls is not supported")
		case strings.HasPrefix(upper, "#EXT-X-BYTERANGE:"):
			return hlsPlaylist{}, fmt.Errorf("hls byte ranges are not supported")
		case strings.HasPrefix(upper, "#EXT-X-MAP:"):
			return hlsPlaylist{}, fmt.Errorf("fmp4/ext-x-map hls is not supported")
		case strings.HasPrefix(upper, "#EXT-X-ENDLIST"):
			pl.EndList = true
		case strings.HasPrefix(upper, "#EXT-X-TARGETDURATION:"):
			pl.TargetDuration, _ = strconv.ParseFloat(strings.TrimSpace(line[len("#EXT-X-TARGETDURATION:"):]), 64)
		case strings.HasPrefix(upper, "#EXT-X-MEDIA-SEQUENCE:"):
			pl.MediaSequence, _ = strconv.ParseInt(strings.TrimSpace(line[len("#EXT-X-MEDIA-SEQUENCE:"):]), 10, 64)
			seq = pl.MediaSequence
		case strings.HasPrefix(upper, "#EXTINF:"):
			sawMedia = true
			rest := line[len("#EXTINF:"):]
			if i := strings.IndexByte(rest, ','); i >= 0 {
				rest = rest[:i]
			}
			pendingDuration, _ = strconv.ParseFloat(strings.TrimSpace(rest), 64)
		case strings.HasPrefix(line, "#"):
			// ignore
		default:
			if pl.IsMaster && !sawMedia {
				pl.Variants = append(pl.Variants, hlsVariant{URI: line, Bandwidth: pendingBandwidth})
				pendingBandwidth = 0
				continue
			}
			pl.Segments = append(pl.Segments, hlsSegment{
				URI:      line,
				Duration: pendingDuration,
				Seq:      seq,
			})
			seq++
			pendingDuration = 0
		}
	}
	if err := sc.Err(); err != nil {
		return hlsPlaylist{}, err
	}
	if pl.TargetDuration <= 0 {
		pl.TargetDuration = 6
	}
	return pl, nil
}

func pickVariant(variants []hlsVariant) hlsVariant {
	best := variants[0]
	for _, v := range variants[1:] {
		if v.Bandwidth > best.Bandwidth {
			best = v
		}
	}
	return best
}

func attrInt(line, key string) int {
	upper := strings.ToUpper(line)
	key = strings.ToUpper(key) + "="
	i := strings.Index(upper, key)
	if i < 0 {
		return 0
	}
	rest := line[i+len(key):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	n, _ := strconv.Atoi(rest[:end])
	return n
}

func resolveURL(baseRaw, ref string) (string, error) {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref, nil
	}
	base, err := url.Parse(baseRaw)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(ref)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(u).String(), nil
}

func copyHeaders(req *http.Request, headers http.Header) {
	req.Header.Set("User-Agent", "tvr/1.0")
	req.Header.Set("Accept", "*/*")
	for k, vals := range headers {
		for _, v := range vals {
			req.Header.Set(k, v)
		}
	}
}

func sleepCtx(ctx context.Context, stop <-chan struct{}, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-stop:
		return context.Canceled
	case <-t.C:
		return nil
	}
}

func looksLikeHLS(contentType string, body []byte) bool {
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "mpegurl") || strings.Contains(ct, "m3u8") {
		return true
	}
	trim := strings.TrimSpace(string(body))
	return strings.HasPrefix(trim, "#EXTM3U")
}

func isTSSegmentURI(raw string) bool {
	path := raw
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".mp4"),
		strings.HasSuffix(lower, ".m4s"),
		strings.HasSuffix(lower, ".m4v"),
		strings.HasSuffix(lower, ".m4a"),
		strings.HasSuffix(lower, ".cmfv"),
		strings.HasSuffix(lower, ".cmfa"),
		strings.HasSuffix(lower, ".aac"),
		strings.HasSuffix(lower, ".vtt"),
		strings.HasSuffix(lower, ".mp3"):
		return false
	default:
		return true
	}
}

type seenSet struct {
	max   int
	items map[string]struct{}
	order []string
}

func newSeenSet(max int) *seenSet {
	return &seenSet{
		max:   max,
		items: make(map[string]struct{}),
	}
}

func (s *seenSet) has(uri string) bool {
	_, ok := s.items[uri]
	return ok
}

func (s *seenSet) add(uri string) {
	if _, ok := s.items[uri]; ok {
		return
	}
	s.items[uri] = struct{}{}
	s.order = append(s.order, uri)
	for len(s.order) > s.max {
		old := s.order[0]
		s.order = s.order[1:]
		delete(s.items, old)
	}
}
