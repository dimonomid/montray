package wsclient

import (
	"os/exec"
	"syscall"
)

// isolateTunnelCommandFromTerminalSignals keeps console control events, such
// as Ctrl+C, from reaching the tunnel before Montray can perform its
// normal teardown.
func isolateTunnelCommandFromTerminalSignals(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
