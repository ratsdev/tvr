package epg

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jqjiang/tvr/internal/core/store"
)

// ChannelInfo is a searchable XMLTV channel from a source index.
type ChannelInfo struct {
	ID           string   `json:"id"`
	DisplayNames []string `json:"display_names"`
}

// ChannelSearchResult is a page of source channels plus the unpaged match count.
type ChannelSearchResult struct {
	Channels []ChannelInfo `json:"channels"`
	Total    int           `json:"total"`
}

const epgChannelSearchCap = 50

// Service refreshes, filters, and serves per-relay XMLTV data.
type Service struct {
	store     *store.Store
	cacheDir  string
	indexDir  string
	sourceDir string
	maxBytes  int64
	logger    *slog.Logger
	client    *http.Client

	mu                   sync.Mutex
	drainMu              sync.Mutex
	lastOK               time.Time
	lastError            string
	busy                 bool
	admitOpen            bool
	admitHeld            int
	indexes              map[int64]indexTag
	pendingSources       map[int64]struct{}
	pendingRelays        map[int64]struct{}
	pendingDeleteSources map[int64]struct{}
	cleanups             map[int64]*relayCleanupState
	sourceRev            map[int64]uint64
	nextRetry            map[int64]time.Time
	failBackoff          map[int64]time.Duration
	wake                 chan struct{}
	done                 chan struct{}
}

// New creates an EPG service.
func New(st *store.Store, dataDir string, maxBytes int64, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if maxBytes <= 0 {
		maxBytes = 64 << 20
	}
	s := &Service{
		store:     st,
		cacheDir:  filepath.Join(dataDir, "epg"),
		indexDir:  filepath.Join(dataDir, "epg-index"),
		sourceDir: filepath.Join(dataDir, "epg-sources"),
		maxBytes:  maxBytes,
		logger:    logger,
		client: &http.Client{
			Timeout: 2 * time.Minute,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
		admitOpen:            true,
		indexes:              map[int64]indexTag{},
		pendingSources:       map[int64]struct{}{},
		pendingRelays:        map[int64]struct{}{},
		pendingDeleteSources: map[int64]struct{}{},
		cleanups:             map[int64]*relayCleanupState{},
		sourceRev:            map[int64]uint64{},
		nextRetry:            map[int64]time.Time{},
		failBackoff:          map[int64]time.Duration{},
		wake:                 make(chan struct{}, 1),
		done:                 make(chan struct{}),
	}
	_ = os.MkdirAll(s.cacheDir, 0o755)
	_ = os.MkdirAll(s.indexDir, 0o755)
	_ = os.MkdirAll(s.sourceDir, 0o755)
	s.reconcileStartup(context.Background())
	return s
}

// Status is a snapshot of EPG refresh state.
type Status struct {
	LastOK     string `json:"last_ok,omitempty"`
	LastError  string `json:"last_error,omitempty"`
	Refreshing bool   `json:"refreshing"`
	CacheDir   string `json:"cache_dir"`
}

// Status returns the current EPG status.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	refreshing := s.busy || len(s.pendingSources) > 0 || len(s.pendingRelays) > 0 || len(s.pendingDeleteSources) > 0 || len(s.cleanups) > 0
	st := Status{
		LastError:  s.lastError,
		Refreshing: refreshing,
		CacheDir:   s.cacheDir,
	}
	if !s.lastOK.IsZero() {
		st.LastOK = s.lastOK.UTC().Format(time.RFC3339)
	}
	return st
}

// Start launches the background worker and due-source scheduler.
func (s *Service) Start(ctx context.Context) {
	go s.loop(ctx)
}

func (s *Service) nextDelay(ctx context.Context) time.Duration {
	now := time.Now()
	var soonest time.Duration = -1

	s.mu.Lock()
	for _, c := range s.cleanups {
		if c == nil || len(c.Slugs) == 0 {
			continue
		}
		var dueIn time.Duration
		if c.RetryAfter.IsZero() || !now.Before(c.RetryAfter) {
			dueIn = 0
		} else {
			dueIn = c.RetryAfter.Sub(now)
		}
		if soonest < 0 || dueIn < soonest {
			soonest = dueIn
		}
	}
	retries := make(map[int64]time.Time, len(s.nextRetry))
	for id, t := range s.nextRetry {
		retries[id] = t
	}
	s.mu.Unlock()

	sources, err := s.store.ListEnabledEPGSources(ctx)
	if err != nil {
		if soonest < 0 {
			return time.Hour
		}
		return soonest
	}
	for _, src := range sources {
		interval := src.RefreshInterval
		if interval < time.Minute {
			interval = time.Minute
		}
		var dueIn time.Duration
		if src.LastRefreshAt == nil {
			// Never refreshed: wait a full interval (UI Refresh for immediate first run).
			dueIn = interval
		} else {
			dueIn = interval - now.Sub(*src.LastRefreshAt)
			if dueIn < 0 {
				dueIn = 0
			}
		}
		if retryAt, ok := retries[src.ID]; ok {
			retryIn := retryAt.Sub(now)
			if retryIn < 0 {
				retryIn = 0
			}
			// Failed first fetch / overdue-but-backing-off: wake on backoff, not the full interval.
			if src.LastRefreshAt == nil || dueIn == 0 {
				dueIn = retryIn
			}
		}
		if soonest < 0 || dueIn < soonest {
			soonest = dueIn
		}
	}
	if soonest < 0 {
		return time.Hour
	}
	if soonest == 0 {
		// Overdue after downtime — refresh shortly, but not in the same tick as Start.
		return time.Second
	}
	return soonest
}

// Refresh enqueues all enabled sources and drains the queue (test/helper API).
func (s *Service) Refresh(ctx context.Context) error {
	if err := s.EnqueueRefreshEnabled(ctx); err != nil {
		return err
	}
	return s.drain(ctx)
}

// RefreshSource enqueues one source and drains the queue (test/helper API).
func (s *Service) RefreshSource(ctx context.Context, id int64) error {
	if err := s.EnqueueRefreshSource(id); err != nil {
		return err
	}
	return s.drain(ctx)
}

// SearchSourceChannels returns a page of indexed channels for an EPG source.
func (s *Service) SearchSourceChannels(sourceID int64, q string, limit int) ChannelSearchResult {
	src, err := s.store.GetEPGSource(context.Background(), sourceID)
	if err != nil {
		return pageChannelSearch(nil, limit)
	}
	_, _, tag, ok := s.eligibleSource(sourceID, src.URL)
	if !ok {
		return pageChannelSearch(nil, limit)
	}
	q = strings.ToLower(strings.TrimSpace(q))
	var out []ChannelInfo
	for _, ch := range tag.Channels {
		if q == "" || strings.Contains(strings.ToLower(ch.ID), q) || namesContain(ch.DisplayNames, q) {
			out = append(out, ch)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return channelSearchLess(q, out[i], out[j])
	})
	return pageChannelSearch(out, limit)
}

func channelSearchLess(q string, a, b ChannelInfo) bool {
	if q != "" {
		ea := strings.EqualFold(a.ID, q)
		eb := strings.EqualFold(b.ID, q)
		if ea != eb {
			return ea
		}
	}
	return guideChannelLess(channelSortName(a), a.ID, channelSortName(b), b.ID)
}

func pageChannelSearch(matches []ChannelInfo, limit int) ChannelSearchResult {
	if matches == nil {
		matches = []ChannelInfo{}
	}
	if limit <= 0 {
		limit = epgChannelSearchCap
	}
	total := len(matches)
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return ChannelSearchResult{Channels: matches, Total: total}
}

// CountEligibleSources returns how many of the given sources have a loaded index.
func (s *Service) CountEligibleSources(sourceIDs []int64) int {
	n := 0
	for _, id := range sourceIDs {
		src, err := s.store.GetEPGSource(context.Background(), id)
		if err != nil {
			continue
		}
		if _, _, _, ok := s.eligibleSource(id, src.URL); ok {
			n++
		}
	}
	return n
}

// FindSourceIDsByTvgID returns source IDs that contain a tvg-id.
func (s *Service) FindSourceIDsByTvgID(sourceIDs []int64, tvgID string) []int64 {
	tvgID = strings.TrimSpace(tvgID)
	if tvgID == "" {
		return nil
	}
	var out []int64
	for _, id := range sourceIDs {
		src, err := s.store.GetEPGSource(context.Background(), id)
		if err != nil {
			continue
		}
		_, _, tag, ok := s.eligibleSource(id, src.URL)
		if !ok {
			continue
		}
		for _, ch := range tag.Channels {
			if ch.ID == tvgID {
				out = append(out, id)
				break
			}
		}
	}
	return out
}

// OpenRelayCache returns a reader for a relay EPG file.
func (s *Service) OpenRelayCache(slug string) (*os.File, os.FileInfo, error) {
	path := filepath.Join(s.cacheDir, slug+".xml")
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	return f, fi, nil
}

func (s *Service) setOK() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastOK = time.Now().UTC()
	s.lastError = ""
}

func (s *Service) setError(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastError = msg
}

func indexFromDoc(doc *tvDocument) []ChannelInfo {
	out := make([]ChannelInfo, 0, len(doc.Channels))
	for id, ch := range doc.Channels {
		info := ChannelInfo{ID: id}
		for _, dn := range ch.DisplayNames {
			if dn.Text != "" {
				info.DisplayNames = append(info.DisplayNames, dn.Text)
			}
		}
		out = append(out, info)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return guideChannelLess(channelSortName(out[i]), out[i].ID, channelSortName(out[j]), out[j].ID)
	})
	return out
}

func namesContain(names []string, q string) bool {
	for _, n := range names {
		if strings.Contains(strings.ToLower(n), q) {
			return true
		}
	}
	return false
}

type tvDocument struct {
	GeneratorInfoName string
	Channels          map[string]tvChannel
	Programmes        []tvProgramme
}

type tvChannel struct {
	ID           string
	DisplayNames []tvText
	Icons        []tvIcon
}

type tvProgramme struct {
	Start   string
	Stop    string
	Channel string
	Titles  []tvText
	Descs   []tvText
	Cats    []tvText
	Icons   []tvIcon
}

type tvText struct {
	Lang string
	Text string
}

type tvIcon struct {
	Src string
}

func parseXMLTV(r io.Reader) (*tvDocument, error) {
	dec := xml.NewDecoder(r)
	dec.CharsetReader = charsetReader
	doc := &tvDocument{
		Channels:   map[string]tvChannel{},
		Programmes: []tvProgramme{},
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "tv":
			for _, attr := range se.Attr {
				if attr.Name.Local == "generator-info-name" {
					doc.GeneratorInfoName = attr.Value
				}
			}
		case "channel":
			var raw rawChannel
			if err := dec.DecodeElement(&raw, &se); err != nil {
				return nil, err
			}
			ch := tvChannel{ID: raw.ID}
			for _, dn := range raw.DisplayNames {
				ch.DisplayNames = append(ch.DisplayNames, tvText{Lang: dn.Lang, Text: strings.TrimSpace(dn.Text)})
			}
			for _, icon := range raw.Icons {
				if icon.Src != "" {
					ch.Icons = append(ch.Icons, tvIcon{Src: icon.Src})
				}
			}
			if ch.ID != "" {
				doc.Channels[ch.ID] = ch
			}
		case "programme":
			var raw rawProgramme
			if err := dec.DecodeElement(&raw, &se); err != nil {
				return nil, err
			}
			prog := tvProgramme{Start: raw.Start, Stop: raw.Stop, Channel: raw.Channel}
			for _, t := range raw.Titles {
				prog.Titles = append(prog.Titles, tvText{Lang: t.Lang, Text: strings.TrimSpace(t.Text)})
			}
			for _, d := range raw.Descs {
				prog.Descs = append(prog.Descs, tvText{Lang: d.Lang, Text: strings.TrimSpace(d.Text)})
			}
			for _, c := range raw.Categories {
				prog.Cats = append(prog.Cats, tvText{Lang: c.Lang, Text: strings.TrimSpace(c.Text)})
			}
			for _, icon := range raw.Icons {
				if icon.Src != "" {
					prog.Icons = append(prog.Icons, tvIcon{Src: icon.Src})
				}
			}
			if prog.Channel == "" || prog.Start == "" {
				continue
			}
			doc.Programmes = append(doc.Programmes, prog)
		}
	}
	return doc, nil
}

func writeXMLTV(w io.Writer, doc *tvDocument) error {
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	start := xml.StartElement{
		Name: xml.Name{Local: "tv"},
		Attr: []xml.Attr{{Name: xml.Name{Local: "generator-info-name"}, Value: "tvr"}},
	}
	if err := enc.EncodeToken(start); err != nil {
		return err
	}
	ids := make([]string, 0, len(doc.Channels))
	for id := range doc.Channels {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		ch := doc.Channels[id]
		raw := rawChannel{ID: ch.ID}
		for _, dn := range ch.DisplayNames {
			raw.DisplayNames = append(raw.DisplayNames, rawText{Lang: dn.Lang, Text: dn.Text})
		}
		for _, icon := range ch.Icons {
			raw.Icons = append(raw.Icons, rawIcon{Src: icon.Src})
		}
		if err := enc.Encode(raw); err != nil {
			return err
		}
	}
	progs := append([]tvProgramme(nil), doc.Programmes...)
	sort.SliceStable(progs, func(i, j int) bool {
		if progs[i].Channel == progs[j].Channel {
			if progs[i].Start == progs[j].Start {
				return preferText(progs[i].Titles) < preferText(progs[j].Titles)
			}
			return progs[i].Start < progs[j].Start
		}
		return progs[i].Channel < progs[j].Channel
	})
	for _, prog := range progs {
		raw := rawProgramme{Start: prog.Start, Stop: prog.Stop, Channel: prog.Channel}
		for _, t := range prog.Titles {
			raw.Titles = append(raw.Titles, rawText{Lang: t.Lang, Text: t.Text})
		}
		for _, d := range prog.Descs {
			raw.Descs = append(raw.Descs, rawText{Lang: d.Lang, Text: d.Text})
		}
		for _, c := range prog.Cats {
			raw.Categories = append(raw.Categories, rawText{Lang: c.Lang, Text: c.Text})
		}
		for _, icon := range prog.Icons {
			raw.Icons = append(raw.Icons, rawIcon{Src: icon.Src})
		}
		if err := enc.Encode(raw); err != nil {
			return err
		}
	}
	if err := enc.EncodeToken(start.End()); err != nil {
		return err
	}
	return enc.Flush()
}

func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "", "utf-8", "utf8", "us-ascii":
		return input, nil
	case "iso-8859-1", "latin1", "latin-1":
		return &latin1Reader{r: input}, nil
	default:
		return nil, fmt.Errorf("unsupported charset %q", charset)
	}
}

// latin1Reader converts ISO-8859-1 bytes to UTF-8.
type latin1Reader struct {
	r   io.Reader
	buf []byte
}

func (l *latin1Reader) Read(p []byte) (int, error) {
	if len(l.buf) > 0 {
		n := copy(p, l.buf)
		l.buf = l.buf[n:]
		return n, nil
	}
	tmp := make([]byte, min(len(p), 4096))
	n, err := l.r.Read(tmp)
	if n == 0 {
		return 0, err
	}
	var out []byte
	for i := 0; i < n; i++ {
		b := tmp[i]
		if b < 0x80 {
			out = append(out, b)
			continue
		}
		// U+0080..U+00FF as UTF-8
		out = append(out, 0xC0|b>>6, 0x80|b&0x3F)
	}
	n = copy(p, out)
	if n < len(out) {
		l.buf = out[n:]
	}
	return n, err
}

type rawChannel struct {
	XMLName      xml.Name  `xml:"channel"`
	ID           string    `xml:"id,attr"`
	DisplayNames []rawText `xml:"display-name"`
	Icons        []rawIcon `xml:"icon"`
}

type rawProgramme struct {
	XMLName    xml.Name  `xml:"programme"`
	Start      string    `xml:"start,attr"`
	Stop       string    `xml:"stop,attr,omitempty"`
	Channel    string    `xml:"channel,attr"`
	Titles     []rawText `xml:"title"`
	Descs      []rawText `xml:"desc"`
	Categories []rawText `xml:"category"`
	Icons      []rawIcon `xml:"icon"`
}

type rawText struct {
	Lang string `xml:"lang,attr,omitempty"`
	Text string `xml:",chardata"`
}

type rawIcon struct {
	Src string `xml:"src,attr"`
}
