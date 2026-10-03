//go:build aix || android || darwin || dragonfly || freebsd || hurd || illumos || ios || linux || netbsd || openbsd || solaris

package main

import (
	"fmt"
	"os"
	"syscall"
)

// restartCurrentProcess replaces this process after the tray and all workers
// have shut down. Prefer exec to spawning on Unix: retaining the PID also
// retains the shell's foreground-job and terminal relationship, and there is
// never a window in which both watcher processes are alive.
func restartCurrentProcess() error {
	executable, arguments, err := currentRestartInvocation()
	if err != nil {
		return err
	}
	if err := syscall.Exec(executable, arguments, os.Environ()); err != nil {
		return fmt.Errorf("replace current process: %w", err)
	}
	return nil
}
