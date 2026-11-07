//go:build unix

package proc

import (
	"os/exec"
	"syscall"
)

// prepare puts the command in its own process group. On cancel it kills the
// whole group, so ssh sessions and forked workers end with the step; with
// graceful set it sends SIGTERM to the group instead, and the process is killed
// only when WaitDelay (the grace period) has passed.
func prepare(cmd *exec.Cmd, graceful bool) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	sig := syscall.SIGKILL
	if graceful {
		sig = syscall.SIGTERM
	}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, sig) }
}
