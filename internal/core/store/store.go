package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"github.com/ratsdev/tvr/internal/utils"
)

var (
	// ErrNotFound indicates a missing entity.
	ErrNotFound = errors.New("not found")
	// ErrValidation indicates invalid user input.
	ErrValidation = errors.New("validation error")
	// ErrConflict indicates a uniqueness or reference conflict.
	ErrConflict = errors.New("conflict")
)

var slugRE = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Store wraps the SQLite database used by tvr.
type Store struct {
	db *sql.DB
}

// Open creates or opens the SQLite database and ensures the schema exists.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)

	s := &Store{db: db}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.ensureSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) ensureSchema() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS epg_sources (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  url TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  refresh_interval_seconds INTEGER NOT NULL DEFAULT 3600,
  last_refresh_at TEXT,
  last_error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_epg_sources_enabled ON epg_sources(enabled);

CREATE TABLE IF NOT EXISTS channels (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL COLLATE NOCASE,
  logo_url TEXT NOT NULL DEFAULT '',
  upstream_url TEXT NOT NULL UNIQUE,
  headers_json TEXT NOT NULL DEFAULT '{}',
  transcode_enabled INTEGER NOT NULL DEFAULT 0,
  upstream_policy TEXT NOT NULL DEFAULT 'fixed',
  fixed_upstream_id TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS channel_upstreams (
  id TEXT PRIMARY KEY,
  channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  url TEXT NOT NULL UNIQUE,
  headers_json TEXT NOT NULL DEFAULT '{}',
  sort_order INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_channel_upstreams_channel ON channel_upstreams(channel_id, sort_order);

CREATE TABLE IF NOT EXISTS app_settings (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  video_crf INTEGER NOT NULL DEFAULT 23,
  video_preset TEXT NOT NULL DEFAULT 'veryfast',
  audio_bitrate_kbps INTEGER NOT NULL DEFAULT 128,
  max_height INTEGER NOT NULL DEFAULT 0,
  startup_timeout_seconds INTEGER NOT NULL DEFAULT 30,
  brand_icon TEXT NOT NULL DEFAULT '',
  brand_title TEXT NOT NULL DEFAULT 'IPTV Relay'
);

CREATE TABLE IF NOT EXISTS relays (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  slug TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS relay_groups (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  relay_id INTEGER NOT NULL REFERENCES relays(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  sort_order INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS relay_memberships (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  relay_id INTEGER NOT NULL REFERENCES relays(id) ON DELETE CASCADE,
  group_id INTEGER NOT NULL REFERENCES relay_groups(id) ON DELETE CASCADE,
  channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE RESTRICT,
  number INTEGER NOT NULL DEFAULT 0,
  epg_source_id INTEGER REFERENCES epg_sources(id) ON DELETE RESTRICT,
  tvg_id TEXT NOT NULL DEFAULT '',
  sort_order INTEGER NOT NULL DEFAULT 0,
  UNIQUE(relay_id, channel_id)
);

CREATE INDEX IF NOT EXISTS idx_relay_groups_relay ON relay_groups(relay_id, sort_order);
CREATE INDEX IF NOT EXISTS idx_relay_memberships_group ON relay_memberships(group_id, sort_order);
CREATE INDEX IF NOT EXISTS idx_relay_memberships_channel ON relay_memberships(channel_id);
`)
	if err != nil {
		return err
	}
	if err := s.ensureChannelTranscodeColumn(); err != nil {
		return err
	}
	if err := s.ensureChannelUpstreams(); err != nil {
		return err
	}
	if err := s.ensureBrandSettingsColumns(); err != nil {
		return err
	}
	if err := s.ensureAppSettingsRow(); err != nil {
		return err
	}
	if err := s.ensureNoRelayEPGSources(); err != nil {
		return err
	}
	return s.ensureChannelNameUniqueIndex()
}

func (s *Store) ensureNoRelayEPGSources() error {
	_, err := s.db.Exec(`DROP TABLE IF EXISTS relay_epg_sources`)
	return err
}

func (s *Store) ensureChannelUpstreams() error {
	hasPolicy, err := hasChannelColumn(s.db, "upstream_policy")
	if err != nil {
		return err
	}
	if !hasPolicy {
		if _, err := s.db.Exec(`ALTER TABLE channels ADD COLUMN upstream_policy TEXT NOT NULL DEFAULT 'fixed'`); err != nil {
			return err
		}
	}
	hasFixed, err := hasChannelColumn(s.db, "fixed_upstream_id")
	if err != nil {
		return err
	}
	if !hasFixed {
		if _, err := s.db.Exec(`ALTER TABLE channels ADD COLUMN fixed_upstream_id TEXT`); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS channel_upstreams (
  id TEXT PRIMARY KEY,
  channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  url TEXT NOT NULL UNIQUE,
  headers_json TEXT NOT NULL DEFAULT '{}',
  sort_order INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_channel_upstreams_channel ON channel_upstreams(channel_id, sort_order);
`); err != nil {
		return err
	}
	return s.backfillChannelUpstreams()
}

func (s *Store) backfillChannelUpstreams() error {
	rows, err := s.db.Query(`
SELECT c.id, c.upstream_url
FROM channels c
WHERE NOT EXISTS (SELECT 1 FROM channel_upstreams u WHERE u.channel_id = c.id)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type row struct {
		id, url string
	}
	var missing []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.url); err != nil {
			return err
		}
		missing = append(missing, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range missing {
		id := uuid.NewString()
		if _, err := s.db.Exec(`
INSERT INTO channel_upstreams (id, channel_id, url, headers_json, sort_order)
VALUES (?, ?, ?, '{}', 0)`, id, r.id, r.url); err != nil {
			return err
		}
		if _, err := s.db.Exec(`
UPDATE channels SET fixed_upstream_id = COALESCE(NULLIF(fixed_upstream_id, ''), ?)
WHERE id = ?`, id, r.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureChannelTranscodeColumn() error {
	rows, err := s.db.Query(`PRAGMA table_info(channels)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	hasCol := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == "transcode_enabled" {
			hasCol = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if hasCol {
		return nil
	}
	_, err = s.db.Exec(`ALTER TABLE channels ADD COLUMN transcode_enabled INTEGER NOT NULL DEFAULT 0`)
	return err
}

func (s *Store) ensureBrandSettingsColumns() error {
	hasIcon, err := hasTableColumn(s.db, "app_settings", "brand_icon")
	if err != nil {
		return err
	}
	if !hasIcon {
		if _, err := s.db.Exec(`ALTER TABLE app_settings ADD COLUMN brand_icon TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	hasTitle, err := hasTableColumn(s.db, "app_settings", "brand_title")
	if err != nil {
		return err
	}
	if !hasTitle {
		if _, err := s.db.Exec(`ALTER TABLE app_settings ADD COLUMN brand_title TEXT NOT NULL DEFAULT 'IPTV Relay'`); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureAppSettingsRow() error {
	_, err := s.db.Exec(`
INSERT OR IGNORE INTO app_settings (
  id, video_crf, video_preset, audio_bitrate_kbps, max_height, startup_timeout_seconds, brand_icon, brand_title
) VALUES (1, 23, 'veryfast', 128, 0, 30, '', 'IPTV Relay')`)
	return err
}

func hasTableColumn(db *sql.DB, table, want string) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == want {
			return true, rows.Err()
		}
	}
	return false, rows.Err()
}

// ensureChannelNameUniqueIndex upgrades a legacy non-unique name index (or adds one)
// without rebuilding it on every Open.
func (s *Store) ensureChannelNameUniqueIndex() error {
	var unique int
	err := s.db.QueryRow(`SELECT "unique" FROM pragma_index_list('channels') WHERE name = 'idx_channels_name'`).Scan(&unique)
	if err == nil && unique == 1 {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := s.db.Exec(`DROP INDEX IF EXISTS idx_channels_name`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE UNIQUE INDEX idx_channels_name ON channels(name COLLATE NOCASE)`); err != nil {
		return fmt.Errorf("ensure channel name uniqueness: %w", err)
	}
	return nil
}

// ListChannels returns all global channels.
func (s *Store) ListChannels(ctx context.Context) ([]Channel, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT c.id, c.name, c.logo_url, c.upstream_url, c.headers_json, c.transcode_enabled, c.created_at, c.updated_at
FROM channels c`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		ch, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.attachChannelRelaySlugs(ctx, out); err != nil {
		return nil, err
	}
	if err := s.attachChannelUpstreams(ctx, out); err != nil {
		return nil, err
	}
	slices.SortStableFunc(out, func(a, b Channel) int {
		return utils.NaturalCompare(a.Name, b.Name)
	})
	return out, nil
}

// GetChannel returns a channel by ID.
func (s *Store) GetChannel(ctx context.Context, id string) (Channel, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Channel{}, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx, `
SELECT c.id, c.name, c.logo_url, c.upstream_url, c.headers_json, c.transcode_enabled, c.created_at, c.updated_at
FROM channels c WHERE c.id = ?`, id)
	ch, err := scanChannel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Channel{}, ErrNotFound
	}
	if err != nil {
		return Channel{}, err
	}
	slugs, err := s.ChannelRelaySlugs(ctx, id)
	if err != nil {
		return Channel{}, err
	}
	ch.RelaySlugs = slugs
	ch.RelayCount = len(slugs)
	tmp := []Channel{ch}
	if err := s.attachChannelUpstreams(ctx, tmp); err != nil {
		return Channel{}, err
	}
	return tmp[0], nil
}

func (s *Store) attachChannelRelaySlugs(ctx context.Context, channels []Channel) error {
	if len(channels) == 0 {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT m.channel_id, r.slug
FROM relay_memberships m
JOIN relays r ON r.id = m.relay_id
ORDER BY r.slug ASC, r.id ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	byID := map[string][]string{}
	for rows.Next() {
		var channelID string
		var slug string
		if err := rows.Scan(&channelID, &slug); err != nil {
			return err
		}
		byID[channelID] = append(byID[channelID], slug)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range channels {
		slugs := byID[channels[i].ID]
		if slugs == nil {
			slugs = []string{}
		}
		channels[i].RelaySlugs = slugs
		channels[i].RelayCount = len(slugs)
	}
	return nil
}

// CreateChannel inserts a new global channel.
func (s *Store) CreateChannel(ctx context.Context, in ChannelInput) (Channel, error) {
	var id string
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		ch, err := createChannelTx(ctx, tx, in)
		if err != nil {
			return err
		}
		id = ch.ID
		return nil
	})
	if err != nil {
		return Channel{}, err
	}
	return s.GetChannel(ctx, id)
}

// UpdateChannel updates an existing channel.
func (s *Store) UpdateChannel(ctx context.Context, id string, in ChannelInput) (Channel, error) {
	existing, err := s.GetChannel(ctx, id)
	if err != nil {
		return Channel{}, err
	}
	if err := validateChannelInput(in); err != nil {
		return Channel{}, err
	}
	spec, err := resolveChannelSpec(in, &existing)
	if err != nil {
		return Channel{}, err
	}
	headers := in.Headers
	if headers == nil {
		headers = existing.Headers
	}
	headersJSON, err := json.Marshal(normalizeHeaders(headers))
	if err != nil {
		return Channel{}, err
	}
	transcode := resolveTranscodeEnabled(in.TranscodeEnabled, existing.TranscodeEnabled)
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if err := replaceChannelUpstreamsTx(ctx, tx, id, spec); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
UPDATE channels
SET name = ?, logo_url = ?, upstream_url = ?, headers_json = ?, transcode_enabled = ?,
    upstream_policy = ?, fixed_upstream_id = ?, updated_at = ?
WHERE id = ?`,
			strings.TrimSpace(in.Name),
			strings.TrimSpace(in.LogoURL),
			spec.primaryURL,
			string(headersJSON),
			boolToInt(transcode),
			spec.policy,
			spec.fixedID,
			time.Now().UTC().Format(time.RFC3339Nano),
			id,
		)
		if err != nil {
			return channelConflictErr(err)
		}
		return nil
	})
	if err != nil {
		return Channel{}, err
	}
	return s.GetChannel(ctx, id)
}

// DeleteChannel removes a channel if unused by relays.
func (s *Store) DeleteChannel(ctx context.Context, id string) error {
	slugs, err := s.ChannelRelaySlugs(ctx, id)
	if err != nil {
		return err
	}
	if len(slugs) > 0 {
		return fmt.Errorf("%w: channel used by relays: %s", ErrConflict, strings.Join(slugs, ", "))
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM channels WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ChannelRelaySlugs returns relay slugs that use a channel.
func (s *Store) ChannelRelaySlugs(ctx context.Context, channelID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT r.slug FROM relays r
JOIN relay_memberships m ON m.relay_id = r.id
WHERE m.channel_id = ?
ORDER BY r.slug`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, err
		}
		out = append(out, slug)
	}
	return out, rows.Err()
}

// ListEPGSources returns all EPG sources.
func (s *Store) ListEPGSources(ctx context.Context) ([]EPGSource, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, url, enabled, refresh_interval_seconds, last_refresh_at, last_error, created_at, updated_at
FROM epg_sources ORDER BY name COLLATE NOCASE ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EPGSource
	for rows.Next() {
		src, err := scanEPGSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// ListEnabledEPGSources returns enabled EPG sources.
func (s *Store) ListEnabledEPGSources(ctx context.Context) ([]EPGSource, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, url, enabled, refresh_interval_seconds, last_refresh_at, last_error, created_at, updated_at
FROM epg_sources WHERE enabled = 1 ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EPGSource
	for rows.Next() {
		src, err := scanEPGSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// GetEPGSource returns an EPG source by ID.
func (s *Store) GetEPGSource(ctx context.Context, id int64) (EPGSource, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, name, url, enabled, refresh_interval_seconds, last_refresh_at, last_error, created_at, updated_at
FROM epg_sources WHERE id = ?`, id)
	src, err := scanEPGSource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return EPGSource{}, ErrNotFound
	}
	return src, err
}

// FindEPGSourceByURL returns an EPG source with the given URL.
func (s *Store) FindEPGSourceByURL(ctx context.Context, rawURL string) (EPGSource, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, name, url, enabled, refresh_interval_seconds, last_refresh_at, last_error, created_at, updated_at
FROM epg_sources WHERE url = ? LIMIT 1`, strings.TrimSpace(rawURL))
	src, err := scanEPGSource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return EPGSource{}, ErrNotFound
	}
	return src, err
}

// CreateEPGSource inserts a new EPG source.
func (s *Store) CreateEPGSource(ctx context.Context, in EPGSourceInput, defaultInterval time.Duration) (EPGSource, error) {
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
	res, err := s.db.ExecContext(ctx, `
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
	return s.GetEPGSource(ctx, id)
}

// UpdateEPGSource updates an existing EPG source.
func (s *Store) UpdateEPGSource(ctx context.Context, id int64, in EPGSourceInput, defaultInterval time.Duration) (EPGSource, error) {
	existing, err := s.GetEPGSource(ctx, id)
	if err != nil {
		return EPGSource{}, err
	}
	interval := existing.RefreshInterval
	if strings.TrimSpace(in.RefreshInterval) != "" {
		interval, err = parseRefreshInterval(in.RefreshInterval, defaultInterval)
		if err != nil {
			return EPGSource{}, err
		}
	}
	if err := validateEPGSourceInput(in); err != nil {
		return EPGSource{}, err
	}
	enabled := existing.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	_, err = s.db.ExecContext(ctx, `
UPDATE epg_sources
SET name = ?, url = ?, enabled = ?, refresh_interval_seconds = ?, updated_at = ?
WHERE id = ?`,
		strings.TrimSpace(in.Name),
		strings.TrimSpace(in.URL),
		boolToInt(enabled),
		int(interval.Seconds()),
		time.Now().UTC().Format(time.RFC3339Nano),
		id,
	)
	if err != nil {
		return EPGSource{}, err
	}
	return s.GetEPGSource(ctx, id)
}

// DeleteEPGSource removes an EPG source if unused by relays.
func (s *Store) DeleteEPGSource(ctx context.Context, id int64) error {
	names, err := s.EPGSourceRelayNames(ctx, id)
	if err != nil {
		return err
	}
	if len(names) > 0 {
		return fmt.Errorf("%w: epg source used by relays: %s", ErrConflict, strings.Join(names, ", "))
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM epg_sources WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// EPGSourceRelayNames returns relay names that use an EPG source on a membership.
func (s *Store) EPGSourceRelayNames(ctx context.Context, epgSourceID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT r.name FROM relays r
JOIN relay_memberships m ON m.relay_id = r.id
WHERE m.epg_source_id = ?
ORDER BY r.name`, epgSourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// MarkEPGSourceRefresh records a successful EPG refresh publication.
func (s *Store) MarkEPGSourceRefresh(ctx context.Context, id int64, at time.Time, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE epg_sources SET last_refresh_at = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		at.UTC().Format(time.RFC3339Nano),
		errMsg,
		time.Now().UTC().Format(time.RFC3339Nano),
		id,
	)
	return err
}

// MarkEPGSourceRefreshError records a failed refresh without advancing last_refresh_at.
func (s *Store) MarkEPGSourceRefreshError(ctx context.Context, id int64, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE epg_sources SET last_error = ?, updated_at = ? WHERE id = ?`,
		errMsg,
		time.Now().UTC().Format(time.RFC3339Nano),
		id,
	)
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanChannel(row rowScanner) (Channel, error) {
	var (
		ch          Channel
		headersJSON string
		transcode   int
		createdAt   string
		updatedAt   string
	)
	err := row.Scan(&ch.ID, &ch.Name, &ch.LogoURL, &ch.UpstreamURL, &headersJSON, &transcode, &createdAt, &updatedAt)
	if err != nil {
		return Channel{}, err
	}
	ch.TranscodeEnabled = transcode != 0
	ch.Headers = map[string]string{}
	if headersJSON != "" {
		if err := json.Unmarshal([]byte(headersJSON), &ch.Headers); err != nil {
			return Channel{}, fmt.Errorf("decode headers: %w", err)
		}
	}
	ch.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Channel{}, err
	}
	ch.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return Channel{}, err
	}
	return ch, nil
}

func scanEPGSource(row rowScanner) (EPGSource, error) {
	var (
		src         EPGSource
		enabled     int
		intervalSec int
		lastRefresh sql.NullString
		createdAt   string
		updatedAt   string
	)
	if err := row.Scan(
		&src.ID, &src.Name, &src.URL, &enabled, &intervalSec, &lastRefresh, &src.LastError, &createdAt, &updatedAt,
	); err != nil {
		return EPGSource{}, err
	}
	src.Enabled = enabled == 1
	src.RefreshInterval = time.Duration(intervalSec) * time.Second
	if lastRefresh.Valid && lastRefresh.String != "" {
		t, err := time.Parse(time.RFC3339Nano, lastRefresh.String)
		if err != nil {
			return EPGSource{}, err
		}
		src.LastRefreshAt = &t
	}
	var err error
	src.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return EPGSource{}, err
	}
	src.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return EPGSource{}, err
	}
	return src, nil
}

func validateChannelInput(in ChannelInput) error {
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrValidation)
	}
	if logo := strings.TrimSpace(in.LogoURL); logo != "" {
		parsed, err := url.Parse(logo)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("%w: logo_url must be http(s)", ErrValidation)
		}
	}
	if err := validateHeaderMap(in.Headers); err != nil {
		return err
	}
	return nil
}

func resolveTranscodeEnabled(in *bool, existing bool) bool {
	if in == nil {
		return existing
	}
	return *in
}

func validateEPGSourceInput(in EPGSourceInput) error {
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrValidation)
	}
	u := strings.TrimSpace(in.URL)
	parsed, err := url.Parse(u)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("%w: url must be http(s)", ErrValidation)
	}
	return nil
}

func parseRefreshInterval(raw string, fallback time.Duration) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: refresh_interval must be a duration like 1h", ErrValidation)
	}
	if d < time.Minute {
		return 0, fmt.Errorf("%w: refresh_interval must be >= 1m", ErrValidation)
	}
	return d, nil
}

func normalizeHeaders(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		out[k] = v
	}
	return out
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func isUniqueErr(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}

func channelConflictErr(err error) error {
	if !isUniqueErr(err) {
		return err
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "channels.name") || strings.Contains(msg, "idx_channels_name"):
		return fmt.Errorf("%w: name already exists", ErrConflict)
	case strings.Contains(msg, "upstream_url") || strings.Contains(msg, "channel_upstreams.url"):
		return fmt.Errorf("%w: upstream_url already exists", ErrConflict)
	default:
		return fmt.Errorf("%w: channel already exists", ErrConflict)
	}
}

func normalizeSlug(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	raw = strings.ReplaceAll(raw, "_", "-")
	raw = strings.ReplaceAll(raw, " ", "-")
	for strings.Contains(raw, "--") {
		raw = strings.ReplaceAll(raw, "--", "-")
	}
	return strings.Trim(raw, "-")
}

func validateSlug(slug string) error {
	if !slugRE.MatchString(slug) {
		return fmt.Errorf("%w: slug must be lowercase alphanumeric with hyphens", ErrValidation)
	}
	return nil
}
