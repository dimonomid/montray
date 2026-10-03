package main

import (
	"bytes"
	"errors"
	"io/ioutil"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dimonomid/montray"
	"github.com/dimonomid/montray/internal/setup"
	"github.com/dimonomid/montray/logs"
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

	data, err := ioutil.ReadFile(path)
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
	data, err = ioutil.ReadFile(path)
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
	for _, want := range []string{"create-config", "create-user", "install-service"} {
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

func TestMontrayServerSetupRootErrorProvidesSudoGuidance(t *testing.T) {
	if err := montrayServerSetupRootError(0, "bin/montray-server setup"); err != nil {
		t.Fatalf("root setup error = %v, want nil", err)
	}
	err := montrayServerSetupRootError(1000, "bin/montray-server setup --reinstall")
	if err == nil {
		t.Fatal("non-root setup was accepted")
	}
	for _, want := range []string{
		"requires root privileges",
		"Hint: Rerun it with sudo:",
		"sudo bin/montray-server setup --reinstall",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("non-root setup error = %q, want %q", err, want)
		}
	}
}

func TestRestartMontrayServerServiceStartsServiceAndReportsSuccess(t *testing.T) {
	output := &bytes.Buffer{}
	var call []string
	run := func(name string, args ...string) error {
		call = append([]string{name}, args...)
		return nil
	}
	if err := restartMontrayServerServiceWith(output, run); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(call, " "), "systemctl restart montray-server.service"; got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
	if !strings.Contains(output.String(), "configured, installed, and running") {
		t.Fatalf("output = %q, want running-service confirmation", output.String())
	}
}

func TestRestartMontrayServerServiceFailureSuggestsDiagnostics(t *testing.T) {
	err := restartMontrayServerServiceWith(&bytes.Buffer{}, func(string, ...string) error {
		return errors.New("service failed")
	})
	if err == nil {
		t.Fatal("restart failure was ignored")
	}
	for _, want := range []string{"service failed", "systemctl status montray-server.service", "journalctl --no-pager -u montray-server.service -n 50"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("restart error = %q, want %q", err, want)
		}
	}
}

func TestCreateMontrayUserInstallsSysusersConfigurationAndCreatesAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sysusers.d", "montray.conf")
	output := &bytes.Buffer{}
	var calls [][]string
	run := func(name string, args ...string) error {
		if !strings.Contains(output.String(), "Created systemd sysusers configuration") {
			t.Fatal("sysusers command ran before file creation was reported")
		}
		calls = append(calls, append([]string{name}, args...))
		return nil
	}

	if err := createMontrayUserAt(output, path, run); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), string(mustSetupAsset("assets/setup/montray-server.sysusers")); got != want {
		t.Fatalf("sysusers configuration = %q, want %q", got, want)
	}
	if got, want := len(calls), 1; got != want {
		t.Fatalf("command count = %d, want %d", got, want)
	}
	if got, want := strings.Join(calls[0], " "), "systemd-sysusers "+path; got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
	if !strings.Contains(output.String(), "Created systemd sysusers configuration") {
		t.Fatalf("unexpected command output: %q", output.String())
	}
}

func TestInstallMontrayServerExecutableCopiesFromNonSystemDirectory(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "Downloads", "montray")
	destination := filepath.Join(directory, "usr", "local", "bin", "montray")
	if err := os.MkdirAll(filepath.Dir(source), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("montray binary"), 0755); err != nil {
		t.Fatal(err)
	}
	output := &bytes.Buffer{}

	executable, preserved, err := installMontrayServerExecutableAt(output, source, destination, false)
	if err != nil {
		t.Fatal(err)
	}
	if executable != destination {
		t.Fatalf("service executable = %q, want %q", executable, destination)
	}
	if preserved {
		t.Fatal("new executable was reported as preserved")
	}
	if !strings.Contains(output.String(), "Installed executable at "+destination) {
		t.Fatalf("output = %q, want installation report", output.String())
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "montray binary"; got != want {
		t.Fatalf("installed executable = %q, want %q", got, want)
	}
}

func TestInstallMontrayServerExecutableUsesSystemLocationInPlace(t *testing.T) {
	for _, source := range []string{
		"/bin/montray-server",
		"/usr/bin/montray-server",
		"/usr/local/bin/montray-server",
		"/opt/montray/bin/montray-server",
		"/nix/store/hash-montray/bin/montray-server",
		"/snap/montray/current/bin/montray-server",
	} {
		t.Run(source, func(t *testing.T) {
			output := &bytes.Buffer{}
			executable, preserved, err := installMontrayServerExecutableAt(output, source, filepath.Join(t.TempDir(), "montray"), false)
			if err != nil {
				t.Fatal(err)
			}
			if executable != source {
				t.Fatalf("service executable = %q, want %q", executable, source)
			}
			if preserved {
				t.Fatal("system executable was reported as preserved installation")
			}
			if output.Len() != 0 {
				t.Fatalf("output = %q, want none", output.String())
			}
		})
	}
}

func TestRequireMontrayServerServiceAccountChecksUserAndGroup(t *testing.T) {
	lookupUser := func(name string) (*user.User, error) {
		if name != montrayUserName {
			t.Fatalf("user name = %q, want %q", name, montrayUserName)
		}
		return &user.User{Username: name}, nil
	}
	lookupGroup := func(name string) (*user.Group, error) {
		if name != montrayGroupName {
			t.Fatalf("group name = %q, want %q", name, montrayGroupName)
		}
		return &user.Group{Name: name}, nil
	}
	if err := requireMontrayServiceAccountWith(lookupUser, lookupGroup); err != nil {
		t.Fatal(err)
	}

	err := requireMontrayServiceAccountWith(
		func(string) (*user.User, error) { return nil, user.UnknownUserError(montrayUserName) },
		lookupGroup,
	)
	if err == nil || !strings.Contains(err.Error(), "sudo montray-server setup create-user") {
		t.Fatalf("missing-user error = %v, want user-create guidance", err)
	}

	err = requireMontrayServiceAccountWith(
		lookupUser,
		func(string) (*user.Group, error) { return nil, user.UnknownGroupError(montrayGroupName) },
	)
	if err == nil || !strings.Contains(err.Error(), "sudo montray-server setup create-user") {
		t.Fatalf("missing-group error = %v, want user-create guidance", err)
	}
}

func TestLogLevelFlagDefaultsToInfoAndRejectsInvalidValues(t *testing.T) {
	command := newRootCommand()
	flag := command.Flags().Lookup("log-level")
	if flag == nil || flag.DefValue != "info" {
		t.Fatalf("log-level flag = %#v, want default info", flag)
	}
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"--log-level", "verbose"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "invalid log level") {
		t.Fatalf("error = %v, want invalid-log-level error", err)
	}
}

func TestVersionFlagPrintsBuildInformation(t *testing.T) {
	command := newRootCommand()
	output := &bytes.Buffer{}
	command.SetOut(output)
	command.SetArgs([]string{"--version"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Montray Server dev\n", "Commit: none\n", "Build time: unknown\n", "Built by: unknown\n", "GOOS: ", "CGO: "} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("version output %q does not contain %q", output.String(), want)
		}
	}
}

func TestMontrayServerServiceTemplateIncludesExecutableAndConfig(t *testing.T) {
	unit, err := setup.RenderSystemdUnitTemplate("montray-server.service.tpl", string(mustSetupAsset("assets/setup/montray-server.service.tpl")), struct {
		Executable     string
		ConfigFilename string
	}{"/usr/local/bin/montray-server", "/etc/montray-server.yml"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"User=montray", "Group=montray", "ExecStart=\"/usr/local/bin/montray-server\" --config \"/etc/montray-server.yml\"", "WantedBy=multi-user.target"} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit %q does not contain %q", unit, want)
		}
	}
}

func TestMontrayServerServiceTemplateEscapesSystemdSpecifiers(t *testing.T) {
	unit, err := setup.RenderSystemdUnitTemplate("montray-server.service.tpl", string(mustSetupAsset("assets/setup/montray-server.service.tpl")), struct {
		Executable     string
		ConfigFilename string
	}{"/tmp/sal%mon", "/tmp/a%b.yml"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "ExecStart=\"/tmp/sal%%mon\" --config \"/tmp/a%%b.yml\""; !strings.Contains(unit, want) {
		t.Fatalf("unit %q does not contain %q", unit, want)
	}
}

func TestRunMontrayServerSuggestsSetupWhenConfigIsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yml")
	err := runMontrayServer(path, logs.Info)
	if err == nil || strings.Contains(err.Error(), "setup") {
		t.Fatalf("runMontrayServer() error = %v, want no setup guidance for custom config", err)
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

func TestRuntimeConfigFallsBackToLegacySalmonPath(t *testing.T) {
	directory := t.TempDir()
	renamed := filepath.Join(directory, "montray-server.yml")
	legacy := filepath.Join(directory, "salmon.yml")
	if err := os.WriteFile(legacy, mustSetupAsset("assets/setup/montray-server.yml"), 0600); err != nil {
		t.Fatal(err)
	}

	_, loadedPath, err := loadRuntimeConfigWithFallback(renamed, legacy, true)
	if err != nil {
		t.Fatal(err)
	}
	if loadedPath != legacy {
		t.Fatalf("loaded config path = %q, want legacy path %q", loadedPath, legacy)
	}

	if err := os.WriteFile(renamed, mustSetupAsset("assets/setup/montray-server.yml"), 0600); err != nil {
		t.Fatal(err)
	}
	_, loadedPath, err = loadRuntimeConfigWithFallback(renamed, legacy, true)
	if err != nil {
		t.Fatal(err)
	}
	if loadedPath != renamed {
		t.Fatalf("loaded config path = %q, want renamed path %q", loadedPath, renamed)
	}
}

func TestLoadConfigRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "montray.yml")
	if err := os.WriteFile(path, []byte("core:\n  collectorz: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "collectorz") {
		t.Fatalf("loadConfig error = %v, want unknown-field error", err)
	}
}

func TestLoadConfigUsesSystemdRuleFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "montray.yml")
	data := []byte(`core:
  collectors:
    - id: services
      systemd:
        unitRules:
          - names: [one.service, two.service]
            conditions:
              - {subStateContains: auto-restart, result: warning, resolve: {after: 5s, states: [active, inactive, not-sent-by-systemd]}}
`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Core.Collectors[0].Systemd.UnitRules[0].Names
	if strings.Join(got, ",") != "one.service,two.service" {
		t.Fatalf("rule names = %#v", got)
	}
	condition := cfg.Core.Collectors[0].Systemd.UnitRules[0].Conditions[0]
	if condition.SubStateContains != "auto-restart" || condition.Resolve == nil || condition.Resolve.After != 5*time.Second || len(condition.Resolve.States) != 3 || condition.Resolve.States[0] != "active" || condition.Resolve.States[1] != "inactive" || condition.Resolve.States[2] != "not-sent-by-systemd" || condition.Result != montray.ItemStateWarning {
		t.Fatalf("rule condition = %#v", condition)
	}
}

func TestLoadConfigUsesWebserverTLSOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "montray.yml")
	data := []byte(`core:
  messengers:
    - webserver:
        listenAddress: 127.0.0.1:41990
        tls:
          certFile: /etc/montray/tls/fullchain.pem
          keyFile: /etc/montray/tls/privkey.pem
        auth:
          - id: my-laptop
            bearerTokenHash: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig := cfg.Core.Messengers[0].Webserver.TLS
	if tlsConfig == nil || tlsConfig.CertFile != "/etc/montray/tls/fullchain.pem" || tlsConfig.KeyFile != "/etc/montray/tls/privkey.pem" {
		t.Fatalf("TLS config = %#v", tlsConfig)
	}
	auth := cfg.Core.Messengers[0].Webserver.Auth
	if len(auth) != 1 || auth[0].ID != "my-laptop" || auth[0].BearerTokenHash == "" {
		t.Fatalf("auth config = %#v", auth)
	}
}

func TestLoadConfigRejectsOldNestedBearerTokenAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "montray.yml")
	data := []byte(`core:
  messengers:
    - webserver:
        listenAddress: 127.0.0.1:41990
        auth:
          bearerTokens:
            - id: my-laptop
              tokenHash: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil {
		t.Fatal("old nested bearer-token authentication was accepted")
	}
}
