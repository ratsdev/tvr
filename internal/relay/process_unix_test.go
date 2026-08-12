//go:build unix

package relay

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestTerminateProcessGroupCancelSkipsSIGKILL(t *testing.T) {
	// Ignore SIGTERM so only a later SIGKILL would reap the process.
	cmd := exec.Command("sh", "-c", "trap '' TERM; sleep 30")
	configureProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	})

	cancel := terminateProcessGroup(cmd, 80*time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	if err := syscall.Kill(cmd.Process.Pid, 0); err != nil {
		t.Fatalf("process should still be alive after SIGTERM: %v", err)
	}
	cancel()
	time.Sleep(150 * time.Millisecond)
	if err := syscall.Kill(cmd.Process.Pid, 0); err != nil {
		t.Fatalf("process was killed after cancelEscalation; SIGKILL raced: %v", err)
	}
}
