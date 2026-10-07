//go:build !packaged

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigInitCreatesConfigWithoutOverwritingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "etc", "montray.yml")
	command := newRootCommand()
	output := &bytes.Buffer{}
	command.SetOut(output)
	command.SetErr(output)
	command.SetArgs([]string{"setup", "create-config", "--config", path})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Created configuration") {
		t.Fatalf("unexpected command output: %q", output.String())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), string(mustSetupAsset("assets/setup/montray-server.yml")); got != want {
		t.Fatalf("config contents = %q, want %q", got, want)
	}
	if _, err := loadConfig(path); err != nil {
		t.Fatalf("generated config is invalid: %v", err)
	}

	command = newRootCommand()
	command.SetOut(output)
	command.SetErr(output)
	command.SetArgs([]string{"setup", "create-config", "--config", path})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), string(mustSetupAsset("assets/setup/montray-server.yml")); got != want {
		t.Fatalf("config was overwritten: got %q, want %q", got, want)
	}
}

func TestRunnableCommandsRejectPositionalArguments(t *testing.T) {
	for _, args := range [][]string{
		{"unexpected"},
		{"setup", "unexpected"},
		{"setup", "create-config", "unexpected"},
		{"setup", "create-user", "unexpected"},
		{"setup", "install-service", "unexpected"},
		{"setup", "migrate-from-salmon", "unexpected"},
	} {
		command := newRootCommand()
		command.SetArgs(args)
		if err := command.Execute(); err == nil {
			t.Errorf("command %q accepted an unexpected positional argument", args)
		}
	}
}

func TestSetupOperationsDoNotPolluteTopLevelCommands(t *testing.T) {
	command := newRootCommand()
	commands := command.Commands()
	if len(commands) != 1 || commands[0].Name() != "setup" {
		t.Fatalf("top-level commands = %v, want only setup", commands)
	}
	if !strings.Contains(commands[0].Long, "Perform the complete setup") {
		t.Fatalf("setup long help = %q, want complete-setup description", commands[0].Long)
	}
	if flag := commands[0].PersistentFlags().Lookup("reinstall"); flag == nil || flag.DefValue != "false" {
		t.Fatalf("setup --reinstall flag = %#v, want default false", flag)
	}

	setupCommands := map[string]bool{}
	for _, subcommand := range commands[0].Commands() {
		setupCommands[subcommand.Name()] = true
	}
	for _, want := range []string{"create-config", "create-user", "install-service", "migrate-from-salmon"} {
		if !setupCommands[want] {
			t.Errorf("setup subcommands = %v, missing %q", setupCommands, want)
		}
	}
	for _, subcommand := range commands[0].Commands() {
		if subcommand.Name() == "install-service" && !strings.Contains(subcommand.Short, "Install the executable and systemd service") {
			t.Errorf("install-service description = %q, want executable and service installation", subcommand.Short)
		}
	}
}

func TestMontrayServerConfigReadErrorSuggestsSetupForDefaultConfig(t *testing.T) {
	err := montrayServerConfigReadError(defaultMontrayServerConfig, os.ErrNotExist)
	for _, want := range []string{
		"Hint: Run the following command to create the default configuration, install the service, and start it:\n\n    sudo " + os.Args[0] + " setup",
		"To create only the default configuration without installing the service, run:\n\n    sudo " + os.Args[0] + " setup create-config",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("montrayServerConfigReadError() = %v, want guidance %q", err, want)
		}
	}
}
