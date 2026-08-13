//go:build unix

package transcode

import (
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// ConfigureProcessGroup puts ffmpeg in its own process group so teardown can signal the whole tree.
func ConfigureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// TerminateProcessGroup sends SIGTERM to the process group and escalates to
// SIGKILL after grace unless the returned cancel function is called first.
func TerminateProcessGroup(cmd *exec.Cmd, grace time.Duration) (cancelEscalation func()) {
	noop := func() {}
	if cmd == nil || cmd.Process == nil {
		return noop
	}
	pid := cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGTERM)

	stop := make(chan struct{})
	var once sync.Once
	go func() {
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-stop:
			return
		case <-timer.C:
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	}()
	return func() {
		once.Do(func() { close(stop) })
	}
}
