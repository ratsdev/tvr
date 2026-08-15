package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// LibraryBackupVersion is the JSON schema written by ExportLibrary.
const LibraryBackupVersion = 1

const defaultBackupEPGIntervalSeconds = 3600

// LibraryBackup is a portable snapshot of the library (not system settings).
type LibraryBackup struct {
	Version    int               `json:"version"`
	ExportedAt time.Time         `json:"exported_at"`
	Proxies    []BackupProxy     `json:"proxies"`
	EPGSources []BackupEPGSource `json:"epg_sources"`
	Channels   []BackupChannel   `json:"channels"`
	Relays     []BackupRelay     `json:"relays"`
}

// BackupProxy is one proxy in a library backup.
type BackupProxy struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	Policy        string              `json:"policy,omitempty"`
	FixedServerID string              `json:"fixed_server_id,omitempty"`
	Servers       []BackupProxyServer `json:"servers"`
}

// BackupProxyServer is one HTTP prefix on a backed-up proxy.
type BackupProxyServer struct {
	ID  string `json:"id,omitempty"`
	URL string `json:"url"`
}

// BackupEPGSource is one EPG feed in a library backup.
type BackupEPGSource struct {
	ID                     int64  `json:"id"`
	Name                   string `json:"name"`
	URL                    string `json:"url"`
	Enabled                bool   `json:"enabled"`
	RefreshIntervalSeconds int    `json:"refresh_interval_seconds"`
}

// BackupChannel is the channel subset stored in a library backup.
type BackupChannel struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	LogoURL          string            `json:"logo_url,omitempty"`
	Upstreams        []BackupUpstream  `json:"upstreams"`
	UpstreamPolicy   string            `json:"upstream_policy,omitempty"`
	FixedUpstreamID  string            `json:"fixed_upstream_id,omitempty"`
	Headers          map[string]string `json:"headers,omitempty"`
	TranscodeEnabled bool              `json:"transcode_enabled,omitempty"`
	EPGSourceID      *int64            `json:"epg_source_id,omitempty"`
	TvgID            string            `json:"tvg_id,omitempty"`
}

// BackupUpstream is one channel upstream, with a proxy name for rematch.
type BackupUpstream struct {
	ID        string            `json:"id,omitempty"`
	URL       string            `json:"url"`
	ProxyID   string            `json:"proxy_id,omitempty"`
	ProxyName string            `json:"proxy_name,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

// BackupRelay is one relay lineup in a library backup.
type BackupRelay struct {
	ID          int64              `json:"id"`
	Name        string             `json:"name"`
	Slug        string             `json:"slug"`
	Groups      []BackupGroup      `json:"groups"`
	Memberships []BackupMembership `json:"memberships"`
}

// BackupGroup is an ordered group inside a backed-up relay.
type BackupGroup struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	SortOrder int    `json:"sort_order"`
}

// BackupMembership binds a channel into a backed-up relay group.
type BackupMembership struct {
	ID        int64  `json:"id"`
	GroupID   int64  `json:"group_id"`
	ChannelID string `json:"channel_id"`
	Number    int    `json:"number"`
	SortOrder int    `json:"sort_order"`
}

// RestoredRelay is one relay written by RestoreLibrary.
type RestoredRelay struct {
	ID   int64
	Slug string
}

// LibraryRestoreResult is the outcome of RestoreLibrary.
type LibraryRestoreResult struct {
	Proxies             int             `json:"proxies"`
	EPGSources          int             `json:"epg_sources"`
	Channels            int             `json:"channels"`
	Relays              int             `json:"relays"`
	ChannelIDs          []string        `json:"-"`
	RemovedChannelIDs   []string        `json:"-"`
	RestoredRelays      []RestoredRelay `json:"-"`
	RemovedRelays       []RestoredRelay `json:"-"`
	OldEPGSourceIDs     []int64         `json:"-"`
	RefreshEPGSourceIDs []int64         `json:"-"`
}

// ExportLibrary snapshots proxies, EPG sources, channels, and relays.
func (s *Store) ExportLibrary(ctx context.Context) (LibraryBackup, error) {
	proxies, err := s.ListProxies(ctx)
	if err != nil {
		return LibraryBackup{}, err
	}
	epgSources, err := s.ListEPGSources(ctx)
	if err != nil {
		return LibraryBackup{}, err
	}
	channels, err := s.ListChannels(ctx)
	if err != nil {
		return LibraryBackup{}, err
	}
	relays, err := s.ListRelays(ctx)
	if err != nil {
		return LibraryBackup{}, err
	}

	nameByID := map[string]string{}
	out := LibraryBackup{
		Version:    LibraryBackupVersion,
		ExportedAt: time.Now().UTC(),
		Proxies:    make([]BackupProxy, 0, len(proxies)),
		EPGSources: make([]BackupEPGSource, 0, len(epgSources)),
		Channels:   make([]BackupChannel, 0, len(channels)),
		Relays:     make([]BackupRelay, 0, len(relays)),
	}
	for _, p := range proxies {
		nameByID[p.ID] = p.Name
		item := BackupProxy{
			ID:            p.ID,
			Name:          p.Name,
			Policy:        p.Policy,
			FixedServerID: p.FixedServerID,
			Servers:       make([]BackupProxyServer, 0, len(p.Servers)),
		}
		for _, srv := range p.Servers {
			item.Servers = append(item.Servers, BackupProxyServer{ID: srv.ID, URL: srv.URL})
		}
		out.Proxies = append(out.Proxies, item)
	}
	for _, src := range epgSources {
		out.EPGSources = append(out.EPGSources, BackupEPGSource{
			ID:                     src.ID,
			Name:                   src.Name,
			URL:                    src.URL,
			Enabled:                src.Enabled,
			RefreshIntervalSeconds: int(src.RefreshInterval / time.Second),
		})
	}
	for _, ch := range channels {
		item := BackupChannel{
			ID:               ch.ID,
			Name:             ch.Name,
			LogoURL:          ch.LogoURL,
			Upstreams:        make([]BackupUpstream, 0, len(ch.Upstreams)),
			UpstreamPolicy:   ch.UpstreamPolicy,
			FixedUpstreamID:  ch.FixedUpstreamID,
			Headers:          ch.Headers,
			TranscodeEnabled: ch.TranscodeEnabled,
			EPGSourceID:      ch.EPGSourceID,
			TvgID:            ch.TvgID,
		}
		for _, u := range ch.Upstreams {
			item.Upstreams = append(item.Upstreams, BackupUpstream{
				ID:        u.ID,
				URL:       u.URL,
				ProxyID:   u.ProxyID,
				ProxyName: nameByID[u.ProxyID],
				Headers:   u.Headers,
			})
		}
		out.Channels = append(out.Channels, item)
	}
	for _, r := range relays {
		detail, err := s.GetRelayDetail(ctx, r.ID)
		if err != nil {
			return LibraryBackup{}, err
		}
		item := BackupRelay{
			ID:          detail.ID,
			Name:        detail.Name,
			Slug:        detail.Slug,
			Groups:      make([]BackupGroup, 0, len(detail.Groups)),
			Memberships: make([]BackupMembership, 0, len(detail.Memberships)),
		}
		for _, g := range detail.Groups {
			item.Groups = append(item.Groups, BackupGroup{ID: g.ID, Name: g.Name, SortOrder: g.SortOrder})
		}
		for _, m := range detail.Memberships {
			item.Memberships = append(item.Memberships, BackupMembership{
				ID:        m.ID,
				GroupID:   m.GroupID,
				ChannelID: m.ChannelID,
				Number:    m.Number,
				SortOrder: m.SortOrder,
			})
		}
		out.Relays = append(out.Relays, item)
	}
	return out, nil
}

// RestoreLibrary replaces proxies, EPG sources, channels, and relays with the backup.
func (s *Store) RestoreLibrary(ctx context.Context, in LibraryBackup) (LibraryRestoreResult, error) {
	if in.Version != LibraryBackupVersion {
		return LibraryRestoreResult{}, fmt.Errorf("%w: unsupported backup version", ErrValidation)
	}
	var result LibraryRestoreResult
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		oldChannelIDs, err := listChannelIDs(ctx, tx)
		if err != nil {
			return err
		}
		oldRelays, err := listRelayIDSlugs(ctx, tx)
		if err != nil {
			return err
		}
		oldEPGIDs, err := listEPGSourceIDs(ctx, tx)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM relays`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM channels`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM proxies`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM epg_sources`); err != nil {
			return err
		}

		byID := map[string]bool{}
		byName := map[string]string{}
		for i, p := range in.Proxies {
			id, name, err := restoreProxyTx(ctx, tx, p)
			if err != nil {
				return fmt.Errorf("proxy %d %q: %w", i+1, p.Name, err)
			}
			byID[id] = true
			byName[strings.ToLower(name)] = id
			result.Proxies++
		}
		var maxEPG int64
		for i, src := range in.EPGSources {
			if err := restoreEPGSourceTx(ctx, tx, src); err != nil {
				return fmt.Errorf("epg source %d %q: %w", i+1, src.Name, err)
			}
			maxEPG = max(maxEPG, src.ID)
			if src.Enabled {
				result.RefreshEPGSourceIDs = append(result.RefreshEPGSourceIDs, src.ID)
			}
			result.EPGSources++
		}
		result.ChannelIDs = make([]string, 0, len(in.Channels))
		for i, ch := range in.Channels {
			id, err := restoreChannelTx(ctx, tx, ch, byID, byName)
			if err != nil {
				return fmt.Errorf("channel %d %q: %w", i+1, ch.Name, err)
			}
			result.ChannelIDs = append(result.ChannelIDs, id)
			result.Channels++
		}
		var maxRelay, maxGroup, maxMem int64
		result.RestoredRelays = make([]RestoredRelay, 0, len(in.Relays))
		for i, r := range in.Relays {
			restored, groupMax, memMax, err := restoreRelayTx(ctx, tx, r)
			if err != nil {
				return fmt.Errorf("relay %d %q: %w", i+1, r.Name, err)
			}
			result.RestoredRelays = append(result.RestoredRelays, restored)
			maxRelay = max(maxRelay, restored.ID)
			maxGroup = max(maxGroup, groupMax)
			maxMem = max(maxMem, memMax)
			result.Relays++
		}

		result.RemovedChannelIDs = exceptStrings(oldChannelIDs, result.ChannelIDs)
		result.RemovedRelays = exceptRelaysBySlug(oldRelays, result.RestoredRelays)
		result.OldEPGSourceIDs = oldEPGIDs

		if err := resetAutoID(ctx, tx, "epg_sources", maxEPG); err != nil {
			return err
		}
		if err := resetAutoID(ctx, tx, "relays", maxRelay); err != nil {
			return err
		}
		if err := resetAutoID(ctx, tx, "relay_groups", maxGroup); err != nil {
			return err
		}
		return resetAutoID(ctx, tx, "relay_memberships", maxMem)
	})
	if err != nil {
		return LibraryRestoreResult{}, err
	}
	return result, nil
}

func restoreProxyTx(ctx context.Context, q querier, in BackupProxy) (id, name string, err error) {
	id = strings.TrimSpace(in.ID)
	if _, err := uuid.Parse(id); err != nil {
		return "", "", fmt.Errorf("%w: proxy id is required", ErrValidation)
	}
	servers := make([]ProxyServer, 0, len(in.Servers))
	for _, srv := range in.Servers {
		servers = append(servers, ProxyServer{ID: srv.ID, URL: srv.URL})
	}
	spec, err := resolveProxySpec(ProxyInput{
		Name:          in.Name,
		Policy:        in.Policy,
		FixedServerID: in.FixedServerID,
		Servers:       servers,
	}, nil)
	if err != nil {
		return "", "", err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = q.ExecContext(ctx, `
INSERT INTO proxies (id, name, policy, fixed_server_id, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		id, spec.name, spec.policy, nullIfEmpty(spec.fixedID), now, now)
	if err != nil {
		return "", "", proxyConflictErr(err)
	}
	if err := insertProxyServersTx(ctx, q, id, spec); err != nil {
		return "", "", err
	}
	return id, spec.name, nil
}

func restoreEPGSourceTx(ctx context.Context, q querier, in BackupEPGSource) error {
	if in.ID <= 0 {
		return fmt.Errorf("%w: epg source id is required", ErrValidation)
	}
	seconds := in.RefreshIntervalSeconds
	if seconds <= 0 {
		seconds = defaultBackupEPGIntervalSeconds
	}
	if seconds < 60 {
		return fmt.Errorf("%w: refresh_interval must be >= 1m", ErrValidation)
	}
	if err := validateEPGSourceInput(EPGSourceInput{Name: in.Name, URL: in.URL}); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := q.ExecContext(ctx, `
INSERT INTO epg_sources (id, name, url, enabled, refresh_interval_seconds, last_refresh_at, last_error, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, NULL, '', ?, ?)`,
		in.ID,
		strings.TrimSpace(in.Name),
		strings.TrimSpace(in.URL),
		boolToInt(in.Enabled),
		seconds,
		now,
		now,
	)
	if isUniqueErr(err) {
		return fmt.Errorf("%w: epg source already exists", ErrConflict)
	}
	return err
}

func restoreChannelTx(ctx context.Context, q querier, in BackupChannel, proxyByID map[string]bool, proxyByName map[string]string) (string, error) {
	id := strings.TrimSpace(in.ID)
	if _, err := uuid.Parse(id); err != nil {
		return "", fmt.Errorf("%w: channel id is required", ErrValidation)
	}
	upstreams, err := rematchBackupUpstreams(in.Upstreams, proxyByID, proxyByName)
	if err != nil {
		return "", err
	}
	epgID, tvgID := in.EPGSourceID, strings.TrimSpace(in.TvgID)
	if epgID != nil {
		if err := ensureEPGSourceExists(ctx, q, epgID); err != nil {
			if !errors.Is(err, ErrValidation) {
				return "", err
			}
			epgID, tvgID = nil, ""
		}
	} else {
		tvgID = ""
	}
	enabled := in.TranscodeEnabled
	_, err = insertChannelTx(ctx, q, id, ChannelInput{
		Name:             in.Name,
		LogoURL:          in.LogoURL,
		Upstreams:        upstreams,
		UpstreamPolicy:   in.UpstreamPolicy,
		FixedUpstreamID:  in.FixedUpstreamID,
		Headers:          in.Headers,
		TranscodeEnabled: &enabled,
		EPGSourceID:      epgID,
		TvgID:            &tvgID,
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

func rematchBackupUpstreams(in []BackupUpstream, byID map[string]bool, byName map[string]string) ([]ChannelUpstream, error) {
	out := make([]ChannelUpstream, 0, len(in))
	for _, u := range in {
		proxyID := strings.TrimSpace(u.ProxyID)
		if proxyID != "" && byID[proxyID] {
			out = append(out, ChannelUpstream{ID: u.ID, URL: u.URL, ProxyID: proxyID, Headers: u.Headers})
			continue
		}
		if name := strings.TrimSpace(u.ProxyName); name != "" {
			if id, ok := byName[strings.ToLower(name)]; ok {
				out = append(out, ChannelUpstream{ID: u.ID, URL: u.URL, ProxyID: id, Headers: u.Headers})
				continue
			}
		}
		if (proxyID != "" || strings.TrimSpace(u.ProxyName) != "") && !isHTTPURL(u.URL) {
			label := strings.TrimSpace(u.ProxyName)
			if label == "" {
				label = proxyID
			}
			return nil, fmt.Errorf("%w: unknown proxy %s", ErrValidation, label)
		}
		out = append(out, ChannelUpstream{ID: u.ID, URL: u.URL, Headers: u.Headers})
	}
	return out, nil
}

func isHTTPURL(raw string) bool {
	s := strings.ToLower(strings.TrimSpace(raw))
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func listChannelIDs(ctx context.Context, q querier) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM channels`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func listRelayIDSlugs(ctx context.Context, q querier) ([]RestoredRelay, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, slug FROM relays`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RestoredRelay
	for rows.Next() {
		var r RestoredRelay
		if err := rows.Scan(&r.ID, &r.Slug); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func listEPGSourceIDs(ctx context.Context, q querier) ([]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM epg_sources`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func exceptStrings(old, keep []string) []string {
	set := make(map[string]struct{}, len(keep))
	for _, id := range keep {
		set[id] = struct{}{}
	}
	var out []string
	for _, id := range old {
		if _, ok := set[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}

func exceptRelaysBySlug(old, keep []RestoredRelay) []RestoredRelay {
	set := make(map[string]struct{}, len(keep))
	for _, r := range keep {
		set[r.Slug] = struct{}{}
	}
	var out []RestoredRelay
	for _, r := range old {
		if _, ok := set[r.Slug]; !ok {
			out = append(out, r)
		}
	}
	return out
}

func restoreRelayTx(ctx context.Context, q querier, in BackupRelay) (out RestoredRelay, groupMax, memMax int64, err error) {
	name := strings.TrimSpace(in.Name)
	slug := normalizeSlug(in.Slug)
	if in.ID <= 0 {
		return RestoredRelay{}, 0, 0, fmt.Errorf("%w: relay id is required", ErrValidation)
	}
	if name == "" {
		return RestoredRelay{}, 0, 0, fmt.Errorf("%w: name is required", ErrValidation)
	}
	if slug == "" {
		slug = normalizeSlug(name)
	}
	if err := validateSlug(slug); err != nil {
		return RestoredRelay{}, 0, 0, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := q.ExecContext(ctx, `
INSERT INTO relays (id, name, slug, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		in.ID, name, slug, now, now); err != nil {
		if isUniqueErr(err) {
			return RestoredRelay{}, 0, 0, fmt.Errorf("%w: slug already exists", ErrConflict)
		}
		return RestoredRelay{}, 0, 0, err
	}
	groupIDs := map[int64]struct{}{}
	for _, g := range in.Groups {
		gname := strings.TrimSpace(g.Name)
		if g.ID <= 0 {
			return RestoredRelay{}, 0, 0, fmt.Errorf("%w: group id is required", ErrValidation)
		}
		if gname == "" {
			return RestoredRelay{}, 0, 0, fmt.Errorf("%w: group name is required", ErrValidation)
		}
		if _, err := q.ExecContext(ctx, `
INSERT INTO relay_groups (id, relay_id, name, sort_order) VALUES (?, ?, ?, ?)`,
			g.ID, in.ID, gname, g.SortOrder); err != nil {
			return RestoredRelay{}, 0, 0, err
		}
		groupIDs[g.ID] = struct{}{}
		groupMax = max(groupMax, g.ID)
	}
	for _, m := range in.Memberships {
		if m.ID <= 0 {
			return RestoredRelay{}, 0, 0, fmt.Errorf("%w: membership id is required", ErrValidation)
		}
		if strings.TrimSpace(m.ChannelID) == "" {
			return RestoredRelay{}, 0, 0, fmt.Errorf("%w: channel_id is required", ErrValidation)
		}
		if _, ok := groupIDs[m.GroupID]; !ok {
			return RestoredRelay{}, 0, 0, fmt.Errorf("%w: group not found", ErrValidation)
		}
		var exists int
		if err := q.QueryRowContext(ctx, `SELECT 1 FROM channels WHERE id = ?`, m.ChannelID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return RestoredRelay{}, 0, 0, fmt.Errorf("%w: channel not found", ErrValidation)
			}
			return RestoredRelay{}, 0, 0, err
		}
		if _, err := q.ExecContext(ctx, `
INSERT INTO relay_memberships (id, relay_id, group_id, channel_id, number, sort_order)
VALUES (?, ?, ?, ?, ?, ?)`,
			m.ID, in.ID, m.GroupID, m.ChannelID, m.Number, m.SortOrder); err != nil {
			if isUniqueErr(err) {
				return RestoredRelay{}, 0, 0, fmt.Errorf("%w: channel already in this relay", ErrConflict)
			}
			return RestoredRelay{}, 0, 0, err
		}
		memMax = max(memMax, m.ID)
	}
	return RestoredRelay{ID: in.ID, Slug: slug}, groupMax, memMax, nil
}

func resetAutoID(ctx context.Context, tx *sql.Tx, table string, seq int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM sqlite_sequence WHERE name = ?`, table); err != nil {
		return err
	}
	if seq <= 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO sqlite_sequence(name, seq) VALUES (?, ?)`, table, seq)
	return err
}
