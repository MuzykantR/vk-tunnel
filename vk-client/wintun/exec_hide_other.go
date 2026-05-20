//go:build !windows

package wintun

import "os/exec"

func hideCmdWindow(cmd *exec.Cmd) {}

func hiddenCmd(name string, arg ...string) *exec.Cmd {
	return exec.Command(name, arg...)
}
