package store

import (
	"time"

	"github.com/jqjiang/tvr/internal/core/upstream"
)

const (
	UpstreamPolicyFixed    = upstream.PolicyFixed
	UpstreamPolicyRandom   = upstream.PolicyRandom
	UpstreamPolicyFallback = upstream.PolicyFallback
)

// ChannelUpstream is one HTTP(S) source URL on a channel.
type ChannelUpstream struct {
	ID      string            `json:"id"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Channel is a reusable global IPTV upstream definition.
type Channel struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	LogoURL          string            `json:"logo_url"`
	UpstreamURL      string            `json:"upstream_url"`
	Upstreams        []ChannelUpstream `json:"upstreams"`
	UpstreamPolicy   string            `json:"upstream_policy"`
	FixedUpstreamID  string            `json:"fixed_upstream_id,omitempty"`
	Headers          map[string]string `json:"headers"`
	TranscodeEnabled bool              `json:"transcode_enabled"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
	RelayCount       int               `json:"relay_count"`
	RelaySlugs       []string          `json:"relay_slugs"`
}

// ChannelInput is the writable subset used by create/update APIs.
type ChannelInput struct {
	Name             string            `json:"name"`
	LogoURL          string            `json:"logo_url"`
	UpstreamURL      string            `json:"upstream_url"`
	Upstreams        []ChannelUpstream `json:"upstreams"`
	UpstreamPolicy   string            `json:"upstream_policy"`
	FixedUpstreamID  string            `json:"fixed_upstream_id"`
	Headers          map[string]string `json:"headers"`
	TranscodeEnabled *bool             `json:"transcode_enabled,omitempty"`
}

// TranscodeSettings is the singleton editable transcoder profile.
type TranscodeSettings struct {
	VideoCRF              int    `json:"video_crf"`
	VideoPreset           string `json:"video_preset"`
	AudioBitrateKbps      int    `json:"audio_bitrate_kbps"`
	MaxHeight             int    `json:"max_height"`
	StartupTimeoutSeconds int    `json:"startup_timeout_seconds"`
}

// EPGSource is a remote XMLTV feed.
type EPGSource struct {
	ID              int64         `json:"id"`
	Name            string        `json:"name"`
	URL             string        `json:"url"`
	Enabled         bool          `json:"enabled"`
	RefreshInterval time.Duration `json:"refresh_interval"`
	LastRefreshAt   *time.Time    `json:"last_refresh_at,omitempty"`
	LastError       string        `json:"last_error,omitempty"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
}

// EPGSourceInput is the writable subset used by create/update APIs.
type EPGSourceInput struct {
	Name            string `json:"name"`
	URL             string `json:"url"`
	Enabled         *bool  `json:"enabled"`
	RefreshInterval string `json:"refresh_interval"`
}

// Relay is a named lineup that generates a playlist and EPG.
type Relay struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RelayInput creates or updates a relay.
type RelayInput struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// RelayGroup is an ordered group inside a relay.
type RelayGroup struct {
	ID        int64  `json:"id"`
	RelayID   int64  `json:"relay_id"`
	Name      string `json:"name"`
	SortOrder int    `json:"sort_order"`
}

// RelayMembership binds a global channel into a relay group.
type RelayMembership struct {
	ID          int64  `json:"id"`
	RelayID     int64  `json:"relay_id"`
	GroupID     int64  `json:"group_id"`
	ChannelID   string `json:"channel_id"`
	Number      int    `json:"number"`
	EPGSourceID *int64 `json:"epg_source_id,omitempty"`
	TvgID       string `json:"tvg_id"`
	SortOrder   int    `json:"sort_order"`

	// Joined fields for API responses.
	ChannelName string `json:"channel_name,omitempty"`
	LogoURL     string `json:"logo_url,omitempty"`
	UpstreamURL string `json:"upstream_url,omitempty"`
	GroupName   string `json:"group_name,omitempty"`
}

// MembershipInput creates or updates a membership.
type MembershipInput struct {
	ChannelID   string `json:"channel_id"`
	GroupID     int64  `json:"group_id"`
	Number      int    `json:"number"`
	EPGSourceID *int64 `json:"epg_source_id"`
	TvgID       string `json:"tvg_id"`
	SortOrder   *int   `json:"sort_order"`
}

// RelayDetail is a full relay editor payload.
type RelayDetail struct {
	Relay
	Groups      []RelayGroup      `json:"groups"`
	Memberships []RelayMembership `json:"memberships"`
}

// RelayLayout is used for atomic drag/drop persistence.
type RelayLayout struct {
	Groups []RelayLayoutGroup `json:"groups"`
}

// RelayLayoutGroup describes one group and its ordered memberships.
type RelayLayoutGroup struct {
	ID            int64   `json:"id"`
	Name          string  `json:"name"`
	MembershipIDs []int64 `json:"membership_ids"`
}

// LineupEntry is one playlist row for a relay.
type LineupEntry struct {
	MembershipID int64
	ChannelID    string
	Name         string
	LogoURL      string
	UpstreamURL  string
	Headers      map[string]string
	Number       int
	TvgID        string
	GroupTitle   string
	EPGSourceID  *int64
}

// RelayEPGMapping is a membership EPG binding used for filtering.
type RelayEPGMapping struct {
	RelayID     int64
	RelaySlug   string
	EPGSourceID int64
	TvgID       string
}
