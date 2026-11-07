//go:build unix

package proc

import (
	"os/exec"
	"syscall"
)

// prepare puts the command in its own process group and kills the whole group
// on cancel, so ssh sessions and forked workers end with the step.
func prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
