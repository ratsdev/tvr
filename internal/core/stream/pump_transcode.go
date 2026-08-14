package stream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ratsdev/tvr/internal/core/transcode"
	"github.com/ratsdev/tvr/internal/core/upstream"
)

func (s *session) pumpTranscode(ctx context.Context) error {
	up := s.upstream
	profile := s.profile
	bin := strings.TrimSpace(profile.FFmpegPath)
	if bin == "" {
		bin = "ffmpeg"
	}
	args, err := transcode.BuildArgs(profile, up)
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, args...)
	transcode.ConfigureProcessGroup(cmd)
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
			cancelEscalation = transcode.TerminateProcessGroup(cmd, 2*time.Second)
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

func annotateFFmpegErr(err error, up upstream.Upstream, stderr string) error {
	msg := redactSecrets(stderr, up)
	if msg == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, msg)
}

func redactSecrets(stderr string, up upstream.Upstream) string {
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
