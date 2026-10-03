//go:build unix

package wsclient

import (
	"os/exec"
	"syscall"
)

// isolateTunnelCommandFromTerminalSignals keeps terminal-generated signals,
// such as Ctrl+C, from reaching the tunnel before Montray can perform its
// normal teardown.
func isolateTunnelCommandFromTerminalSignals(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
