package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ListRelays returns all relays.
func (s *Store) ListRelays(ctx context.Context) ([]Relay, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, slug, created_at, updated_at FROM relays ORDER BY name COLLATE NOCASE ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Relay
	for rows.Next() {
		r, err := scanRelay(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRelay returns a relay by ID.
func (s *Store) GetRelay(ctx context.Context, id int64) (Relay, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, name, slug, created_at, updated_at FROM relays WHERE id = ?`, id)
	r, err := scanRelay(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Relay{}, ErrNotFound
	}
	return r, err
}

// GetRelayBySlug returns a relay by slug.
func (s *Store) GetRelayBySlug(ctx context.Context, slug string) (Relay, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, name, slug, created_at, updated_at FROM relays WHERE slug = ?`, normalizeSlug(slug))
	r, err := scanRelay(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Relay{}, ErrNotFound
	}
	return r, err
}

// GetRelayDetail returns relay + groups + memberships.
func (s *Store) GetRelayDetail(ctx context.Context, id int64) (RelayDetail, error) {
	relay, err := s.GetRelay(ctx, id)
	if err != nil {
		return RelayDetail{}, err
	}
	groups, err := s.ListRelayGroups(ctx, id)
	if err != nil {
		return RelayDetail{}, err
	}
	memberships, err := s.ListRelayMemberships(ctx, id)
	if err != nil {
		return RelayDetail{}, err
	}
	return RelayDetail{
		Relay:       relay,
		Groups:      groups,
		Memberships: memberships,
	}, nil
}

// CreateRelay creates a relay and an initial empty group.
func (s *Store) CreateRelay(ctx context.Context, in RelayInput) (Relay, error) {
	name := strings.TrimSpace(in.Name)
	slug := normalizeSlug(in.Slug)
	if name == "" {
		return Relay{}, fmt.Errorf("%w: name is required", ErrValidation)
	}
	if slug == "" {
		slug = normalizeSlug(name)
	}
	if err := validateSlug(slug); err != nil {
		return Relay{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Relay{}, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
INSERT INTO relays (name, slug, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		name, slug, now, now)
	if err != nil {
		if isUniqueErr(err) {
			return Relay{}, fmt.Errorf("%w: slug already exists", ErrConflict)
		}
		return Relay{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Relay{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO relay_groups (relay_id, name, sort_order) VALUES (?, 'Channels', 0)`, id); err != nil {
		return Relay{}, err
	}
	if err := tx.Commit(); err != nil {
		return Relay{}, err
	}
	return s.GetRelay(ctx, id)
}

// UpdateRelay updates relay name/slug.
func (s *Store) UpdateRelay(ctx context.Context, id int64, in RelayInput) (Relay, error) {
	if _, err := s.GetRelay(ctx, id); err != nil {
		return Relay{}, err
	}
	name := strings.TrimSpace(in.Name)
	slug := normalizeSlug(in.Slug)
	if name == "" {
		return Relay{}, fmt.Errorf("%w: name is required", ErrValidation)
	}
	if err := validateSlug(slug); err != nil {
		return Relay{}, err
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE relays SET name = ?, slug = ?, updated_at = ? WHERE id = ?`,
		name, slug, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		if isUniqueErr(err) {
			return Relay{}, fmt.Errorf("%w: slug already exists", ErrConflict)
		}
		return Relay{}, err
	}
	return s.GetRelay(ctx, id)
}

// DeleteRelay deletes a relay and its groups/memberships/bindings.
func (s *Store) DeleteRelay(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM relays WHERE id = ?`, id)
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

// ListRelayGroups returns ordered groups for a relay.
func (s *Store) ListRelayGroups(ctx context.Context, relayID int64) ([]RelayGroup, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, relay_id, name, sort_order FROM relay_groups
WHERE relay_id = ? ORDER BY sort_order ASC, id ASC`, relayID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RelayGroup
	for rows.Next() {
		var g RelayGroup
		if err := rows.Scan(&g.ID, &g.RelayID, &g.Name, &g.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	if out == nil {
		out = []RelayGroup{}
	}
	return out, rows.Err()
}

// CreateRelayGroup creates a group at the end of the relay.
func (s *Store) CreateRelayGroup(ctx context.Context, relayID int64, name string) (RelayGroup, error) {
	if _, err := s.GetRelay(ctx, relayID); err != nil {
		return RelayGroup{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return RelayGroup{}, fmt.Errorf("%w: group name is required", ErrValidation)
	}
	var maxOrd sql.NullInt64
	_ = s.db.QueryRowContext(ctx, `SELECT MAX(sort_order) FROM relay_groups WHERE relay_id = ?`, relayID).Scan(&maxOrd)
	ord := 0
	if maxOrd.Valid {
		ord = int(maxOrd.Int64) + 1
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO relay_groups (relay_id, name, sort_order) VALUES (?, ?, ?)`, relayID, name, ord)
	if err != nil {
		return RelayGroup{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return RelayGroup{}, err
	}
	return RelayGroup{ID: id, RelayID: relayID, Name: name, SortOrder: ord}, nil
}

// UpdateRelayGroup renames a group scoped to its parent relay.
func (s *Store) UpdateRelayGroup(ctx context.Context, relayID, groupID int64, name string) (RelayGroup, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return RelayGroup{}, fmt.Errorf("%w: group name is required", ErrValidation)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE relay_groups SET name = ? WHERE id = ? AND relay_id = ?`, name, groupID, relayID)
	if err != nil {
		return RelayGroup{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return RelayGroup{}, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx, `SELECT id, relay_id, name, sort_order FROM relay_groups WHERE id = ? AND relay_id = ?`, groupID, relayID)
	var g RelayGroup
	if err := row.Scan(&g.ID, &g.RelayID, &g.Name, &g.SortOrder); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RelayGroup{}, ErrNotFound
		}
		return RelayGroup{}, err
	}
	return g, nil
}

// DeleteRelayGroup deletes a group scoped to its parent relay if it has no memberships.
func (s *Store) DeleteRelayGroup(ctx context.Context, relayID, groupID int64) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM relay_memberships WHERE group_id = ? AND relay_id = ?`, groupID, relayID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("%w: group still has channels", ErrConflict)
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM relay_groups WHERE id = ? AND relay_id = ?`, groupID, relayID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListRelayMemberships returns memberships with joined channel/group info.
func (s *Store) ListRelayMemberships(ctx context.Context, relayID int64) ([]RelayMembership, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT m.id, m.relay_id, m.group_id, m.channel_id, m.number, c.epg_source_id, c.tvg_id, m.sort_order,
       c.name, c.logo_url, c.upstream_url, g.name
FROM relay_memberships m
JOIN channels c ON c.id = m.channel_id
JOIN relay_groups g ON g.id = m.group_id
WHERE m.relay_id = ?
ORDER BY g.sort_order ASC, m.sort_order ASC, m.id ASC`, relayID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RelayMembership
	for rows.Next() {
		m, err := scanMembership(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if out == nil {
		out = []RelayMembership{}
	}
	return out, rows.Err()
}

// GetMembership returns a membership by ID.
func (s *Store) GetMembership(ctx context.Context, id int64) (RelayMembership, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT m.id, m.relay_id, m.group_id, m.channel_id, m.number, c.epg_source_id, c.tvg_id, m.sort_order,
       c.name, c.logo_url, c.upstream_url, g.name
FROM relay_memberships m
JOIN channels c ON c.id = m.channel_id
JOIN relay_groups g ON g.id = m.group_id
WHERE m.id = ?`, id)
	m, err := scanMembership(row)
	if errors.Is(err, sql.ErrNoRows) {
		return RelayMembership{}, ErrNotFound
	}
	return m, err
}

// GetMembershipInRelay returns a membership if it belongs to the relay.
func (s *Store) GetMembershipInRelay(ctx context.Context, relayID, membershipID int64) (RelayMembership, error) {
	m, err := s.GetMembership(ctx, membershipID)
	if err != nil {
		return RelayMembership{}, err
	}
	if m.RelayID != relayID {
		return RelayMembership{}, ErrNotFound
	}
	return m, nil
}

func renumberRelayMemberships(ctx context.Context, q querier, relayID int64) error {
	rows, err := q.QueryContext(ctx, `
SELECT m.id
FROM relay_memberships m
JOIN relay_groups g ON g.id = m.group_id
WHERE m.relay_id = ?
ORDER BY g.sort_order ASC, m.sort_order ASC, m.id ASC`, relayID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i, id := range ids {
		if _, err := q.ExecContext(ctx, `
UPDATE relay_memberships SET number = ? WHERE id = ? AND relay_id = ?`, i+1, id, relayID); err != nil {
			return err
		}
	}
	return nil
}

func getMembershipQ(ctx context.Context, q querier, id int64) (RelayMembership, error) {
	row := q.QueryRowContext(ctx, `
SELECT m.id, m.relay_id, m.group_id, m.channel_id, m.number, c.epg_source_id, c.tvg_id, m.sort_order,
       c.name, c.logo_url, c.upstream_url, g.name
FROM relay_memberships m
JOIN channels c ON c.id = m.channel_id
JOIN relay_groups g ON g.id = m.group_id
WHERE m.id = ?`, id)
	m, err := scanMembership(row)
	if errors.Is(err, sql.ErrNoRows) {
		return RelayMembership{}, ErrNotFound
	}
	return m, err
}

func validateMembershipInputQ(ctx context.Context, q querier, relayID int64, in MembershipInput) error {
	if strings.TrimSpace(in.ChannelID) == "" {
		return fmt.Errorf("%w: channel_id is required", ErrValidation)
	}
	if in.GroupID <= 0 {
		return fmt.Errorf("%w: group_id is required", ErrValidation)
	}
	var groupRelay int64
	if err := q.QueryRowContext(ctx, `SELECT relay_id FROM relay_groups WHERE id = ?`, in.GroupID).Scan(&groupRelay); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: group not found", ErrValidation)
		}
		return err
	}
	if groupRelay != relayID {
		return fmt.Errorf("%w: group does not belong to relay", ErrValidation)
	}
	var exists int
	if err := q.QueryRowContext(ctx, `SELECT 1 FROM channels WHERE id = ?`, in.ChannelID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: channel not found", ErrValidation)
		}
		return err
	}
	return nil
}

func addMembershipTx(ctx context.Context, tx *sql.Tx, relayID int64, in MembershipInput) (RelayMembership, error) {
	if err := validateMembershipInputQ(ctx, tx, relayID, in); err != nil {
		return RelayMembership{}, err
	}
	var maxOrd sql.NullInt64
	_ = tx.QueryRowContext(ctx, `
SELECT MAX(sort_order) FROM relay_memberships WHERE group_id = ?`, in.GroupID).Scan(&maxOrd)
	ord := 0
	if in.SortOrder != nil {
		ord = *in.SortOrder
	} else if maxOrd.Valid {
		ord = int(maxOrd.Int64) + 1
	}
	res, err := tx.ExecContext(ctx, `
INSERT INTO relay_memberships (relay_id, group_id, channel_id, number, sort_order)
VALUES (?, ?, ?, ?, ?)`,
		relayID, in.GroupID, in.ChannelID, 0, ord)
	if err != nil {
		if isUniqueErr(err) {
			return RelayMembership{}, fmt.Errorf("%w: channel already in this relay", ErrConflict)
		}
		return RelayMembership{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return RelayMembership{}, err
	}
	if err := renumberRelayMemberships(ctx, tx, relayID); err != nil {
		return RelayMembership{}, err
	}
	return getMembershipQ(ctx, tx, id)
}

func updateMembershipTx(ctx context.Context, tx *sql.Tx, membershipID int64, in MembershipInput) (RelayMembership, error) {
	existing, err := getMembershipQ(ctx, tx, membershipID)
	if err != nil {
		return RelayMembership{}, err
	}
	if strings.TrimSpace(in.ChannelID) == "" {
		in.ChannelID = existing.ChannelID
	}
	if in.GroupID == 0 {
		in.GroupID = existing.GroupID
	}
	if err := validateMembershipInputQ(ctx, tx, existing.RelayID, in); err != nil {
		return RelayMembership{}, err
	}
	ord := existing.SortOrder
	if in.SortOrder != nil {
		ord = *in.SortOrder
	}
	_, err = tx.ExecContext(ctx, `
UPDATE relay_memberships
SET group_id = ?, channel_id = ?, sort_order = ?
WHERE id = ?`,
		in.GroupID, in.ChannelID, ord, membershipID)
	if err != nil {
		if isUniqueErr(err) {
			return RelayMembership{}, fmt.Errorf("%w: channel already in this relay", ErrConflict)
		}
		return RelayMembership{}, err
	}
	if err := renumberRelayMemberships(ctx, tx, existing.RelayID); err != nil {
		return RelayMembership{}, err
	}
	return getMembershipQ(ctx, tx, membershipID)
}

func deleteMembershipTx(ctx context.Context, tx *sql.Tx, membershipID int64) error {
	existing, err := getMembershipQ(ctx, tx, membershipID)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM relay_memberships WHERE id = ?`, membershipID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return renumberRelayMemberships(ctx, tx, existing.RelayID)
}

// AddMembership adds a channel to a relay group.
// Channel numbers are assigned from lineup order, not from the request.
func (s *Store) AddMembership(ctx context.Context, relayID int64, in MembershipInput) (RelayMembership, error) {
	var out RelayMembership
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		m, err := addMembershipTx(ctx, tx, relayID, in)
		if err != nil {
			return err
		}
		out = m
		return nil
	})
	return out, err
}

// UpdateMembership updates membership fields.
// Channel numbers follow lineup order and are not taken from the request.
func (s *Store) UpdateMembership(ctx context.Context, membershipID int64, in MembershipInput) (RelayMembership, error) {
	var out RelayMembership
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		m, err := updateMembershipTx(ctx, tx, membershipID, in)
		if err != nil {
			return err
		}
		out = m
		return nil
	})
	return out, err
}

// DeleteMembership removes a membership.
func (s *Store) DeleteMembership(ctx context.Context, membershipID int64) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		return deleteMembershipTx(ctx, tx, membershipID)
	})
}

// ReplaceRelayLayout atomically reorders groups and memberships.
func (s *Store) ReplaceRelayLayout(ctx context.Context, relayID int64, layout RelayLayout) (RelayDetail, error) {
	if _, err := s.GetRelay(ctx, relayID); err != nil {
		return RelayDetail{}, err
	}
	// Load current layout before opening a transaction: MaxOpenConns is 1.
	existingGroups, err := s.ListRelayGroups(ctx, relayID)
	if err != nil {
		return RelayDetail{}, err
	}
	existingMembers, err := s.ListRelayMemberships(ctx, relayID)
	if err != nil {
		return RelayDetail{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RelayDetail{}, err
	}
	defer func() { _ = tx.Rollback() }()
	groupSet := map[int64]struct{}{}
	for _, g := range existingGroups {
		groupSet[g.ID] = struct{}{}
	}
	memberSet := map[int64]struct{}{}
	for _, m := range existingMembers {
		memberSet[m.ID] = struct{}{}
	}
	if len(layout.Groups) != len(existingGroups) {
		return RelayDetail{}, fmt.Errorf("%w: layout must include all groups", ErrValidation)
	}

	seenGroups := map[int64]struct{}{}
	seenMembers := map[int64]struct{}{}
	channelNo := 0
	for gi, g := range layout.Groups {
		if _, ok := groupSet[g.ID]; !ok {
			return RelayDetail{}, fmt.Errorf("%w: unknown group %d", ErrValidation, g.ID)
		}
		if _, ok := seenGroups[g.ID]; ok {
			return RelayDetail{}, fmt.Errorf("%w: duplicate group %d", ErrValidation, g.ID)
		}
		seenGroups[g.ID] = struct{}{}
		name := strings.TrimSpace(g.Name)
		if name == "" {
			return RelayDetail{}, fmt.Errorf("%w: group name is required", ErrValidation)
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE relay_groups SET name = ?, sort_order = ? WHERE id = ? AND relay_id = ?`,
			name, gi, g.ID, relayID); err != nil {
			return RelayDetail{}, err
		}
		for mi, mid := range g.MembershipIDs {
			if _, ok := memberSet[mid]; !ok {
				return RelayDetail{}, fmt.Errorf("%w: unknown membership %d", ErrValidation, mid)
			}
			if _, ok := seenMembers[mid]; ok {
				return RelayDetail{}, fmt.Errorf("%w: duplicate membership %d", ErrValidation, mid)
			}
			seenMembers[mid] = struct{}{}
			channelNo++
			if _, err := tx.ExecContext(ctx, `
UPDATE relay_memberships SET group_id = ?, sort_order = ?, number = ? WHERE id = ? AND relay_id = ?`,
				g.ID, mi, channelNo, mid, relayID); err != nil {
				return RelayDetail{}, err
			}
		}
	}
	if len(seenMembers) != len(existingMembers) {
		return RelayDetail{}, fmt.Errorf("%w: layout must include all memberships", ErrValidation)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE relays SET updated_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), relayID); err != nil {
		return RelayDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return RelayDetail{}, err
	}
	return s.GetRelayDetail(ctx, relayID)
}

// ListRelayLineup returns ordered playlist entries for a relay slug.
func (s *Store) ListRelayLineup(ctx context.Context, slug string) (Relay, []LineupEntry, error) {
	relay, err := s.GetRelayBySlug(ctx, slug)
	if err != nil {
		return Relay{}, nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT m.id, m.channel_id, c.name, c.logo_url, c.upstream_url, c.headers_json,
       m.number, c.tvg_id, g.name, c.epg_source_id
FROM relay_memberships m
JOIN channels c ON c.id = m.channel_id
JOIN relay_groups g ON g.id = m.group_id
WHERE m.relay_id = ?
ORDER BY g.sort_order ASC, m.sort_order ASC, m.id ASC`, relay.ID)
	if err != nil {
		return relay, nil, err
	}
	defer rows.Close()
	var out []LineupEntry
	for rows.Next() {
		var (
			e           LineupEntry
			headersJSON string
			epgID       sql.NullInt64
		)
		if err := rows.Scan(&e.MembershipID, &e.ChannelID, &e.Name, &e.LogoURL, &e.UpstreamURL, &headersJSON,
			&e.Number, &e.TvgID, &e.GroupTitle, &epgID); err != nil {
			return relay, nil, err
		}
		e.Headers = map[string]string{}
		if headersJSON != "" {
			_ = json.Unmarshal([]byte(headersJSON), &e.Headers)
		}
		if epgID.Valid {
			v := epgID.Int64
			e.EPGSourceID = &v
		}
		out = append(out, e)
	}
	if out == nil {
		out = []LineupEntry{}
	}
	return relay, out, rows.Err()
}

// ListAllRelayEPGMappings returns all membership EPG bindings for refresh.
func (s *Store) ListAllRelayEPGMappings(ctx context.Context) ([]RelayEPGMapping, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT r.id, r.slug, c.epg_source_id, c.tvg_id
FROM relay_memberships m
JOIN relays r ON r.id = m.relay_id
JOIN channels c ON c.id = m.channel_id
WHERE c.epg_source_id IS NOT NULL AND c.tvg_id <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RelayEPGMapping
	for rows.Next() {
		var m RelayEPGMapping
		if err := rows.Scan(&m.RelayID, &m.RelaySlug, &m.EPGSourceID, &m.TvgID); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func scanRelay(row rowScanner) (Relay, error) {
	var (
		r         Relay
		createdAt string
		updatedAt string
	)
	if err := row.Scan(&r.ID, &r.Name, &r.Slug, &createdAt, &updatedAt); err != nil {
		return Relay{}, err
	}
	var err error
	r.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Relay{}, err
	}
	r.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return Relay{}, err
	}
	return r, nil
}

func scanMembership(row rowScanner) (RelayMembership, error) {
	var (
		m     RelayMembership
		epgID sql.NullInt64
	)
	if err := row.Scan(
		&m.ID, &m.RelayID, &m.GroupID, &m.ChannelID, &m.Number, &epgID, &m.TvgID, &m.SortOrder,
		&m.ChannelName, &m.LogoURL, &m.UpstreamURL, &m.GroupName,
	); err != nil {
		return RelayMembership{}, err
	}
	if epgID.Valid {
		v := epgID.Int64
		m.EPGSourceID = &v
	}
	return m, nil
}

func nullableInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}
