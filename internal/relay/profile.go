package relay

import "time"

// TranscodeProfile is the immutable global ffmpeg encoding configuration.
type TranscodeProfile struct {
	FFmpegPath            string
	VideoCRF              int
	VideoPreset           string
	AudioBitrateKbps      int
	MaxHeight             int
	StartupTimeout        time.Duration
}

// DefaultTranscodeProfile returns the built-in encoding defaults.
func DefaultTranscodeProfile() TranscodeProfile {
	return TranscodeProfile{
		FFmpegPath:       "ffmpeg",
		VideoCRF:         23,
		VideoPreset:      "veryfast",
		AudioBitrateKbps: 128,
		MaxHeight:        0,
		StartupTimeout:   30 * time.Second,
	}
}
