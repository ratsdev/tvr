package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ratsdev/tvr/internal/utils"
)

// ImportRelayEntry is one preclassified playlist entry ready for persistence.
type ImportRelayEntry struct {
	Name        string
	LogoURL     string
	UpstreamURL string
	Headers     map[string]string
	GroupTitle  string
	TvgID       string
	EPGSourceID *int64
}

// ImportRelayInput is the parsed, preclassified payload for an atomic import.
type ImportRelayInput struct {
	Name               string
	Slug               string
	EPGURLs            []string
	DefaultEPGInterval time.Duration
	Entries            []ImportRelayEntry
}

// ImportRelayResult summarizes a successful import.
type ImportRelayResult struct {
	RelayID            int64
	Slug               string
	ChannelsCreated    int
	ChannelsReused     int
	MembershipsCreated int
	GroupsCreated      int
	EPGImported        int
	EPGSourceIDs       []int64
	UpdatedIDs         []string
}

// ImportChannelEntry is one preclassified TXT row ready for persistence.
type ImportChannelEntry struct {
	Name string
	URL  string
}

// ImportChannelsResult summarizes a successful channel-list import.
type ImportChannelsResult struct {
	Created        int
	Reused         int
	UpstreamsAdded int
	UpdatedIDs     []string
	Warnings       []string
}

// ImportRelay creates a relay and related records in one transaction.
// Callers must preclassify deterministic skips (non-HTTP URLs, duplicate upstreams).
func (s *Store) ImportRelay(ctx context.Context, in ImportRelayInput) (ImportRelayResult, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "Imported Relay"
	}
	slug := normalizeSlug(in.Slug)
	if slug == "" {
		slug = normalizeSlug(name)
	}
	if err := validateSlug(slug); err != nil {
		return ImportRelayResult{}, err
	}
	if in.DefaultEPGInterval <= 0 {
		in.DefaultEPGInterval = time.Hour
	}

	var result ImportRelayResult
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		res, err := tx.ExecContext(ctx, `
INSERT INTO relays (name, slug, created_at, updated_at) VALUES (?, ?, ?, ?)`,
			name, slug, now, now)
		if err != nil {
			if isUniqueErr(err) {
				return fmt.Errorf("%w: slug already exists", ErrConflict)
			}
			return err
		}
		relayID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		result.RelayID = relayID
		result.Slug = slug

		epgIDs := make([]int64, 0, len(in.EPGURLs))
		for i, epgURL := range in.EPGURLs {
			epgURL = strings.TrimSpace(epgURL)
			if epgURL == "" {
				continue
			}
			src, err := findEPGSourceByURLTx(ctx, tx, epgURL)
			if errors.Is(err, ErrNotFound) {
				src, err = createEPGSourceTx(ctx, tx, EPGSourceInput{
					Name:            fmt.Sprintf("Imported EPG %d", i+1),
					URL:             epgURL,
					RefreshInterval: in.DefaultEPGInterval.String(),
				}, in.DefaultEPGInterval)
				if err != nil {
					return err
				}
				result.EPGImported++
			} else if err != nil {
				return err
			}
			epgIDs = append(epgIDs, src.ID)
		}
		result.EPGSourceIDs = epgIDs

		type groupState struct {
			id  int64
			ord int
		}
		groupMap := map[string]*groupState{}
		ensureGroup := func(title string) (*groupState, error) {
			if title == "" {
				title = "Channels"
			}
			if g, ok := groupMap[title]; ok {
				return g, nil
			}
			var maxOrd sql.NullInt64
			_ = tx.QueryRowContext(ctx, `SELECT MAX(sort_order) FROM relay_groups WHERE relay_id = ?`, relayID).Scan(&maxOrd)
			ord := 0
			if maxOrd.Valid {
				ord = int(maxOrd.Int64) + 1
			}
			gres, err := tx.ExecContext(ctx, `
INSERT INTO relay_groups (relay_id, name, sort_order) VALUES (?, ?, ?)`, relayID, title, ord)
			if err != nil {
				return nil, err
			}
			gid, err := gres.LastInsertId()
			if err != nil {
				return nil, err
			}
			result.GroupsCreated++
			g := &groupState{id: gid}
			groupMap[title] = g
			return g, nil
		}

		takenNames, err := loadChannelNameKeysTx(ctx, tx)
		if err != nil {
			return err
		}
		updated := map[string]struct{}{}

		for _, ent := range in.Entries {
			srcID, tvg := resolveImportChannelEPG(ent, epgIDs)
			ch, err := findChannelByUpstreamURLTx(ctx, tx, ent.UpstreamURL)
			if errors.Is(err, ErrNotFound) {
				name := nextUniqueChannelName(takenNames, ent.Name)
				chIn := ChannelInput{
					Name:        name,
					LogoURL:     ent.LogoURL,
					UpstreamURL: ent.UpstreamURL,
					Headers:     ent.Headers,
				}
				if srcID != nil && tvg != "" {
					t := tvg
					chIn.EPGSourceID = srcID
					chIn.TvgID = &t
				}
				ch, err = createChannelTx(ctx, tx, chIn)
				if err != nil {
					return err
				}
				result.ChannelsCreated++
			} else if err != nil {
				return err
			} else {
				result.ChannelsReused++
				if !HasMapping(ch.EPGSourceID, ch.TvgID) && srcID != nil && tvg != "" {
					if err := fillChannelEPGTx(ctx, tx, ch.ID, *srcID, tvg); err != nil {
						return err
					}
					updated[ch.ID] = struct{}{}
				}
			}

			g, err := ensureGroup(ent.GroupTitle)
			if err != nil {
				return err
			}
			ord := g.ord
			if _, err := addMembershipTx(ctx, tx, relayID, MembershipInput{
				ChannelID: ch.ID,
				GroupID:   g.id,
				SortOrder: &ord,
			}); err != nil {
				return err
			}
			g.ord++
			result.MembershipsCreated++
		}
		for id := range updated {
			result.UpdatedIDs = append(result.UpdatedIDs, id)
		}
		sort.Strings(result.UpdatedIDs)

		if result.GroupsCreated == 0 {
			if _, err := ensureGroup("Channels"); err != nil {
				return err
			}
		}
		return nil
	})
	return result, err
}

// resolveImportChannelEPG chooses a complete channel EPG pair for an import entry.
//
// Requires both playlist EPG URL(s) and a per-entry tvg-id. Then:
//  1. Prefer an explicit source already selected for this entry (multi-url-tvg match)
//     when it belongs to this playlist's EPG set.
//  2. If the playlist has exactly one EPG source, bind that source.
//  3. Otherwise leave unset (both empty — never a half-pair).
func resolveImportChannelEPG(ent ImportRelayEntry, epgIDs []int64) (*int64, string) {
	tvg := strings.TrimSpace(ent.TvgID)
	if tvg == "" || len(epgIDs) == 0 {
		return nil, ""
	}
	if ent.EPGSourceID != nil && utils.ContainsInt64(epgIDs, *ent.EPGSourceID) {
		return ent.EPGSourceID, tvg
	}
	if len(epgIDs) == 1 {
		id := epgIDs[0]
		return &id, tvg
	}
	return nil, ""
}

func fillChannelEPGTx(ctx context.Context, q querier, channelID string, sourceID int64, tvg string) error {
	_, err := q.ExecContext(ctx, `
UPDATE channels SET epg_source_id = ?, tvg_id = ?, updated_at = ? WHERE id = ?`,
		sourceID, tvg, time.Now().UTC().Format(time.RFC3339Nano), channelID)
	return err
}

// ImportChannels creates Channels or appends upstreams from a TXT list in one transaction.
func (s *Store) ImportChannels(ctx context.Context, entries []ImportChannelEntry) (ImportChannelsResult, error) {
	var result ImportChannelsResult
	updated := map[string]struct{}{}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		for _, ent := range entries {
			name := strings.TrimSpace(ent.Name)
			rawURL := strings.TrimSpace(ent.URL)
			owner, err := findChannelByUpstreamURLTx(ctx, tx, rawURL)
			if err == nil {
				if channelNameKey(owner.Name) == channelNameKey(name) {
					result.Reused++
				} else {
					result.Warnings = append(result.Warnings, fmt.Sprintf("url already used by %s", owner.Name))
				}
				continue
			}
			if !errors.Is(err, ErrNotFound) {
				return err
			}
			existing, err := findChannelByNameTx(ctx, tx, name)
			if err == nil {
				added, err := appendChannelUpstreamTx(ctx, tx, existing.ID, rawURL)
				if err != nil {
					return err
				}
				if added {
					result.UpstreamsAdded++
					updated[existing.ID] = struct{}{}
				} else {
					result.Reused++
				}
				continue
			}
			if !errors.Is(err, ErrNotFound) {
				return err
			}
			if _, err := createChannelTx(ctx, tx, ChannelInput{Name: name, UpstreamURL: rawURL}); err != nil {
				return err
			}
			result.Created++
		}
		return nil
	})
	if err != nil {
		return ImportChannelsResult{}, err
	}
	for id := range updated {
		result.UpdatedIDs = append(result.UpdatedIDs, id)
	}
	sort.Strings(result.UpdatedIDs)
	return result, nil
}

func findChannelByNameTx(ctx context.Context, q querier, name string) (Channel, error) {
	row := q.QueryRowContext(ctx, `SELECT `+channelSelect+` FROM channels c WHERE name = ? LIMIT 1`, strings.TrimSpace(name))
	ch, err := scanChannel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Channel{}, ErrNotFound
	}
	return ch, err
}

func appendChannelUpstreamTx(ctx context.Context, q querier, channelID, rawURL string) (bool, error) {
	item, err := normalizeChannelUpstream(ChannelUpstream{URL: rawURL}, map[string]struct{}{})
	if err != nil {
		return false, err
	}
	channels := []Channel{{ID: channelID}}
	if err := attachChannelUpstreams(ctx, q, channels); err != nil {
		return false, err
	}
	for _, u := range channels[0].Upstreams {
		for _, ru := range resolvedForDedupe(u) {
			if ru == item.URL {
				return false, nil
			}
		}
	}
	var next int
	if err := q.QueryRowContext(ctx, `
SELECT COALESCE(MAX(sort_order)+1, 0) FROM channel_upstreams WHERE channel_id = ?`, channelID).Scan(&next); err != nil {
		return false, err
	}
	headersJSON, err := json.Marshal(normalizeHeaders(item.Headers))
	if err != nil {
		return false, err
	}
	if _, err := q.ExecContext(ctx, `
INSERT INTO channel_upstreams (id, channel_id, url, headers_json, sort_order, proxy_id)
VALUES (?, ?, ?, ?, ?, NULL)`,
		item.ID, channelID, item.URL, string(headersJSON), next); err != nil {
		return false, channelConflictErr(err)
	}
	_, err = q.ExecContext(ctx, `UPDATE channels SET updated_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), channelID)
	return true, err
}

func findChannelByUpstreamURLTx(ctx context.Context, q querier, rawURL string) (Channel, error) {
	rawURL = strings.TrimSpace(rawURL)
	row := q.QueryRowContext(ctx, `SELECT `+channelSelect+`
FROM channels c
WHERE c.upstream_url = ?
   OR EXISTS (SELECT 1 FROM channel_upstreams u WHERE u.channel_id = c.id AND u.url = ? AND u.proxy_id IS NULL)
ORDER BY CASE WHEN c.upstream_url = ? THEN 0 ELSE 1 END, c.id
LIMIT 1`,
		rawURL, rawURL, rawURL)
	ch, err := scanChannel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Channel{}, ErrNotFound
	}
	return ch, err
}

func createChannelTx(ctx context.Context, q querier, in ChannelInput) (Channel, error) {
	if err := validateChannelInput(in); err != nil {
		return Channel{}, err
	}
	spec, err := resolveChannelSpec(ctx, q, in, nil)
	if err != nil {
		return Channel{}, err
	}
	now := time.Now().UTC()
	headersJSON, err := json.Marshal(normalizeHeaders(in.Headers))
	if err != nil {
		return Channel{}, err
	}
	transcode := resolveTranscodeEnabled(in.TranscodeEnabled, false)
	epgID, tvgID, err := resolveChannelEPG(in, nil)
	if err != nil {
		return Channel{}, err
	}
	if err := ensureEPGSourceExists(ctx, q, epgID); err != nil {
		return Channel{}, err
	}
	id := uuid.NewString()
	_, err = q.ExecContext(ctx, `
INSERT INTO channels (id, name, logo_url, upstream_url, headers_json, transcode_enabled, upstream_policy, fixed_upstream_id, epg_source_id, tvg_id, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id,
		strings.TrimSpace(in.Name),
		strings.TrimSpace(in.LogoURL),
		spec.primaryURL,
		string(headersJSON),
		boolToInt(transcode),
		spec.policy,
		spec.fixedID,
		nullableInt64(epgID),
		tvgID,
		now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return Channel{}, channelConflictErr(err)
	}
	if err := insertChannelUpstreamsTx(ctx, q, id, spec); err != nil {
		return Channel{}, err
	}
	row := q.QueryRowContext(ctx, `SELECT `+channelSelect+` FROM channels c WHERE c.id = ?`, id)
	return scanChannel(row)
}

func loadChannelNameKeysTx(ctx context.Context, q querier) (map[string]struct{}, error) {
	rows, err := q.QueryContext(ctx, `SELECT name FROM channels`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[channelNameKey(name)] = struct{}{}
	}
	return out, rows.Err()
}

// nextUniqueChannelName returns base, or "base (2)", "base (3)", … and records it in taken.
// Keys use ASCII lower-case to match SQLite NOCASE on channel names.
func nextUniqueChannelName(taken map[string]struct{}, base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "Channel"
	}
	for n := 1; ; n++ {
		candidate := base
		if n > 1 {
			candidate = fmt.Sprintf("%s (%d)", base, n)
		}
		key := channelNameKey(candidate)
		if _, ok := taken[key]; ok {
			continue
		}
		taken[key] = struct{}{}
		return candidate
	}
}

func channelNameKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func findEPGSourceByURLTx(ctx context.Context, q querier, rawURL string) (EPGSource, error) {
	row := q.QueryRowContext(ctx, `
SELECT id, name, url, enabled, refresh_interval_seconds, last_refresh_at, last_error, created_at, updated_at
FROM epg_sources WHERE url = ? LIMIT 1`, strings.TrimSpace(rawURL))
	src, err := scanEPGSource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return EPGSource{}, ErrNotFound
	}
	return src, err
}

func createEPGSourceTx(ctx context.Context, q querier, in EPGSourceInput, defaultInterval time.Duration) (EPGSource, error) {
	interval, err := parseRefreshInterval(in.RefreshInterval, defaultInterval)
	if err != nil {
		return EPGSource{}, err
	}
	if err := validateEPGSourceInput(in); err != nil {
		return EPGSource{}, err
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	now := time.Now().UTC()
	res, err := q.ExecContext(ctx, `
INSERT INTO epg_sources (name, url, enabled, refresh_interval_seconds, last_refresh_at, last_error, created_at, updated_at)
VALUES (?, ?, ?, ?, NULL, '', ?, ?)`,
		strings.TrimSpace(in.Name),
		strings.TrimSpace(in.URL),
		boolToInt(enabled),
		int(interval.Seconds()),
		now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return EPGSource{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return EPGSource{}, err
	}
	row := q.QueryRowContext(ctx, `
SELECT id, name, url, enabled, refresh_interval_seconds, last_refresh_at, last_error, created_at, updated_at
FROM epg_sources WHERE id = ?`, id)
	return scanEPGSource(row)
}
