package main

import (
	"bytes"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dimonomid/montray/internal/setup"
)

func TestWatchSetupCreateConfigCreatesConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "montray-ui.yml")
	command := newWatchRootCommand()
	output := &bytes.Buffer{}
	command.SetOut(output)
	command.SetErr(output)
	command.SetArgs([]string{"setup", "create-config", "--config", path})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	data, err := ioutil.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), string(mustEmbeddedAsset("assets/setup/montray-ui.yml")); got != want {
		t.Fatalf("config contents = %q, want %q", got, want)
	}
	if _, err := loadConfig(path); err != nil {
		t.Fatalf("generated config is invalid: %v", err)
	}
}

func TestWatchSetupDoesNotFallBackToRelativeConfigDirectory(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	command := newWatchRootCommand()
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"setup", "create-config"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "determine user configuration directory") {
		t.Fatalf("setup error = %v, want user-configuration-directory error", err)
	}
}

func TestWatchConfigPathsRespectXDGConfigHome(t *testing.T) {
	xdgConfigHome := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", xdgConfigHome)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "different-home"))

	configPath, err := defaultWatchConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(xdgConfigHome, "montray-ui", "montray-ui.yml"); configPath != want {
		t.Fatalf("default config path = %q, want %q", configPath, want)
	}

	autostartPath, err := defaultWatchAutostartPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(xdgConfigHome, "autostart", "montray-ui-legacy.desktop"); autostartPath != want {
		t.Fatalf("default autostart path = %q, want %q", autostartPath, want)
	}
}

func TestWatchRuntimeConfigPathFallsBackToSalmonWatch(t *testing.T) {
	xdgConfigHome := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", xdgConfigHome)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "different-home"))

	canonical := filepath.Join(xdgConfigHome, "montray-ui", "montray-ui.yml")
	former := filepath.Join(xdgConfigHome, "salmon-watch", "salmon-watch.yml")
	if err := os.MkdirAll(filepath.Dir(former), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(former, []byte("former"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := runtimeDefaultWatchConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != former {
		t.Fatalf("runtime config path = %q, want former path %q", got, former)
	}

	if err := os.MkdirAll(filepath.Dir(canonical), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonical, []byte("canonical"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err = runtimeDefaultWatchConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != canonical {
		t.Fatalf("runtime config path = %q, want canonical path %q", got, canonical)
	}
}

func TestWatchLauncherPathRespectsXDGDataHome(t *testing.T) {
	xdgDataHome := filepath.Join(t.TempDir(), "xdg-data")
	t.Setenv("XDG_DATA_HOME", xdgDataHome)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "different-home"))

	launcherPath, err := defaultWatchLauncherPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(xdgDataHome, "applications", "montray-ui-legacy.desktop"); launcherPath != want {
		t.Fatalf("default launcher path = %q, want %q", launcherPath, want)
	}

	iconPath, err := defaultWatchIconPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(xdgDataHome, "icons", "hicolor", "scalable", "apps", "montray-ui-legacy.svg"); iconPath != want {
		t.Fatalf("default icon path = %q, want %q", iconPath, want)
	}
}

func TestWatchSetupAllowsExplicitConfigWithoutUserConfigDirectory(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	path := filepath.Join(t.TempDir(), "montray.yml")
	command := newWatchRootCommand()
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"setup", "create-config", "--config", path})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("explicit configuration was not created: %v", err)
	}
}

func TestWatchSetupPlatformValidation(t *testing.T) {
	if err := validateWatchSetupPlatform("linux"); err != nil {
		t.Fatalf("Linux setup rejected: %v", err)
	}
	for _, goos := range []string{"darwin", "windows"} {
		err := validateWatchSetupPlatform(goos)
		if err == nil || !strings.Contains(err.Error(), "not implemented on this platform ("+goos+")") {
			t.Errorf("validateWatchSetupPlatform(%q) error = %v", goos, err)
		}
	}
}

func TestWatchRunnableCommandsRejectPositionalArguments(t *testing.T) {
	for _, args := range [][]string{
		{"unexpected"},
		{"setup", "unexpected"},
		{"setup", "create-config", "unexpected"},
		{"setup", "install-autostart", "unexpected"},
		{"setup", "install-launcher", "unexpected"},
		{"generate-bearer-token"},
		{"generate-bearer-token", "one", "two"},
	} {
		command := newWatchRootCommand()
		command.SetArgs(args)
		if err := command.Execute(); err == nil {
			t.Errorf("command %q accepted an unexpected positional argument", args)
		}
	}
}

func TestWatchSetupOperationsDoNotPolluteTopLevelCommands(t *testing.T) {
	command := newWatchRootCommand()
	topLevelCommands := map[string]*cobra.Command{}
	for _, subcommand := range command.Commands() {
		topLevelCommands[subcommand.Name()] = subcommand
	}
	if len(topLevelCommands) != 2 || topLevelCommands["setup"] == nil || topLevelCommands["generate-bearer-token"] == nil {
		t.Fatalf("top-level commands = %v, want setup and generate-bearer-token", topLevelCommands)
	}
	if !strings.Contains(topLevelCommands["setup"].Long, "Perform the complete setup") {
		t.Fatalf("setup long help = %q, want complete-setup description", topLevelCommands["setup"].Long)
	}

	setupCommands := map[string]bool{}
	for _, subcommand := range topLevelCommands["setup"].Commands() {
		setupCommands[subcommand.Name()] = true
	}
	for _, want := range []string{"create-config", "install-autostart", "install-launcher"} {
		if !setupCommands[want] {
			t.Errorf("setup subcommands = %v, missing %q", setupCommands, want)
		}
	}
}

func TestWatchLogLevelFlagDefaultsToInfoAndRejectsInvalidValues(t *testing.T) {
	command := newWatchRootCommand()
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

func TestWatchVersionFlagPrintsBuildInformation(t *testing.T) {
	command := newWatchRootCommand()
	output := &bytes.Buffer{}
	command.SetOut(output)
	command.SetArgs([]string{"--version"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Montray UI Legacy dev\n", "Commit: none\n", "Build time: unknown\n", "Built by: unknown\n", "GOOS: ", "CGO: "} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("version output %q does not contain %q", output.String(), want)
		}
	}
}

func TestWatchStartCommand(t *testing.T) {
	defaultConfigFilename, err := defaultWatchConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	got, err := watchStartCommand(defaultConfigFilename)
	if err != nil {
		t.Fatal(err)
	}
	if want := setup.ShellArgument(os.Args[0]); got != want {
		t.Fatalf("default start command = %q, want %q", got, want)
	}
	got, err = watchStartCommand("/tmp/custom.yml")
	if err != nil {
		t.Fatal(err)
	}
	if want := setup.ShellArgument(os.Args[0]) + " --config /tmp/custom.yml"; got != want {
		t.Fatalf("custom start command = %q, want %q", got, want)
	}
	absoluteConfigFilename, err := filepath.Abs("custom.yml")
	if err != nil {
		t.Fatal(err)
	}
	got, err = watchStartCommand("custom.yml")
	if err != nil {
		t.Fatal(err)
	}
	if want := setup.ShellArgument(os.Args[0]) + " --config " + absoluteConfigFilename; got != want {
		t.Fatalf("relative custom start command = %q, want %q", got, want)
	}
	got, err = watchStartCommand("/tmp/custom config's.yml")
	if err != nil {
		t.Fatal(err)
	}
	if want := setup.ShellArgument(os.Args[0]) + " --config '/tmp/custom config'\"'\"'s.yml'"; got != want {
		t.Fatalf("quoted custom start command = %q, want %q", got, want)
	}
}

func TestWatchAutostartTemplateIncludesExecutableAndConfig(t *testing.T) {
	entry, err := setup.RenderDesktopEntryTemplate("montray-ui-legacy.desktop.tpl", string(mustEmbeddedAsset("assets/setup/montray-ui-legacy.desktop.tpl")), struct {
		Executable     string
		ConfigFilename string
	}{"/home/user/.local/bin/montray-ui-legacy", "/home/user/.config/montray-ui/montray-ui.yml"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Type=Application", "Icon=montray-ui-legacy", "Exec=\"/home/user/.local/bin/montray-ui-legacy\" --config \"/home/user/.config/montray-ui/montray-ui.yml\"", "Terminal=false"} {
		if !strings.Contains(entry, want) {
			t.Fatalf("entry %q does not contain %q", entry, want)
		}
	}
}

func TestWatchAutostartTemplateEscapesDesktopFieldCodes(t *testing.T) {
	entry, err := setup.RenderDesktopEntryTemplate("montray-ui-legacy.desktop.tpl", string(mustEmbeddedAsset("assets/setup/montray-ui-legacy.desktop.tpl")), struct {
		Executable     string
		ConfigFilename string
	}{"/tmp/sal%mon", "/tmp/a%b.yml"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "Exec=\"/tmp/sal%%mon\" --config \"/tmp/a%%b.yml\""; !strings.Contains(entry, want) {
		t.Fatalf("entry %q does not contain %q", entry, want)
	}
}
