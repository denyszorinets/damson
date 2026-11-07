//go:build !unix

package gitcheckout

import "os/exec"

func prepare(*exec.Cmd) {}
