//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// hideConsoleWindow prevents a console window from flashing on Windows
// when running external commands like tasklist.
func hideConsoleWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
	}
}
