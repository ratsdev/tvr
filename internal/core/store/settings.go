package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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
	if err := ValidateTranscodeSettings(in); err != nil {
		return TranscodeSettings{}, err
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE app_settings
SET video_crf = ?, video_preset = ?, audio_bitrate_kbps = ?, max_height = ?, startup_timeout_seconds = ?
WHERE id = 1`,
		in.VideoCRF,
		strings.TrimSpace(in.VideoPreset),
		in.AudioBitrateKbps,
		in.MaxHeight,
		in.StartupTimeoutSeconds,
	)
	if err != nil {
		return TranscodeSettings{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return TranscodeSettings{}, err
	}
	if n == 0 {
		return TranscodeSettings{}, fmt.Errorf("app_settings singleton missing")
	}
	return s.GetTranscodeSettings(ctx)
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
