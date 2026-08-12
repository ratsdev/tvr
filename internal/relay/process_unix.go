//go:build unix

package relay

import (
	"os/exec"
	"sync"
	"syscall"
	"time"
)

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateProcessGroup sends SIGTERM to the process group and escalates to
// SIGKILL after grace unless the returned cancel function is called first.
func terminateProcessGroup(cmd *exec.Cmd, grace time.Duration) (cancelEscalation func()) {
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
