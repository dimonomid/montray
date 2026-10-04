package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/dimonomid/montray/v2/internal/setup"
)

// validateWatchSetupPlatform prevents Linux desktop-autostart setup from
// creating unusable files on platforms that do not support it yet.
func validateWatchSetupPlatform(goos string) error {
	if goos != "linux" {
		return fmt.Errorf("montray-ui-legacy setup is not implemented on this platform (%s)", goos)
	}
	return nil
}

// initializeWatchConfig creates the configuration when absent and reports the
// result to output.
func initializeWatchConfig(output io.Writer, configFilename string) error {
	created, err := setup.EnsureFile(configFilename, string(mustEmbeddedAsset("assets/setup/montray-ui.yml")))
	if err != nil {
		return err
	}
	return setup.ReportEnsureResult(output, "configuration", configFilename, created)
}

// installWatchAutostart creates the desktop autostart entry when absent and
// reports the result to output.
func installWatchAutostart(output io.Writer, configFilename string) error {
	entry, err := renderWatchDesktopEntry(configFilename)
	if err != nil {
		return err
	}
	autostartPath, err := defaultWatchAutostartPath()
	if err != nil {
		return err
	}
	created, err := setup.EnsureFile(autostartPath, entry)
	if err != nil {
		return err
	}
	return setup.ReportEnsureResult(output, "desktop autostart entry", autostartPath, created)
}

// installWatchLauncher creates the desktop application-menu entry when absent
// and reports the result to output.
func installWatchLauncher(output io.Writer, configFilename string) error {
	entry, err := renderWatchDesktopEntry(configFilename)
	if err != nil {
		return err
	}
	iconPath, err := defaultWatchIconPath()
	if err != nil {
		return err
	}
	iconCreated, err := setup.EnsureFile(iconPath, string(mustEmbeddedAsset("assets/webroot/favicon.svg")))
	if err != nil {
		return err
	}
	if err := setup.ReportEnsureResult(output, "application icon", iconPath, iconCreated); err != nil {
		return err
	}
	launcherPath, err := defaultWatchLauncherPath()
	if err != nil {
		return err
	}
	created, err := setup.EnsureFile(launcherPath, entry)
	if err != nil {
		return err
	}
	return setup.ReportEnsureResult(output, "application launcher", launcherPath, created)
}

func renderWatchDesktopEntry(configFilename string) (string, error) {
	absoluteConfigFilename, err := filepath.Abs(configFilename)
	if err != nil {
		return "", fmt.Errorf("resolve config path: %w", err)
	}
	if _, err := loadConfig(absoluteConfigFilename); err != nil {
		return "", fmt.Errorf("validate config at %s: %w", absoluteConfigFilename, err)
	}
	executable, err := setup.PersistentExecutablePath()
	if err != nil {
		return "", err
	}
	entry, err := setup.RenderDesktopEntryTemplate("montray-ui-legacy.desktop.tpl", string(mustEmbeddedAsset("assets/setup/montray-ui-legacy.desktop.tpl")), struct {
		Executable     string
		ConfigFilename string
	}{executable, absoluteConfigFilename})
	if err != nil {
		return "", err
	}
	return entry, nil
}

// printWatchStartHint explains how to start Montray UI Legacy now.
func printWatchStartHint(output io.Writer, configFilename string) error {
	startCommand, err := watchStartCommand(configFilename)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "\nMontray UI Legacy is configured and installed for desktop autostart and application-menu launch. To start it now, run:\n\n    %s\n", startCommand)
	return err
}

// defaultWatchConfigPath returns the default XDG configuration path.
func defaultWatchConfigPath() (string, error) {
	configHome, err := userConfigHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(configHome, "montray-ui", "montray-ui.yml"), nil
}

// runtimeDefaultWatchConfigPath prefers the Montray UI path but lets existing
// Salmon Watch users keep running without moving their configuration first.
func runtimeDefaultWatchConfigPath() (string, error) {
	canonical, err := defaultWatchConfigPath()
	if err != nil {
		return "", err
	}
	configHome, err := userConfigHome()
	if err != nil {
		return "", err
	}
	former := filepath.Join(configHome, "salmon-watch", "salmon-watch.yml")
	if _, err := os.Stat(canonical); err == nil || !os.IsNotExist(err) {
		return canonical, nil
	}
	if _, err := os.Stat(former); err == nil {
		return former, nil
	}
	return canonical, nil
}

// defaultWatchAutostartPath returns the default XDG desktop-autostart path.
func defaultWatchAutostartPath() (string, error) {
	configHome, err := userConfigHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(configHome, "autostart", "montray-ui-legacy.desktop"), nil
}

// defaultWatchLauncherPath returns the XDG desktop application-menu path.
func defaultWatchLauncherPath() (string, error) {
	dataHome, err := userDataHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataHome, "applications", "montray-ui-legacy.desktop"), nil
}

// defaultWatchIconPath returns the scalable XDG application-icon path.
func defaultWatchIconPath() (string, error) {
	dataHome, err := userDataHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataHome, "icons", "hicolor", "scalable", "apps", "montray-ui-legacy.svg"), nil
}

// watchStartCommand returns a command that starts Montray UI Legacy with the given
// configuration, resolving custom paths so the command works from any
// directory.
func watchStartCommand(configFilename string) (string, error) {
	defaultConfigFilename, err := defaultWatchConfigPath()
	if err == nil && configFilename == defaultConfigFilename {
		return setup.ShellArgument(os.Args[0]), nil
	}
	absoluteConfigFilename, err := filepath.Abs(configFilename)
	if err != nil {
		return "", fmt.Errorf("resolve config path: %w", err)
	}
	return fmt.Sprintf("%s --config %s", setup.ShellArgument(os.Args[0]), setup.ShellArgument(absoluteConfigFilename)), nil
}

// userConfigHome returns the platform's user configuration directory. It does
// not guess a relative path when the environment cannot identify one.
func userConfigHome() (string, error) {
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("determine user configuration directory: %w", err)
	}
	return directory, nil
}

// userDataHome returns XDG_DATA_HOME or its standard per-user default.
func userDataHome() (string, error) {
	if directory := os.Getenv("XDG_DATA_HOME"); directory != "" {
		return directory, nil
	}
	homeDirectory, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine user data directory: %w", err)
	}
	return filepath.Join(homeDirectory, ".local", "share"), nil
}
