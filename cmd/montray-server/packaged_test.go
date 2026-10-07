//go:build packaged

package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestPackagedSetupIsDisabled(t *testing.T) {
	command := newRootCommand()
	command.SetArgs([]string{"setup", "create-config", "--reinstall"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "in-app setup is disabled") {
		t.Fatalf("setup error = %v, want package manager message", err)
	}
}

func TestPackagedVersionShowsBuildMode(t *testing.T) {
	command := newRootCommand()
	output := &bytes.Buffer{}
	command.SetOut(output)
	command.SetArgs([]string{"--version"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Built by: unknown (for packaging)\n") {
		t.Fatalf("version output %q does not show packaging build mode", output.String())
	}
}

func TestPackagedConfigReadErrorDoesNotSuggestSetup(t *testing.T) {
	err := montrayServerConfigReadError(defaultMontrayServerConfig, os.ErrNotExist)
	if strings.Contains(err.Error(), "setup") {
		t.Fatalf("montrayServerConfigReadError() = %v, want no setup guidance", err)
	}
}
