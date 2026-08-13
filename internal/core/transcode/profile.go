package transcode

import (
	"strings"
	"time"
)

// Profile is the immutable global ffmpeg encoding configuration.
type Profile struct {
	FFmpegPath       string
	VideoCRF         int
	VideoPreset      string
	AudioBitrateKbps int
	MaxHeight        int
	StartupTimeout   time.Duration
}

// DefaultProfile returns the built-in encoding defaults.
func DefaultProfile() Profile {
	return Profile{
		FFmpegPath:       "ffmpeg",
		VideoCRF:         23,
		VideoPreset:      "veryfast",
		AudioBitrateKbps: 128,
		MaxHeight:        0,
		StartupTimeout:   30 * time.Second,
	}
}

// Normalize fills zeros and clamps invalid fields.
func Normalize(p Profile) Profile {
	def := DefaultProfile()
	unset := p.VideoCRF == 0 && strings.TrimSpace(p.VideoPreset) == "" && p.AudioBitrateKbps == 0 && p.StartupTimeout == 0
	if strings.TrimSpace(p.FFmpegPath) == "" {
		p.FFmpegPath = def.FFmpegPath
	} else {
		p.FFmpegPath = strings.TrimSpace(p.FFmpegPath)
	}
	if strings.TrimSpace(p.VideoPreset) == "" {
		p.VideoPreset = def.VideoPreset
	} else {
		p.VideoPreset = strings.TrimSpace(p.VideoPreset)
	}
	if unset {
		p.VideoCRF = def.VideoCRF
	} else if p.VideoCRF < 0 || p.VideoCRF > 51 {
		p.VideoCRF = def.VideoCRF
	}
	if p.AudioBitrateKbps <= 0 {
		p.AudioBitrateKbps = def.AudioBitrateKbps
	}
	if p.StartupTimeout <= 0 {
		p.StartupTimeout = def.StartupTimeout
	}
	if p.MaxHeight < 0 {
		p.MaxHeight = 0
	}
	return p
}
