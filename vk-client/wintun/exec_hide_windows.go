//go:build windows

package wintun

import (
	"os/exec"
	"syscall"
)

func hideCmdWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
}

func hiddenCmd(name string, arg ...string) *exec.Cmd {
	cmd := exec.Command(name, arg...)
	hideCmdWindow(cmd)
	return cmd
}
