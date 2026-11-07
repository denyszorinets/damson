//go:build !unix

package ansible

import "os/exec"

func prepare(*exec.Cmd) {}
