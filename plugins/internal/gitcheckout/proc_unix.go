//go:build unix

package gitcheckout

import (
	"os/exec"
	"syscall"
)

// prepare puts git in its own process group and kills the whole group on
// cancel, so ssh and credential helpers do not outlive a cancelled step.
func prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
