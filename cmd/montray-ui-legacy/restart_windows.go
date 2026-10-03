package main

import "fmt"

// Windows has no exec equivalent, so start a replacement after teardown and
// let the current process return normally.
func restartCurrentProcess() error {
	executable, arguments, err := currentRestartInvocation()
	if err != nil {
		return err
	}
	if err := newRestartCommand(executable, arguments).Start(); err != nil {
		return fmt.Errorf("start replacement process: %w", err)
	}
	return nil
}
