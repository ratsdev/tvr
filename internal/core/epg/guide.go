package epg

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jqjiang/tvr/internal/utils"
)

// Sentinel errors for guide queries.
var (
	ErrGuideRefreshRequired = errors.New("epg source refresh required")
	ErrGuideInvalidQuery    = errors.New("invalid guide query")
)

const (
	maxGuideWindow    = 24 * time.Hour
	defaultGuideLimit = 50
	maxGuideLimit     = 100
)

// GuideQuery filters a source guide window.
type GuideQuery struct {
	From   time.Time
	To     time.Time
	Q      string
	Offset int
	Limit  int
}

// GuideProgramme is one programme in a guide response.
type GuideProgramme struct {
	Start       string `json:"start"`
	Stop        string `json:"stop"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Category    string `json:"category,omitempty"`
	Icon        string `json:"icon,omitempty"`
}

// GuideChannel is one channel row with overlapping programmes.
type GuideChannel struct {
	ID          string           `json:"id"`
	DisplayName string           `json:"display_name"`
	Icon        string           `json:"icon,omitempty"`
	Programmes  []GuideProgramme `json:"programmes"`
}

// GuideResult is a bounded source-guide page.
type GuideResult struct {
	FetchedAt string         `json:"fetched_at"`
	Stale     bool           `json:"stale"`
	Total     int            `json:"total"`
	Offset    int            `json:"offset"`
	Limit     int            `json:"limit"`
	Channels  []GuideChannel `json:"channels"`
}

// QuerySourceGuide returns one page of channels and overlapping programmes for a source.
func (s *Service) QuerySourceGuide(id int64, sourceURL string, stale bool, q GuideQuery) (GuideResult, error) {
	if err := normalizeGuideQuery(&q); err != nil {
		return GuideResult{}, err
	}
	manifest, xmlPath, _, ok := s.eligibleSource(id, sourceURL)
	if !ok {
		return GuideResult{}, ErrGuideRefreshRequired
	}

	channels, err := scanGuideChannels(xmlPath, q.Q)
	if err != nil {
		return GuideResult{}, err
	}
	total := len(channels)
	if q.Offset > total {
		q.Offset = total
	}
	end := q.Offset + q.Limit
	if end > total {
		end = total
	}
	page := channels[q.Offset:end]
	allowed := make(map[string]struct{}, len(page))
	for _, ch := range page {
		allowed[ch.ID] = struct{}{}
	}
	progs, err := scanGuideProgrammes(xmlPath, allowed, q.From, q.To)
	if err != nil {
		return GuideResult{}, err
	}
	for i := range page {
		list := progs[page[i].ID]
		if list == nil {
			list = []GuideProgramme{}
		}
		page[i].Programmes = list
	}
	return GuideResult{
		FetchedAt: manifest.FetchedAt,
		Stale:     stale,
		Total:     total,
		Offset:    q.Offset,
		Limit:     q.Limit,
		Channels:  page,
	}, nil
}

func normalizeGuideQuery(q *GuideQuery) error {
	if q.From.IsZero() || q.To.IsZero() {
		return fmt.Errorf("%w: from and to are required", ErrGuideInvalidQuery)
	}
	if !q.To.After(q.From) {
		return fmt.Errorf("%w: to must be after from", ErrGuideInvalidQuery)
	}
	if q.To.Sub(q.From) > maxGuideWindow {
		return fmt.Errorf("%w: window must be <= 24h", ErrGuideInvalidQuery)
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	if q.Limit <= 0 {
		q.Limit = defaultGuideLimit
	}
	if q.Limit > maxGuideLimit {
		q.Limit = maxGuideLimit
	}
	q.Q = strings.ToLower(strings.TrimSpace(q.Q))
	q.From = q.From.UTC()
	q.To = q.To.UTC()
	return nil
}

type guideChannelScan struct {
	ID          string
	DisplayName string
	Icon        string
	namesLower  []string
}

func scanGuideChannels(path, q string) ([]GuideChannel, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dec := xml.NewDecoder(f)
	dec.CharsetReader = charsetReader
	var scanned []guideChannelScan
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "channel" {
			continue
		}
		var raw rawChannel
		if err := dec.DecodeElement(&raw, &se); err != nil {
			return nil, err
		}
		if strings.TrimSpace(raw.ID) == "" {
			continue
		}
		names := make([]tvText, 0, len(raw.DisplayNames))
		lower := make([]string, 0, len(raw.DisplayNames)+1)
		for _, dn := range raw.DisplayNames {
			text := strings.TrimSpace(dn.Text)
			if text == "" {
				continue
			}
			names = append(names, tvText{Lang: dn.Lang, Text: text})
			lower = append(lower, strings.ToLower(text))
		}
		display := preferText(names)
		lower = append(lower, strings.ToLower(raw.ID))
		icon := ""
		if len(raw.Icons) > 0 {
			icon = strings.TrimSpace(raw.Icons[0].Src)
		}
		if q != "" {
			match := strings.Contains(strings.ToLower(raw.ID), q)
			if !match {
				for _, n := range lower {
					if strings.Contains(n, q) {
						match = true
						break
					}
				}
			}
			if !match {
				continue
			}
		}
		scanned = append(scanned, guideChannelScan{
			ID:          raw.ID,
			DisplayName: display,
			Icon:        icon,
			namesLower:  lower,
		})
	}
	sort.SliceStable(scanned, func(i, j int) bool {
		ai := guideSortKey(scanned[i].DisplayName, scanned[i].ID)
		aj := guideSortKey(scanned[j].DisplayName, scanned[j].ID)
		if cmp := utils.NaturalCompare(ai, aj); cmp != 0 {
			return cmp < 0
		}
		return utils.NaturalLess(scanned[i].ID, scanned[j].ID)
	})
	out := make([]GuideChannel, 0, len(scanned))
	for _, ch := range scanned {
		out = append(out, GuideChannel{
			ID:          ch.ID,
			DisplayName: ch.DisplayName,
			Icon:        ch.Icon,
			Programmes:  []GuideProgramme{},
		})
	}
	return out, nil
}

func scanGuideProgrammes(path string, allowed map[string]struct{}, from, to time.Time) (map[string][]GuideProgramme, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dec := xml.NewDecoder(f)
	dec.CharsetReader = charsetReader
	out := map[string][]GuideProgramme{}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "programme" {
			continue
		}
		var raw rawProgramme
		if err := dec.DecodeElement(&raw, &se); err != nil {
			return nil, err
		}
		if _, ok := allowed[raw.Channel]; !ok {
			continue
		}
		start, err := ParseXMLTVTime(raw.Start)
		if err != nil {
			continue
		}
		stop, err := ParseXMLTVTime(raw.Stop)
		if err != nil || !stop.After(start) {
			continue
		}
		// Half-open overlap: start < to && stop > from
		if !start.Before(to) || !stop.After(from) {
			continue
		}
		titles := make([]tvText, 0, len(raw.Titles))
		for _, t := range raw.Titles {
			titles = append(titles, tvText{Lang: t.Lang, Text: strings.TrimSpace(t.Text)})
		}
		descs := make([]tvText, 0, len(raw.Descs))
		for _, d := range raw.Descs {
			descs = append(descs, tvText{Lang: d.Lang, Text: strings.TrimSpace(d.Text)})
		}
		cats := make([]tvText, 0, len(raw.Categories))
		for _, c := range raw.Categories {
			cats = append(cats, tvText{Lang: c.Lang, Text: strings.TrimSpace(c.Text)})
		}
		icon := ""
		if len(raw.Icons) > 0 {
			icon = strings.TrimSpace(raw.Icons[0].Src)
		}
		out[raw.Channel] = append(out[raw.Channel], GuideProgramme{
			Start:       start.UTC().Format(time.RFC3339),
			Stop:        stop.UTC().Format(time.RFC3339),
			Title:       preferText(titles),
			Description: preferText(descs),
			Category:    preferText(cats),
			Icon:        icon,
		})
	}
	for ch := range out {
		sort.SliceStable(out[ch], func(i, j int) bool {
			if out[ch][i].Start == out[ch][j].Start {
				return out[ch][i].Title < out[ch][j].Title
			}
			return out[ch][i].Start < out[ch][j].Start
		})
	}
	return out, nil
}

// ParseXMLTVTime parses common XMLTV timestamp forms into UTC.
func ParseXMLTVTime(raw string) (time.Time, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	if strings.HasSuffix(s, "Z") || strings.HasSuffix(s, "z") {
		base := strings.TrimSuffix(strings.TrimSuffix(s, "Z"), "z")
		for _, layout := range []string{"20060102150405", "200601021504", "2006010215", "20060102"} {
			if t, err := time.ParseInLocation(layout, base, time.UTC); err == nil {
				return t.UTC(), nil
			}
		}
		return time.Time{}, fmt.Errorf("invalid xmltv time %q", raw)
	}
	parts := strings.Fields(s)
	if len(parts) == 2 {
		off := parts[1]
		joined := parts[0] + " " + off
		var layouts []string
		if strings.Contains(off, ":") {
			layouts = []string{
				"20060102150405 -07:00",
				"200601021504 -07:00",
				"2006010215 -07:00",
				"20060102 -07:00",
			}
		} else {
			layouts = []string{
				"20060102150405 -0700",
				"200601021504 -0700",
				"2006010215 -0700",
				"20060102 -0700",
			}
		}
		for _, layout := range layouts {
			if t, err := time.Parse(layout, joined); err == nil {
				return t.UTC(), nil
			}
		}
	}
	for _, layout := range []string{"20060102150405", "200601021504", "2006010215", "20060102"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid xmltv time %q", raw)
}

func preferText(items []tvText) string {
	for _, it := range items {
		lang := strings.ToLower(strings.TrimSpace(it.Lang))
		text := strings.TrimSpace(it.Text)
		if text == "" {
			continue
		}
		if lang == "en" || strings.HasPrefix(lang, "en-") || strings.HasPrefix(lang, "en_") {
			return text
		}
	}
	for _, it := range items {
		if text := strings.TrimSpace(it.Text); text != "" {
			return text
		}
	}
	return ""
}

func guideSortKey(name, id string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return id
	}
	return name
}
