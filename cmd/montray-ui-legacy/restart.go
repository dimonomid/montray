package main

import (
	"fmt"
	"os"
	"os/exec"
)

// currentRestartInvocation preserves the original arguments while using the
// resolved executable path as argv[0].
func currentRestartInvocation() (string, []string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", nil, fmt.Errorf("locate current executable: %w", err)
	}
	arguments := make([]string, 1, len(os.Args))
	arguments[0] = executable
	arguments = append(arguments, os.Args[1:]...)
	return executable, arguments, nil
}

// newRestartCommand is used by platforms without Unix process replacement.
func newRestartCommand(executable string, arguments []string) *exec.Cmd {
	command := exec.Command(executable, arguments[1:]...)
	command.Args = append([]string(nil), arguments...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command
}
