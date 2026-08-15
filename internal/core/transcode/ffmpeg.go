package transcode

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ratsdev/tvr/internal/core/upstream"
)

const defaultUserAgent = "tvr/1.0"

// BuildArgs returns ffmpeg CLI args that remux/transcode up into MPEG-TS on stdout.
func BuildArgs(profile Profile, up upstream.Upstream) ([]string, error) {
	if strings.TrimSpace(up.URL) == "" {
		return nil, fmt.Errorf("upstream url is required")
	}
	ua := defaultUserAgent
	headers := make(map[string]string, len(up.Headers))
	for k, v := range up.Headers {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if strings.ContainsAny(k, "\r\n\x00") || strings.ContainsAny(v, "\r\n\x00") {
			return nil, fmt.Errorf("invalid header %q", k)
		}
		if strings.EqualFold(k, "User-Agent") {
			if strings.TrimSpace(v) != "" {
				ua = v
			}
			continue
		}
		headers[k] = v
	}
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var headerBlock strings.Builder
	for _, k := range keys {
		headerBlock.WriteString(k)
		headerBlock.WriteString(": ")
		headerBlock.WriteString(headers[k])
		headerBlock.WriteString("\r\n")
	}

	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-nostdin",
		"-protocol_whitelist", "http,https,tcp,tls,crypto,httpproxy",
		"-user_agent", ua,
		"-fflags", "+genpts",
	}
	if headerBlock.Len() > 0 {
		args = append(args, "-headers", headerBlock.String())
	}
	args = append(args,
		"-i", up.URL,
		"-map", "0:v:0",
		"-map", "0:a:0?",
		"-sn",
		"-dn",
		"-vsync", "cfr",
		"-c:v", "libx264",
		"-preset", profile.VideoPreset,
		"-profile:v", "main",
		"-bf", "0",
		"-flags", "+cgop",
		"-x264-params", "repeat-headers=1:aud=1",
		"-crf", fmt.Sprintf("%d", profile.VideoCRF),
		"-pix_fmt", "yuv420p",
		"-g", "50",
		"-keyint_min", "25",
		"-sc_threshold", "0",
		"-bsf:v", "dump_extra",
		"-c:a", "aac",
		"-b:a", fmt.Sprintf("%dk", profile.AudioBitrateKbps),
		"-ar", "48000",
		"-ac", "2",
		"-vf", scaleFilter(profile.MaxHeight),
		"-mpegts_flags", "+resend_headers",
		"-muxdelay", "0.1",
		"-muxpreload", "0.1",
		"-pcr_period", "20",
		"-avoid_negative_ts", "make_zero",
		"-f", "mpegts",
		"pipe:1",
	)
	return args, nil
}

func scaleFilter(maxHeight int) string {
	if maxHeight > 0 {
		return fmt.Sprintf("scale=w=-2:h='trunc(min(ih\\,%d)/2)*2'", maxHeight)
	}
	return "scale=trunc(iw/2)*2:trunc(ih/2)*2"
}
