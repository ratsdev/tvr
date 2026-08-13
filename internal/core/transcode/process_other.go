//go:build !unix

package transcode

import (
	"os/exec"
	"sync"
	"time"
)

func ConfigureProcessGroup(cmd *exec.Cmd) {}

func TerminateProcessGroup(cmd *exec.Cmd, grace time.Duration) (cancelEscalation func()) {
	noop := func() {}
	if cmd == nil || cmd.Process == nil {
		return noop
	}
	_ = cmd.Process.Kill()

	stop := make(chan struct{})
	var once sync.Once
	go func() {
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-stop:
			return
		case <-timer.C:
			_ = cmd.Process.Kill()
		}
	}()
	return func() {
		once.Do(func() { close(stop) })
	}
}
