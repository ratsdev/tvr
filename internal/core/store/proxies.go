package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ratsdev/tvr/internal/core/upstream"
	"github.com/ratsdev/tvr/internal/utils"
)

func (s *Store) ListProxies(ctx context.Context) ([]Proxy, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT p.id, p.name, p.policy, COALESCE(p.fixed_server_id, ''), p.created_at, p.updated_at,
       (SELECT COUNT(DISTINCT u.channel_id) FROM channel_upstreams u WHERE u.proxy_id = p.id)
FROM proxies p`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Proxy
	for rows.Next() {
		p, err := scanProxy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := attachProxyServers(ctx, s.db, out); err != nil {
		return nil, err
	}
	slices.SortStableFunc(out, func(a, b Proxy) int {
		return utils.NaturalCompare(a.Name, b.Name)
	})
	return out, nil
}

func (s *Store) GetProxy(ctx context.Context, id string) (Proxy, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Proxy{}, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx, `
SELECT p.id, p.name, p.policy, COALESCE(p.fixed_server_id, ''), p.created_at, p.updated_at,
       (SELECT COUNT(DISTINCT u.channel_id) FROM channel_upstreams u WHERE u.proxy_id = p.id)
FROM proxies p WHERE p.id = ?`, id)
	p, err := scanProxy(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Proxy{}, ErrNotFound
	}
	if err != nil {
		return Proxy{}, err
	}
	tmp := []Proxy{p}
	if err := attachProxyServers(ctx, s.db, tmp); err != nil {
		return Proxy{}, err
	}
	return tmp[0], nil
}

func (s *Store) CreateProxy(ctx context.Context, in ProxyInput) (Proxy, error) {
	var id string
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		spec, err := resolveProxySpec(in, nil)
		if err != nil {
			return err
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		id = uuid.NewString()
		_, err = tx.ExecContext(ctx, `
INSERT INTO proxies (id, name, policy, fixed_server_id, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)`,
			id, spec.name, spec.policy, nullIfEmpty(spec.fixedID), now, now)
		if err != nil {
			return proxyConflictErr(err)
		}
		return insertProxyServersTx(ctx, tx, id, spec)
	})
	if err != nil {
		return Proxy{}, err
	}
	return s.GetProxy(ctx, id)
}

// UpdateProxy writes the proxy and refreshes dependent channel primaries.
// affectedIDs are channels that reference the proxy (for live invalidate).
func (s *Store) UpdateProxy(ctx context.Context, id string, in ProxyInput) (Proxy, []string, error) {
	id = strings.TrimSpace(id)
	existing, err := s.GetProxy(ctx, id)
	if err != nil {
		return Proxy{}, nil, err
	}
	var affected []string
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		spec, err := resolveProxySpec(in, &existing)
		if err != nil {
			return err
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		_, err = tx.ExecContext(ctx, `
UPDATE proxies SET name = ?, policy = ?, fixed_server_id = ?, updated_at = ? WHERE id = ?`,
			spec.name, spec.policy, nullIfEmpty(spec.fixedID), now, id)
		if err != nil {
			return proxyConflictErr(err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM proxy_servers WHERE proxy_id = ?`, id); err != nil {
			return err
		}
		if err := insertProxyServersTx(ctx, tx, id, spec); err != nil {
			return err
		}
		saved := Proxy{ID: id, Name: spec.name, Policy: spec.policy, FixedServerID: spec.fixedID, Servers: spec.servers}
		ids, err := refreshProxyDependentsTx(ctx, tx, saved, now)
		if err != nil {
			return err
		}
		affected = ids
		return nil
	})
	if err != nil {
		return Proxy{}, nil, err
	}
	p, err := s.GetProxy(ctx, id)
	return p, affected, err
}

func (s *Store) DeleteProxy(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrNotFound
	}
	n, err := countProxyChannelsTx(ctx, s.db, id)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: proxy used by %d channel(s)", ErrConflict, n)
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM proxies WHERE id = ?`, id)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

type proxySpec struct {
	name    string
	policy  string
	fixedID string
	servers []ProxyServer
}

func resolveProxySpec(in ProxyInput, existing *Proxy) (proxySpec, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return proxySpec{}, fmt.Errorf("%w: name is required", ErrValidation)
	}
	policy, err := upstream.ParseProxyPolicy(in.Policy)
	if err != nil {
		return proxySpec{}, fmt.Errorf("%w: %s", ErrValidation, err.Error())
	}
	raw := in.Servers
	if raw == nil && existing != nil {
		raw = append([]ProxyServer(nil), existing.Servers...)
	}
	if len(raw) == 0 {
		return proxySpec{}, fmt.Errorf("%w: at least one server is required", ErrValidation)
	}
	seen := map[string]struct{}{}
	usedID := map[string]struct{}{}
	out := make([]ProxyServer, 0, len(raw))
	for _, srv := range raw {
		item, err := normalizeProxyServer(srv, usedID)
		if err != nil {
			return proxySpec{}, err
		}
		if _, dup := seen[item.URL]; dup {
			return proxySpec{}, fmt.Errorf("%w: duplicate server url", ErrValidation)
		}
		seen[item.URL] = struct{}{}
		out = append(out, item)
	}
	fixedID := strings.TrimSpace(in.FixedServerID)
	if fixedID == "" && existing != nil {
		fixedID = strings.TrimSpace(existing.FixedServerID)
	}
	if _, ok := usedID[fixedID]; !ok {
		fixedID = out[0].ID
	}
	return proxySpec{name: name, policy: policy, fixedID: fixedID, servers: out}, nil
}

func normalizeProxyServer(s ProxyServer, usedID map[string]struct{}) (ProxyServer, error) {
	raw := strings.TrimSpace(s.URL)
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ProxyServer{}, fmt.Errorf("%w: server url must be http(s)", ErrValidation)
	}
	id := strings.TrimSpace(s.ID)
	if id == "" {
		id = uuid.NewString()
	}
	if _, ok := usedID[id]; ok {
		id = uuid.NewString()
	}
	usedID[id] = struct{}{}
	return ProxyServer{ID: id, URL: raw}, nil
}

func insertProxyServersTx(ctx context.Context, q querier, proxyID string, spec proxySpec) error {
	for i, srv := range spec.servers {
		if _, err := q.ExecContext(ctx, `
INSERT INTO proxy_servers (id, proxy_id, url, sort_order) VALUES (?, ?, ?, ?)`,
			srv.ID, proxyID, srv.URL, i); err != nil {
			return err
		}
	}
	return nil
}

func attachProxyServers(ctx context.Context, q querier, proxies []Proxy) error {
	if len(proxies) == 0 {
		return nil
	}
	rows, err := q.QueryContext(ctx, `
SELECT id, proxy_id, url, sort_order FROM proxy_servers
ORDER BY proxy_id ASC, sort_order ASC, id ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	byProxy := map[string][]ProxyServer{}
	for rows.Next() {
		var item ProxyServer
		var proxyID string
		var sortOrder int
		if err := rows.Scan(&item.ID, &proxyID, &item.URL, &sortOrder); err != nil {
			return err
		}
		byProxy[proxyID] = append(byProxy[proxyID], item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range proxies {
		srvs := byProxy[proxies[i].ID]
		if srvs == nil {
			srvs = []ProxyServer{}
		}
		proxies[i].Servers = srvs
		if proxies[i].FixedServerID == "" && len(srvs) > 0 {
			proxies[i].FixedServerID = srvs[0].ID
		}
	}
	return nil
}

func loadProxyMapTx(ctx context.Context, q querier, ids []string) (map[string]Proxy, error) {
	out := map[string]Proxy{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, `
SELECT id, name, policy, COALESCE(fixed_server_id, ''), created_at, updated_at, 0
FROM proxies`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []Proxy
	want := map[string]struct{}{}
	for _, id := range ids {
		want[id] = struct{}{}
	}
	for rows.Next() {
		p, err := scanProxy(rows)
		if err != nil {
			return nil, err
		}
		if _, ok := want[p.ID]; ok {
			all = append(all, p)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := attachProxyServers(ctx, q, all); err != nil {
		return nil, err
	}
	for _, p := range all {
		out[p.ID] = p
	}
	return out, nil
}

func refreshProxyDependentsTx(ctx context.Context, q querier, p Proxy, now string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `
SELECT DISTINCT channel_id FROM channel_upstreams WHERE proxy_id = ?`, p.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if _, err := q.ExecContext(ctx, `
UPDATE channels SET updated_at = ? WHERE id IN (`+placeholders(len(ids))+`)`,
		append([]any{now}, strToAny(ids)...)...); err != nil {
		return nil, err
	}
	channels := make([]Channel, 0, len(ids))
	for _, id := range ids {
		channels = append(channels, Channel{ID: id})
	}
	if err := attachChannelUpstreams(ctx, q, channels); err != nil {
		return nil, err
	}
	for _, ch := range channels {
		primary := ch.PrimaryUpstream()
		if primary.ProxyID != p.ID {
			continue
		}
		if _, err := q.ExecContext(ctx, `UPDATE channels SET upstream_url = ? WHERE id = ?`,
			primary.StablePrimary(), ch.ID); err != nil {
			return nil, channelConflictErr(err)
		}
	}
	return ids, nil
}

func countProxyChannelsTx(ctx context.Context, q querier, proxyID string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `
SELECT COUNT(DISTINCT channel_id) FROM channel_upstreams WHERE proxy_id = ?`, proxyID).Scan(&n)
	return n, err
}

func scanProxy(row rowScanner) (Proxy, error) {
	var (
		p         Proxy
		createdAt string
		updatedAt string
	)
	if err := row.Scan(&p.ID, &p.Name, &p.Policy, &p.FixedServerID, &createdAt, &updatedAt, &p.ChannelCount); err != nil {
		return Proxy{}, err
	}
	var err error
	p.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Proxy{}, err
	}
	p.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return Proxy{}, err
	}
	return p, nil
}

func proxyConflictErr(err error) error {
	if !isUniqueErr(err) {
		return err
	}
	return fmt.Errorf("%w: name already exists", ErrConflict)
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

func strToAny(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}
