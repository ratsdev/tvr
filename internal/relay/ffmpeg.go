package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

const defaultUserAgent = "tvr/1.0"

func buildFFmpegArgs(profile TranscodeProfile, up Upstream) ([]string, error) {
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
		"-crf", fmt.Sprintf("%d", profile.VideoCRF),
		"-pix_fmt", "yuv420p",
		"-g", "50",
		"-keyint_min", "25",
		"-sc_threshold", "0",
		"-c:a", "aac",
		"-b:a", fmt.Sprintf("%dk", profile.AudioBitrateKbps),
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

func (s *session) pumpTranscode(ctx context.Context) error {
	up := s.upstream
	profile := s.profile
	bin := strings.TrimSpace(profile.FFmpegPath)
	if bin == "" {
		bin = "ffmpeg"
	}
	args, err := buildFFmpegArgs(profile, up)
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, args...)
	configureProcessGroup(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}

	stderrBuf := &ringBuffer{max: 4 << 10}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(stderrBuf, stderr)
	}()

	cancelEscalation := func() {}
	killOnce := sync.Once{}
	kill := func() {
		killOnce.Do(func() {
			cancelEscalation = terminateProcessGroup(cmd, 2*time.Second)
		})
	}
	watchDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			kill()
		case <-s.stopCh:
			kill()
		case <-watchDone:
		}
	}()

	s.setState("connecting", "")
	copyErr := s.copyMPEGTS(ctx, stdout, mpegTSCopyOptions{})
	kill()
	close(watchDone)
	waitErr := cmd.Wait()
	cancelEscalation()
	wg.Wait()

	if s.stopped.Load() || errors.Is(ctx.Err(), context.Canceled) {
		return nil
	}
	if copyErr != nil && !errors.Is(copyErr, io.EOF) {
		return annotateFFmpegErr(copyErr, up, stderrBuf.String())
	}
	if waitErr != nil {
		return annotateFFmpegErr(waitErr, up, stderrBuf.String())
	}
	if !s.everReady.Load() {
		return annotateFFmpegErr(fmt.Errorf("ffmpeg produced no mpeg-ts"), up, stderrBuf.String())
	}
	return fmt.Errorf("ffmpeg exited")
}

func annotateFFmpegErr(err error, up Upstream, stderr string) error {
	msg := redactSecrets(stderr, up)
	if msg == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, msg)
}

func redactSecrets(stderr string, up Upstream) string {
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		return ""
	}
	if up.URL != "" {
		msg = strings.ReplaceAll(msg, up.URL, "<url>")
	}
	for _, v := range up.Headers {
		if strings.TrimSpace(v) != "" {
			msg = strings.ReplaceAll(msg, v, "<redacted>")
		}
	}
	if len(msg) > 512 {
		msg = msg[len(msg)-512:]
	}
	return msg
}

type ringBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = r.buf.Write(p)
	if r.buf.Len() > r.max {
		data := r.buf.Bytes()
		r.buf.Reset()
		_, _ = r.buf.Write(data[len(data)-r.max:])
	}
	return len(p), nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}
