//go:build !unix

package proc

import "os/exec"

func prepare(*exec.Cmd) {}
