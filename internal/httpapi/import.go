package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jqjiang/tvr/internal/core/m3u"
	"github.com/jqjiang/tvr/internal/core/store"
	"github.com/jqjiang/tvr/internal/utils"
)

type importRelayRequest struct {
	Content      string `json:"content"`
	URL          string `json:"url"`
	RelayName    string `json:"relay_name"`
	RelaySlug    string `json:"relay_slug"`
	IgnoreGroups bool   `json:"ignore_groups"`
	ImportEPG    *bool  `json:"import_epg"`
}

type importRelayResult struct {
	RelayID            int64    `json:"relay_id"`
	Slug               string   `json:"slug"`
	ChannelsCreated    int      `json:"channels_created"`
	ChannelsReused     int      `json:"channels_reused"`
	MembershipsCreated int      `json:"memberships_created"`
	GroupsCreated      int      `json:"groups_created"`
	EPGImported        int      `json:"epg_imported"`
	UnmatchedTvgIDs    []string `json:"unmatched_tvg_ids,omitempty"`
	AmbiguousTvgIDs    []string `json:"ambiguous_tvg_ids,omitempty"`
	Warnings           []string `json:"warnings,omitempty"`
	PlaylistURL        string   `json:"playlist_url"`
	EPGURL             string   `json:"epg_url"`
}

func (s *Server) handleImportRelay(w http.ResponseWriter, r *http.Request) {
	var in importRelayRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	content := strings.TrimSpace(in.Content)
	if content == "" && strings.TrimSpace(in.URL) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("content or url is required"))
		return
	}
	if content == "" {
		fetched, err := s.fetchM3U(r.Context(), strings.TrimSpace(in.URL))
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("fetch m3u: %w", err))
			return
		}
		content = fetched
	}
	pl, err := m3u.Parse(strings.NewReader(content))
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("parse m3u: %w", err))
		return
	}

	importEPG := true
	if in.ImportEPG != nil {
		importEPG = *in.ImportEPG
	}

	warnings := append([]string{}, pl.Warnings...)
	seenUpstream := map[string]struct{}{}
	entries := make([]store.ImportRelayEntry, 0, len(pl.Entries))
	for _, ent := range pl.Entries {
		if !m3u.IsHTTPStream(ent.URL) {
			warnings = append(warnings, fmt.Sprintf("skipped non-http(s) url for %q", ent.Name))
			continue
		}
		up := strings.TrimSpace(ent.URL)
		if _, ok := seenUpstream[up]; ok {
			warnings = append(warnings, fmt.Sprintf("skipped duplicate upstream url for %q", ent.Name))
			continue
		}
		seenUpstream[up] = struct{}{}

		groupTitle := ent.GroupTitle
		if in.IgnoreGroups {
			groupTitle = "Channels"
		}
		entries = append(entries, store.ImportRelayEntry{
			Name:        ent.Name,
			LogoURL:     ent.LogoURL,
			UpstreamURL: up,
			Headers:     ent.Headers,
			GroupTitle:  groupTitle,
			TvgID:       strings.TrimSpace(ent.TvgID),
		})
	}

	var epgURLs []string
	if importEPG {
		seenEPG := map[string]struct{}{}
		for _, epgURL := range pl.EPGURLs {
			epgURL = strings.TrimSpace(epgURL)
			if epgURL == "" {
				continue
			}
			if _, ok := seenEPG[epgURL]; ok {
				continue
			}
			if !m3u.IsHTTPStream(epgURL) {
				warnings = append(warnings, fmt.Sprintf("skipped invalid epg url %q", epgURL))
				continue
			}
			seenEPG[epgURL] = struct{}{}
			epgURLs = append(epgURLs, epgURL)
		}
	}

	spec := store.ImportRelayInput{
		Name:               strings.TrimSpace(in.RelayName),
		Slug:               strings.TrimSpace(in.RelaySlug),
		EPGURLs:            epgURLs,
		DefaultEPGInterval: s.cfg.EPGDefaultEvery,
		Entries:            entries,
	}

	// Match playlist tvg-ids against cached url-tvg sources only. Sole-source
	// binding for uncached first imports is handled by ImportRelay.
	var unmatched, ambiguous []string
	if len(epgURLs) > 0 {
		existingEPGIDs := make([]int64, 0, len(epgURLs))
		for _, epgURL := range epgURLs {
			if src, err := s.store.FindEPGSourceByURL(r.Context(), epgURL); err == nil {
				existingEPGIDs = append(existingEPGIDs, src.ID)
			}
		}
		eligible := s.epg.CountEligibleSources(existingEPGIDs)
		for i := range spec.Entries {
			tvgID := spec.Entries[i].TvgID
			if tvgID == "" {
				continue
			}
			var matches []int64
			if len(existingEPGIDs) > 0 {
				matches = s.epg.FindSourceIDsByTvgID(existingEPGIDs, tvgID)
			}
			switch len(matches) {
			case 0:
				if eligible > 0 {
					unmatched = utils.AppendUnique(unmatched, tvgID)
				}
			case 1:
				id := matches[0]
				spec.Entries[i].EPGSourceID = &id
			default:
				ambiguous = utils.AppendUnique(ambiguous, tvgID)
			}
		}
	}

	imported, err := s.workflows.ImportRelay(r.Context(), spec)
	if err != nil {
		s.writeWorkflowError(w, err)
		return
	}

	base := s.publicBaseURL(r)
	result := importRelayResult{
		RelayID:            imported.RelayID,
		Slug:               imported.Slug,
		ChannelsCreated:    imported.ChannelsCreated,
		ChannelsReused:     imported.ChannelsReused,
		MembershipsCreated: imported.MembershipsCreated,
		GroupsCreated:      imported.GroupsCreated,
		EPGImported:        imported.EPGImported,
		UnmatchedTvgIDs:    unmatched,
		AmbiguousTvgIDs:    ambiguous,
		Warnings:           warnings,
		PlaylistURL:        fmt.Sprintf("%s/r/%s/playlist.m3u", base, imported.Slug),
		EPGURL:             fmt.Sprintf("%s/r/%s/epg.xml", base, imported.Slug),
	}
	if len(imported.EPGSourceIDs) > 0 {
		result.Warnings = append(result.Warnings, "EPG refresh queued")
	}
	if len(unmatched) > 0 || len(ambiguous) > 0 {
		result.Warnings = append(result.Warnings, "some tvg-id mappings need review after EPG refresh")
	}
	if len(result.Warnings) > 40 {
		result.Warnings = append(result.Warnings[:40], fmt.Sprintf("…and %d more warnings", len(result.Warnings)-40))
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) fetchM3U(ctx context.Context, rawURL string) (string, error) {
	if !m3u.IsHTTPStream(rawURL) {
		return "", fmt.Errorf("url must be http(s)")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "tvr/1.0")
	req.Header.Set("Accept", "application/vnd.apple.mpegurl, audio/mpegurl, text/plain, */*")
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	const maxBytes = 32 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxBytes {
		return "", fmt.Errorf("playlist exceeds %d bytes", maxBytes)
	}
	return string(data), nil
}
