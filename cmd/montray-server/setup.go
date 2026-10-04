package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/dimonomid/montray/v2/internal/setup"
)

const (
	defaultMontrayServerConfig  = "/etc/montray-server.yml"
	legacySalmonConfig          = "/etc/salmon.yml"
	montrayServerExecutablePath = "/usr/local/bin/montray-server"
	montrayUserName             = "_montray"
	montrayGroupName            = "_montray"
	montraySysusersPath         = "/usr/local/lib/sysusers.d/montray.conf"
	montrayServerUnitPath       = "/etc/systemd/system/montray-server.service"
)

// initializeMontrayServerConfig creates the configuration when absent and reports the
// result to output.
func initializeMontrayServerConfig(output io.Writer, configFilename string) error {
	created, err := setup.EnsureFile(configFilename, string(mustSetupAsset("assets/setup/montray-server.yml")))
	if err != nil {
		return err
	}
	return setup.ReportEnsureResult(output, "configuration", configFilename, created)
}

// createMontrayUser installs Montray's sysusers configuration and asks systemd
// to create the service user and group when they do not already exist.
func createMontrayUser(output io.Writer) error {
	return createMontrayUserAt(output, montraySysusersPath, runCommand)
}

func createMontrayUserAt(output io.Writer, sysusersPath string, run setup.CmdRunner) error {
	created, err := setup.EnsureFile(sysusersPath, string(mustSetupAsset("assets/setup/montray-server.sysusers")))
	if err != nil {
		return err
	}
	if err := setup.ReportEnsureResult(output, "systemd sysusers configuration", sysusersPath, created); err != nil {
		return err
	}
	if err := run("systemd-sysusers", sysusersPath); err != nil {
		return fmt.Errorf("create Montray service user and group: %w", err)
	}
	return nil
}

// requireMontrayServiceAccount prevents installing a unit that systemd cannot
// start because its configured user or group is missing.
func requireMontrayServiceAccount() error {
	return requireMontrayServiceAccountWith(user.Lookup, user.LookupGroup)
}

func requireMontrayServiceAccountWith(
	lookupUser func(string) (*user.User, error),
	lookupGroup func(string) (*user.Group, error),
) error {
	if _, err := lookupUser(montrayUserName); err != nil {
		return fmt.Errorf("service user %q does not exist; run `sudo montray-server setup create-user`: %w", montrayUserName, err)
	}
	if _, err := lookupGroup(montrayGroupName); err != nil {
		return fmt.Errorf("service group %q does not exist; run `sudo montray-server setup create-user`: %w", montrayGroupName, err)
	}
	return nil
}

// installMontrayServerService installs or explicitly reinstalls the executable and
// systemd unit, enables the service, and reports each result.
func installMontrayServerService(output io.Writer, configFilename string, reinstall bool) error {
	if err := requireMontrayServiceAccount(); err != nil {
		return err
	}
	absoluteConfigFilename, err := filepath.Abs(configFilename)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	if _, err := loadConfig(absoluteConfigFilename); err != nil {
		return fmt.Errorf("validate config at %s: %w", absoluteConfigFilename, err)
	}
	executable, err := setup.ExecutablePath()
	if err != nil {
		return err
	}
	executable, executablePreserved, err := installMontrayServerExecutable(output, executable, reinstall)
	if err != nil {
		return err
	}
	unit, err := setup.RenderSystemdUnitTemplate("montray-server.service.tpl", string(mustSetupAsset("assets/setup/montray-server.service.tpl")), struct {
		Executable     string
		ConfigFilename string
	}{executable, absoluteConfigFilename})
	if err != nil {
		return err
	}
	installService := setup.InstallSystemdService
	if reinstall {
		installService = setup.ReinstallSystemdService
	}
	created, err := installService(montrayServerUnitPath, "montray-server.service", unit, runCommand)
	if err != nil {
		return err
	}
	if !created && reinstall {
		_, err := fmt.Fprintf(output, "Updated systemd service at %s\n", montrayServerUnitPath)
		return err
	}
	if err := setup.ReportEnsureResult(output, "systemd service", montrayServerUnitPath, created); err != nil {
		return err
	}
	if executablePreserved || !created {
		return printMontrayReinstallHint(output)
	}
	return nil
}

// installMontrayServerExecutable copies an executable launched from a user or other
// non-system directory to a stable, system-wide location for the service.
func installMontrayServerExecutable(output io.Writer, source string, reinstall bool) (string, bool, error) {
	return installMontrayServerExecutableAt(output, source, montrayServerExecutablePath, reinstall)
}

func installMontrayServerExecutableAt(output io.Writer, source, destination string, reinstall bool) (string, bool, error) {
	if isSystemExecutablePath(source) {
		return source, false, nil
	}
	installed, err := setup.InstallExecutable(source, destination, reinstall)
	if err != nil {
		return "", false, err
	}
	if installed {
		action := "Installed"
		if reinstall {
			action = "Reinstalled"
		}
		if _, err := fmt.Fprintf(output, "%s executable at %s\n", action, destination); err != nil {
			return "", false, err
		}
	} else {
		if err := setup.ReportEnsureResult(output, "executable", destination, false); err != nil {
			return "", false, err
		}
	}
	return destination, !installed, nil
}

func printMontrayReinstallHint(output io.Writer) error {
	_, err := fmt.Fprintln(output, "\nRun this setup command again with --reinstall to replace installed files. The configuration will not be overwritten.")
	return err
}

func isSystemExecutablePath(path string) bool {
	path = filepath.Clean(path)
	for _, directory := range []string{"/bin", "/sbin", "/usr/bin", "/usr/sbin", "/usr/local/bin", "/usr/local/sbin"} {
		if filepath.Dir(path) == directory {
			return true
		}
	}
	for _, directory := range []string{"/nix/store", "/opt", "/snap"} {
		if path == directory || strings.HasPrefix(path, directory+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// restartMontrayServerService starts the installed service or restarts it to pick up
// an explicitly reinstalled executable or unit.
func restartMontrayServerService(output io.Writer) error {
	return restartMontrayServerServiceWith(output, runCommand)
}

func restartMontrayServerServiceWith(output io.Writer, run setup.CmdRunner) error {
	if err := run("systemctl", "restart", "montray-server.service"); err != nil {
		return fmt.Errorf("start Montray Server service: %w\n\nView its status and recent logs with:\n\n    sudo systemctl status montray-server.service\n    sudo journalctl --no-pager -u montray-server.service -n 50", err)
	}
	_, err := fmt.Fprintln(output, "\nMontray Server is configured, installed, and running.")
	return err
}

// runCommand executes a command, forwarding its standard output and error to
// Montray's own streams.
func runCommand(name string, args ...string) error {
	command := exec.Command(name, args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}
