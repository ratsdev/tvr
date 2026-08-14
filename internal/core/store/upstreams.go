package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ratsdev/tvr/internal/core/upstream"
)

type channelSpec struct {
	upstreams  []ChannelUpstream
	policy     string
	fixedID    string
	primaryURL string
}

func normalizeUpstreamPolicy(raw string) (string, error) {
	p, err := upstream.ParsePolicy(raw)
	if err != nil {
		return "", fmt.Errorf("%w: upstream_policy must be fixed, random, or failover", ErrValidation)
	}
	return p, nil
}

func resolveChannelSpec(ctx context.Context, q querier, in ChannelInput, existing *Channel) (channelSpec, error) {
	policyRaw := strings.TrimSpace(in.UpstreamPolicy)
	if policyRaw == "" && existing != nil {
		policyRaw = existing.UpstreamPolicy
	}
	policy, err := normalizeUpstreamPolicy(policyRaw)
	if err != nil {
		return channelSpec{}, err
	}
	raw := in.Upstreams
	if raw == nil {
		if existing != nil {
			raw = append([]ChannelUpstream(nil), existing.Upstreams...)
		} else {
			raw = []ChannelUpstream{{URL: in.UpstreamURL}}
		}
	}
	if len(raw) == 0 {
		return channelSpec{}, fmt.Errorf("%w: at least one upstream is required", ErrValidation)
	}

	seenKey := map[string]struct{}{}
	usedID := map[string]struct{}{}
	out := make([]ChannelUpstream, 0, len(raw))
	proxyIDs := map[string]struct{}{}
	for _, u := range raw {
		item, err := normalizeChannelUpstream(u, usedID)
		if err != nil {
			return channelSpec{}, err
		}
		key := item.URL + "\x00" + item.ProxyID
		if _, dup := seenKey[key]; dup {
			return channelSpec{}, fmt.Errorf("%w: duplicate upstream url", ErrValidation)
		}
		seenKey[key] = struct{}{}
		if item.ProxyID != "" {
			proxyIDs[item.ProxyID] = struct{}{}
		}
		out = append(out, item)
	}

	proxies, err := loadProxyMapTx(ctx, q, keys(proxyIDs))
	if err != nil {
		return channelSpec{}, err
	}
	for i := range out {
		if out[i].ProxyID == "" {
			continue
		}
		p, ok := proxies[out[i].ProxyID]
		if !ok {
			return channelSpec{}, fmt.Errorf("%w: unknown proxy_id", ErrValidation)
		}
		out[i].proxy = snapshotProxy(p)
	}

	seenResolved := map[string]struct{}{}
	for _, u := range out {
		for _, ru := range resolvedForDedupe(u) {
			if _, dup := seenResolved[ru]; dup {
				return channelSpec{}, fmt.Errorf("%w: duplicate resolved upstream url", ErrValidation)
			}
			seenResolved[ru] = struct{}{}
		}
	}

	fixedID := strings.TrimSpace(in.FixedUpstreamID)
	if fixedID == "" && existing != nil {
		fixedID = strings.TrimSpace(existing.FixedUpstreamID)
	}
	if _, ok := usedID[fixedID]; !ok {
		fixedID = out[0].ID
	}
	primary := out[0]
	if policy == UpstreamPolicyFixed {
		for _, u := range out {
			if u.ID == fixedID {
				primary = u
				break
			}
		}
	}
	return channelSpec{upstreams: out, policy: policy, fixedID: fixedID, primaryURL: primary.StablePrimary()}, nil
}

func keys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}

func resolvedForDedupe(u ChannelUpstream) []string {
	ref := u.proxyRef()
	if ref != nil && ref.Policy == upstream.PolicyFailover {
		return upstream.Resolve(u.URL, ref)
	}
	return []string{u.StablePrimary()}
}

func normalizeChannelUpstream(u ChannelUpstream, usedID map[string]struct{}) (ChannelUpstream, error) {
	raw := strings.TrimSpace(u.URL)
	proxyID := strings.TrimSpace(u.ProxyID)
	if proxyID == "" {
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return ChannelUpstream{}, fmt.Errorf("%w: upstream url must be http(s)", ErrValidation)
		}
	} else if !upstream.ValidProxiedLink(raw) {
		return ChannelUpstream{}, fmt.Errorf("%w: proxied upstream must be host:port (no scheme)", ErrValidation)
	}
	headers := normalizeHeaders(u.Headers)
	if err := validateHeaderMap(headers); err != nil {
		return ChannelUpstream{}, err
	}
	id := strings.TrimSpace(u.ID)
	if id == "" {
		id = uuid.NewString()
	}
	if _, ok := usedID[id]; ok {
		id = uuid.NewString()
	}
	usedID[id] = struct{}{}
	out := ChannelUpstream{ID: id, URL: raw, ProxyID: proxyID}
	if len(headers) > 0 {
		out.Headers = headers
	}
	return out, nil
}

func insertChannelUpstreamsTx(ctx context.Context, q querier, channelID string, spec channelSpec) error {
	for i, u := range spec.upstreams {
		headersJSON, err := json.Marshal(normalizeHeaders(u.Headers))
		if err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, `
INSERT INTO channel_upstreams (id, channel_id, url, headers_json, sort_order, proxy_id)
VALUES (?, ?, ?, ?, ?, ?)`,
			u.ID, channelID, u.URL, string(headersJSON), i, nullIfEmpty(u.ProxyID)); err != nil {
			return channelConflictErr(err)
		}
	}
	return nil
}

func replaceChannelUpstreamsTx(ctx context.Context, q querier, channelID string, spec channelSpec) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM channel_upstreams WHERE channel_id = ?`, channelID); err != nil {
		return err
	}
	return insertChannelUpstreamsTx(ctx, q, channelID, spec)
}

func attachChannelUpstreams(ctx context.Context, q querier, channels []Channel) error {
	if len(channels) == 0 {
		return nil
	}
	policyRows, err := q.QueryContext(ctx, `
SELECT id, COALESCE(upstream_policy, ''), COALESCE(fixed_upstream_id, '')
FROM channels`)
	if err != nil {
		return err
	}
	defer policyRows.Close()
	policyByID := map[string]string{}
	fixedByID := map[string]string{}
	for policyRows.Next() {
		var id, policy, fixed string
		if err := policyRows.Scan(&id, &policy, &fixed); err != nil {
			return err
		}
		policyByID[id] = policy
		fixedByID[id] = fixed
	}
	if err := policyRows.Err(); err != nil {
		return err
	}

	upRows, err := q.QueryContext(ctx, `
SELECT id, channel_id, url, headers_json, sort_order, proxy_id
FROM channel_upstreams
ORDER BY channel_id ASC, sort_order ASC, id ASC`)
	if err != nil {
		return err
	}
	defer upRows.Close()
	byChannel := map[string][]ChannelUpstream{}
	proxyIDs := map[string]struct{}{}
	for upRows.Next() {
		var (
			item        ChannelUpstream
			channelID   string
			headersJSON string
			sortOrder   int
			proxyID     sql.NullString
		)
		if err := upRows.Scan(&item.ID, &channelID, &item.URL, &headersJSON, &sortOrder, &proxyID); err != nil {
			return err
		}
		item.Headers = map[string]string{}
		if headersJSON != "" {
			if err := json.Unmarshal([]byte(headersJSON), &item.Headers); err != nil {
				return fmt.Errorf("decode upstream headers: %w", err)
			}
		}
		if len(item.Headers) == 0 {
			item.Headers = nil
		}
		if proxyID.Valid {
			item.ProxyID = strings.TrimSpace(proxyID.String)
			if item.ProxyID != "" {
				proxyIDs[item.ProxyID] = struct{}{}
			}
		}
		byChannel[channelID] = append(byChannel[channelID], item)
	}
	if err := upRows.Err(); err != nil {
		return err
	}

	proxies, err := loadProxyMapTx(ctx, q, keys(proxyIDs))
	if err != nil {
		return err
	}
	for i := range channels {
		id := channels[i].ID
		policy := policyByID[id]
		if policy == "" {
			policy = UpstreamPolicyFixed
		}
		channels[i].UpstreamPolicy = policy
		channels[i].FixedUpstreamID = fixedByID[id]
		ups := byChannel[id]
		if ups == nil {
			ups = []ChannelUpstream{}
		}
		for j := range ups {
			if p, ok := proxies[ups[j].ProxyID]; ok {
				ups[j].proxy = snapshotProxy(p)
			}
		}
		channels[i].Upstreams = ups
		if channels[i].FixedUpstreamID == "" && len(ups) > 0 {
			channels[i].FixedUpstreamID = ups[0].ID
		}
	}
	return nil
}

func (s *Store) attachChannelUpstreams(ctx context.Context, channels []Channel) error {
	return attachChannelUpstreams(ctx, s.db, channels)
}

// PrimaryUpstream returns the derived primary row (fixed pick, else first).
func (c Channel) PrimaryUpstream() ChannelUpstream {
	if c.UpstreamPolicy == UpstreamPolicyFixed && c.FixedUpstreamID != "" {
		if u, ok := c.UpstreamByID(c.FixedUpstreamID); ok {
			return u
		}
	}
	if len(c.Upstreams) > 0 {
		return c.Upstreams[0]
	}
	return ChannelUpstream{URL: c.UpstreamURL}
}

// SelectUpstream picks a row for a one-shot probe. Explicit id wins; otherwise
// random policy rolls a URL and every other policy uses the derived primary.
func (c Channel) SelectUpstream(upstreamID string) (ChannelUpstream, error) {
	if id := strings.TrimSpace(upstreamID); id != "" {
		u, ok := c.UpstreamByID(id)
		if !ok {
			return ChannelUpstream{}, fmt.Errorf("%w: unknown upstream_id", ErrValidation)
		}
		return u, nil
	}
	p, _ := upstream.ParsePolicy(c.UpstreamPolicy)
	if p == upstream.PolicyRandom && len(c.Upstreams) > 0 {
		return c.Upstreams[rand.IntN(len(c.Upstreams))], nil
	}
	return c.PrimaryUpstream(), nil
}

// StreamSource is the live origin set for a stream session (merged headers, policy, revision).
func (c Channel) StreamSource() upstream.Source {
	ups := c.Upstreams
	if len(ups) == 0 {
		ups = []ChannelUpstream{{URL: c.UpstreamURL}}
	}
	items := make([]upstream.Upstream, 0, len(ups))
	fixedIdx := 0
	for i, u := range ups {
		cands := u.ResolveCandidates()
		fetch := u.URL
		if len(cands) > 0 {
			fetch = cands[0]
		}
		items = append(items, upstream.Upstream{
			ID:         u.ID,
			URL:        fetch,
			Candidates: cands,
			Headers:    c.MergedHeaders(u),
		})
		if c.FixedUpstreamID != "" && u.ID == c.FixedUpstreamID {
			fixedIdx = i
		}
	}
	policy, err := upstream.ParsePolicy(c.UpstreamPolicy)
	if err != nil {
		policy = upstream.PolicyFixed
	}
	rev := ""
	if !c.UpdatedAt.IsZero() {
		rev = c.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	return upstream.Source{
		Policy:     policy,
		Upstreams:  items,
		FixedIndex: fixedIdx,
		Transcode:  c.TranscodeEnabled,
		Revision:   rev,
	}
}

// UpstreamByID finds a row on the channel.
func (c Channel) UpstreamByID(id string) (ChannelUpstream, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return ChannelUpstream{}, false
	}
	for _, u := range c.Upstreams {
		if u.ID == id {
			return u, true
		}
	}
	return ChannelUpstream{}, false
}

// MergedHeaders returns channel defaults overlaid by the row.
func (c Channel) MergedHeaders(u ChannelUpstream) map[string]string {
	return MergeHeaders(c.Headers, u.Headers)
}

// MergeHeaders copies base then overlay (same name replaces). Empty overlay inherits.
func MergeHeaders(base, overlay map[string]string) map[string]string {
	out := normalizeHeaders(base)
	for k, v := range normalizeHeaders(overlay) {
		out[k] = v
	}
	return out
}

func validateHeaderMap(headers map[string]string) error {
	for k, v := range headers {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("%w: header names must be non-empty", ErrValidation)
		}
		if strings.ContainsAny(k, "\r\n\x00") || strings.ContainsAny(v, "\r\n\x00") {
			return fmt.Errorf("%w: header names and values must not contain CR, LF, or NUL", ErrValidation)
		}
	}
	return nil
}

// ChannelRelayInvalidate reports whether a save should retire the live session.
func ChannelRelayInvalidate(before, after Channel) bool {
	if before.TranscodeEnabled != after.TranscodeEnabled {
		return true
	}
	if before.UpstreamPolicy != after.UpstreamPolicy || before.FixedUpstreamID != after.FixedUpstreamID {
		return true
	}
	if !headerMapsEqual(before.Headers, after.Headers) {
		return true
	}
	if len(before.Upstreams) != len(after.Upstreams) {
		return true
	}
	for i := range before.Upstreams {
		if before.Upstreams[i].URL != after.Upstreams[i].URL || before.Upstreams[i].ProxyID != after.Upstreams[i].ProxyID {
			return true
		}
		if !headerMapsEqual(before.Upstreams[i].Headers, after.Upstreams[i].Headers) {
			return true
		}
	}
	return false
}

func headerMapsEqual(a, b map[string]string) bool {
	an := normalizeHeaders(a)
	bn := normalizeHeaders(b)
	if len(an) != len(bn) {
		return false
	}
	for k, v := range an {
		if bn[k] != v {
			return false
		}
	}
	return true
}

func hasChannelColumn(db *sql.DB, want string) (bool, error) {
	return hasTableColumn(db, "channels", want)
}

func (u ChannelUpstream) proxyRef() *upstream.ProxyRef {
	if u.proxy == nil || u.ProxyID == "" {
		return nil
	}
	return &upstream.ProxyRef{
		Policy:     u.proxy.Policy,
		Servers:    u.proxy.Servers,
		FixedIndex: u.proxy.FixedIndex,
	}
}

// StablePrimary is the deterministic fetch URL for this row.
func (u ChannelUpstream) StablePrimary() string {
	return upstream.StablePrimary(u.URL, u.proxyRef())
}

// ResolveCandidates is the session/test fetch URL list for this row.
func (u ChannelUpstream) ResolveCandidates() []string {
	return upstream.Resolve(u.URL, u.proxyRef())
}

func snapshotProxy(p Proxy) *proxySnapshot {
	servers := make([]string, 0, len(p.Servers))
	fixedIdx := 0
	for i, srv := range p.Servers {
		servers = append(servers, srv.URL)
		if p.FixedServerID != "" && srv.ID == p.FixedServerID {
			fixedIdx = i
		}
	}
	return &proxySnapshot{Policy: p.Policy, Servers: servers, FixedIndex: fixedIdx}
}

func nullIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}
