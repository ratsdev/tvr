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
	"github.com/jqjiang/tvr/internal/core/upstream"
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
		return "", fmt.Errorf("%w: upstream_policy must be fixed, random, or fallback", ErrValidation)
	}
	return p, nil
}

func resolveChannelSpec(in ChannelInput, existing *Channel) (channelSpec, error) {
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

	seenURL := map[string]struct{}{}
	usedID := map[string]struct{}{}
	out := make([]ChannelUpstream, 0, len(raw))
	for _, u := range raw {
		item, err := normalizeChannelUpstream(u, usedID)
		if err != nil {
			return channelSpec{}, err
		}
		if _, dup := seenURL[item.URL]; dup {
			return channelSpec{}, fmt.Errorf("%w: duplicate upstream url", ErrValidation)
		}
		seenURL[item.URL] = struct{}{}
		out = append(out, item)
	}

	fixedID := strings.TrimSpace(in.FixedUpstreamID)
	if fixedID == "" && existing != nil {
		fixedID = strings.TrimSpace(existing.FixedUpstreamID)
	}
	if _, ok := usedID[fixedID]; !ok {
		fixedID = out[0].ID
	}
	primary := out[0].URL
	if policy == UpstreamPolicyFixed {
		for _, u := range out {
			if u.ID == fixedID {
				primary = u.URL
				break
			}
		}
	}
	return channelSpec{upstreams: out, policy: policy, fixedID: fixedID, primaryURL: primary}, nil
}

func normalizeChannelUpstream(u ChannelUpstream, usedID map[string]struct{}) (ChannelUpstream, error) {
	raw := strings.TrimSpace(u.URL)
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ChannelUpstream{}, fmt.Errorf("%w: upstream url must be http(s)", ErrValidation)
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
	out := ChannelUpstream{ID: id, URL: raw}
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
INSERT INTO channel_upstreams (id, channel_id, url, headers_json, sort_order)
VALUES (?, ?, ?, ?, ?)`,
			u.ID, channelID, u.URL, string(headersJSON), i); err != nil {
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
SELECT id, channel_id, url, headers_json, sort_order
FROM channel_upstreams
ORDER BY channel_id ASC, sort_order ASC, id ASC`)
	if err != nil {
		return err
	}
	defer upRows.Close()
	byChannel := map[string][]ChannelUpstream{}
	for upRows.Next() {
		var (
			item        ChannelUpstream
			channelID   string
			headersJSON string
			sortOrder   int
		)
		if err := upRows.Scan(&item.ID, &channelID, &item.URL, &headersJSON, &sortOrder); err != nil {
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
		byChannel[channelID] = append(byChannel[channelID], item)
	}
	if err := upRows.Err(); err != nil {
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
		items = append(items, upstream.Upstream{
			ID:      u.ID,
			URL:     u.URL,
			Headers: c.MergedHeaders(u),
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
		if before.Upstreams[i].URL != after.Upstreams[i].URL {
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
	rows, err := db.Query(`PRAGMA table_info(channels)`)
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
