package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

var allowedVideoPresets = map[string]struct{}{
	"ultrafast": {},
	"superfast": {},
	"veryfast":  {},
	"faster":    {},
	"fast":      {},
	"medium":    {},
	"slow":      {},
	"slower":    {},
	"veryslow":  {},
}

// GetTranscodeSettings returns the singleton transcoder profile.
func (s *Store) GetTranscodeSettings(ctx context.Context) (TranscodeSettings, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT video_crf, video_preset, audio_bitrate_kbps, max_height, startup_timeout_seconds
FROM app_settings WHERE id = 1`)
	var st TranscodeSettings
	err := row.Scan(&st.VideoCRF, &st.VideoPreset, &st.AudioBitrateKbps, &st.MaxHeight, &st.StartupTimeoutSeconds)
	if errors.Is(err, sql.ErrNoRows) {
		return TranscodeSettings{}, fmt.Errorf("app_settings singleton missing")
	}
	if err != nil {
		return TranscodeSettings{}, err
	}
	return st, nil
}

// UpdateTranscodeSettings replaces the singleton transcoder profile.
func (s *Store) UpdateTranscodeSettings(ctx context.Context, in TranscodeSettings) (TranscodeSettings, error) {
	saved, _, err := s.UpdateAppSettings(ctx, in, nil)
	return saved, err
}

// UpdateAppSettings writes transcode and optional brand in one statement.
func (s *Store) UpdateAppSettings(ctx context.Context, transcode TranscodeSettings, brand *BrandSettings) (TranscodeSettings, BrandSettings, error) {
	if err := ValidateTranscodeSettings(transcode); err != nil {
		return TranscodeSettings{}, BrandSettings{}, err
	}
	var brandRow *BrandSettings
	if brand != nil {
		st, err := prepareBrandSettings(*brand)
		if err != nil {
			return TranscodeSettings{}, BrandSettings{}, err
		}
		brandRow = &st
	}

	var (
		res sql.Result
		err error
	)
	if brandRow != nil {
		res, err = s.db.ExecContext(ctx, `
UPDATE app_settings
SET video_crf = ?, video_preset = ?, audio_bitrate_kbps = ?, max_height = ?, startup_timeout_seconds = ?,
    brand_icon = ?, brand_title = ?
WHERE id = 1`,
			transcode.VideoCRF,
			strings.TrimSpace(transcode.VideoPreset),
			transcode.AudioBitrateKbps,
			transcode.MaxHeight,
			transcode.StartupTimeoutSeconds,
			brandRow.Icon,
			brandRow.Title,
		)
	} else {
		res, err = s.db.ExecContext(ctx, `
UPDATE app_settings
SET video_crf = ?, video_preset = ?, audio_bitrate_kbps = ?, max_height = ?, startup_timeout_seconds = ?
WHERE id = 1`,
			transcode.VideoCRF,
			strings.TrimSpace(transcode.VideoPreset),
			transcode.AudioBitrateKbps,
			transcode.MaxHeight,
			transcode.StartupTimeoutSeconds,
		)
	}
	if err != nil {
		return TranscodeSettings{}, BrandSettings{}, err
	}
	if err := requireAppSettingsRow(res); err != nil {
		return TranscodeSettings{}, BrandSettings{}, err
	}
	saved, err := s.GetTranscodeSettings(ctx)
	if err != nil {
		return TranscodeSettings{}, BrandSettings{}, err
	}
	got, err := s.GetBrandSettings(ctx)
	if err != nil {
		return TranscodeSettings{}, BrandSettings{}, err
	}
	return saved, got, nil
}

// ValidateTranscodeSettings checks editable transcoder fields.
func ValidateTranscodeSettings(in TranscodeSettings) error {
	if in.VideoCRF < 0 || in.VideoCRF > 51 {
		return fmt.Errorf("%w: video_crf must be between 0 and 51", ErrValidation)
	}
	preset := strings.TrimSpace(in.VideoPreset)
	if _, ok := allowedVideoPresets[preset]; !ok {
		return fmt.Errorf("%w: video_preset is not supported", ErrValidation)
	}
	if in.AudioBitrateKbps < 32 || in.AudioBitrateKbps > 512 {
		return fmt.Errorf("%w: audio_bitrate_kbps must be between 32 and 512", ErrValidation)
	}
	if in.MaxHeight < 0 || in.MaxHeight > 4320 {
		return fmt.Errorf("%w: max_height must be between 0 and 4320", ErrValidation)
	}
	if in.MaxHeight > 0 && in.MaxHeight%2 != 0 {
		return fmt.Errorf("%w: max_height must be even", ErrValidation)
	}
	if in.StartupTimeoutSeconds < 1 || in.StartupTimeoutSeconds > 300 {
		return fmt.Errorf("%w: startup_timeout_seconds must be between 1 and 300", ErrValidation)
	}
	return nil
}

// DefaultTranscodeSettings returns the built-in profile defaults.
func DefaultTranscodeSettings() TranscodeSettings {
	return TranscodeSettings{
		VideoCRF:              23,
		VideoPreset:           "veryfast",
		AudioBitrateKbps:      128,
		MaxHeight:             0,
		StartupTimeoutSeconds: 30,
	}
}

// GetBrandSettings returns the singleton nav brand.
func (s *Store) GetBrandSettings(ctx context.Context) (BrandSettings, error) {
	row := s.db.QueryRowContext(ctx, `SELECT brand_icon, brand_title FROM app_settings WHERE id = 1`)
	var st BrandSettings
	err := row.Scan(&st.Icon, &st.Title)
	if errors.Is(err, sql.ErrNoRows) {
		return BrandSettings{}, fmt.Errorf("app_settings singleton missing")
	}
	if err != nil {
		return BrandSettings{}, err
	}
	return resolveBrandSettings(st), nil
}

// UpdateBrandSettings replaces the singleton nav brand.
func (s *Store) UpdateBrandSettings(ctx context.Context, in BrandSettings) (BrandSettings, error) {
	st, err := prepareBrandSettings(in)
	if err != nil {
		return BrandSettings{}, err
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE app_settings SET brand_icon = ?, brand_title = ? WHERE id = 1`,
		st.Icon, st.Title)
	if err != nil {
		return BrandSettings{}, err
	}
	if err := requireAppSettingsRow(res); err != nil {
		return BrandSettings{}, err
	}
	return s.GetBrandSettings(ctx)
}

func requireAppSettingsRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("app_settings singleton missing")
	}
	return nil
}

// DefaultBrandSettings returns the built-in nav brand.
func DefaultBrandSettings() BrandSettings {
	return BrandSettings{Icon: DefaultBrandIcon, Title: DefaultBrandTitle}
}

// ValidateBrandSettings checks editable nav brand fields.
func ValidateBrandSettings(in BrandSettings) error {
	_, err := prepareBrandSettings(in)
	return err
}

const (
	maxBrandIconURLLen     = 2048
	maxBrandTitleRunes     = 64
	legacyDefaultBrandIcon = "/assets/assets/brand.svg"
)

// IsDefaultBrandIcon reports a missing or built-in nav icon.
func IsDefaultBrandIcon(icon string) bool {
	switch strings.TrimSpace(icon) {
	case "", DefaultBrandIcon, legacyDefaultBrandIcon:
		return true
	default:
		return false
	}
}

func normalizeBrandSettings(in BrandSettings) BrandSettings {
	icon := strings.TrimSpace(in.Icon)
	title := strings.TrimSpace(in.Title)
	if IsDefaultBrandIcon(icon) {
		icon = ""
	}
	if title == "" {
		title = DefaultBrandTitle
	}
	return BrandSettings{Icon: icon, Title: title}
}

func resolveBrandSettings(in BrandSettings) BrandSettings {
	st := normalizeBrandSettings(in)
	if st.Icon == "" || !isBrandIconURL(st.Icon) {
		st.Icon = DefaultBrandIcon
	}
	return st
}

func prepareBrandSettings(in BrandSettings) (BrandSettings, error) {
	st := normalizeBrandSettings(in)
	if st.Icon != "" {
		if len(st.Icon) > maxBrandIconURLLen {
			return BrandSettings{}, fmt.Errorf("%w: brand icon URL is too long", ErrValidation)
		}
		if !isBrandIconURL(st.Icon) {
			return BrandSettings{}, fmt.Errorf("%w: brand icon must be an image URL", ErrValidation)
		}
	}
	if utf8.RuneCountInString(st.Title) > maxBrandTitleRunes {
		return BrandSettings{}, fmt.Errorf("%w: brand title must be at most %d characters", ErrValidation, maxBrandTitleRunes)
	}
	if strings.ContainsAny(st.Title, "\r\n") {
		return BrandSettings{}, fmt.Errorf("%w: brand title cannot contain newlines", ErrValidation)
	}
	return st, nil
}

func isBrandIconURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || strings.HasPrefix(s, "//") {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u.Host != ""
	case "":
		return u.Path == "/brand-icon"
	default:
		return false
	}
}
