package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/dimonomid/montray/v2/internal/setup"
)

const (
	salmonServerExecutablePath = "/usr/local/bin/salmon"
	salmonUserName             = "salmon"
	salmonGroupName            = "salmon"
	salmonSysusersPath         = "/usr/local/lib/sysusers.d/salmon.conf"
	salmonServerUnitPath       = "/etc/systemd/system/salmon.service"
)

// salmonServerInstallation identifies only files created by Salmon's built-in
// setup command, keeping migration cleanup deliberately narrow.
type salmonServerInstallation struct {
	config     string
	executable string
	unit       string
	sysusers   string
}

// defaultSalmonServerInstallation describes the one installation layout the
// pre-package-manager Salmon setup supported.
func defaultSalmonServerInstallation() salmonServerInstallation {
	return salmonServerInstallation{
		config:     legacySalmonConfig,
		executable: salmonServerExecutablePath,
		unit:       salmonServerUnitPath,
		sysusers:   salmonSysusersPath,
	}
}

// existingInstallationPaths uses Lstat so a dangling setup-created symlink is
// still evidence of an old installation and is not silently skipped.
func existingInstallationPaths(installation salmonServerInstallation) ([]string, error) {
	var existing []string
	for _, path := range []string{installation.config, installation.executable, installation.unit, installation.sysusers} {
		if _, err := os.Lstat(path); err == nil {
			existing = append(existing, path)
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("inspect former Salmon installation at %s: %w", path, err)
		}
	}
	return existing, nil
}

// rejectSalmonServerInstallation prevents ordinary setup from creating a
// parallel installation when the explicit migration workflow is appropriate.
func rejectSalmonServerInstallation(installation salmonServerInstallation) error {
	existing, err := existingInstallationPaths(installation)
	if err != nil || len(existing) == 0 {
		return err
	}
	return fmt.Errorf("a Salmon Server installation was detected:\n  %s\n\nMigrate it explicitly with:\n\n    sudo montray-server setup migrate-from-salmon\n\nTo create a separate Montray Server installation, rerun setup with --ignore-salmon", strings.Join(existing, "\n  "))
}

// migrateMontrayServerFromSalmon keeps Salmon recoverable until the new service
// is active, then removes only the known setup-created artifacts.
func migrateMontrayServerFromSalmon(output io.Writer, configFilename string, dryRun bool) error {
	installation := defaultSalmonServerInstallation()
	existing, err := existingInstallationPaths(installation)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		if _, err := os.Stat(configFilename); err == nil {
			_, err = fmt.Fprintln(output, "No Salmon Server installation remains; Montray Server is already configured.")
			return err
		}
		return fmt.Errorf("no Salmon Server installation was found")
	}

	if dryRun {
		return printSalmonServerMigrationPlan(output, installation, configFilename, existing)
	}

	if err := prepareMigratedServerConfig(installation.config, configFilename); err != nil {
		return err
	}
	oldUnitExists := pathExists(installation.unit)
	if oldUnitExists {
		if err := runCommand("systemctl", "disable", "--now", "salmon.service"); err != nil {
			return fmt.Errorf("stop and disable Salmon service: %w", err)
		}
	}

	accountRenamed, err := renameSalmonServiceAccount(runCommand, user.Lookup, user.LookupGroup)
	if err != nil {
		restartFormerSalmonService(oldUnitExists)
		return err
	}
	rollback := func(cause error) error {
		_ = runCommand("systemctl", "disable", "--now", "montray-server.service")
		if accountRenamed {
			_ = runCommand("groupmod", "--new-name", salmonGroupName, montrayGroupName)
			_ = runCommand("usermod", "--login", salmonUserName, montrayUserName)
		}
		restartFormerSalmonService(oldUnitExists)
		return cause
	}

	if err := createMontrayUser(output); err != nil {
		return rollback(err)
	}
	if err := installMontrayServerService(output, configFilename, true); err != nil {
		return rollback(err)
	}
	if err := restartMontrayServerService(output); err != nil {
		return rollback(err)
	}
	if err := runCommand("systemctl", "is-active", "--quiet", "montray-server.service"); err != nil {
		return rollback(fmt.Errorf("verify Montray Server service is active: %w", err))
	}

	if err := removeFormerSalmonServerFiles(output, installation, configFilename); err != nil {
		return err
	}
	return nil
}

// printSalmonServerMigrationPlan mirrors the mutating workflow closely enough
// that --dry-run can be used as a meaningful preflight.
func printSalmonServerMigrationPlan(output io.Writer, installation salmonServerInstallation, configFilename string, existing []string) error {
	_, err := fmt.Fprintf(output, "Would migrate Salmon Server to Montray Server:\n  detected: %s\n  copy configuration to: %s\n  rename service account: %s -> %s\n  install and start: montray-server.service\n", strings.Join(existing, ", "), configFilename, salmonUserName, montrayUserName)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, "  remove the former service, executable, and sysusers file; retain the old config as a backup")
	return err
}

// prepareMigratedServerConfig copies the user's configuration without replacing
// a destination that may already contain independent Montray changes.
func prepareMigratedServerConfig(source, destination string) error {
	if _, err := os.Stat(destination); err == nil {
		if _, err := loadConfig(destination); err != nil {
			return fmt.Errorf("validate existing Montray Server config at %s: %w", destination, err)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect Montray Server config at %s: %w", destination, err)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read Salmon Server config at %s: %w", source, err)
	}
	if _, err := loadConfig(source); err != nil {
		return fmt.Errorf("validate Salmon Server config at %s: %w", source, err)
	}
	created, err := setup.EnsureFile(destination, string(data))
	if err != nil {
		return fmt.Errorf("copy Salmon Server config to %s: %w", destination, err)
	}
	if !created {
		return fmt.Errorf("Montray Server config appeared during migration at %s; rerun migration", destination)
	}
	return nil
}

// renameSalmonServiceAccount preserves the numeric UID and GID, and therefore
// access to arbitrary TLS keys and log files owned by the old daemon account.
func renameSalmonServiceAccount(run setup.CmdRunner, lookupUser func(string) (*user.User, error), lookupGroup func(string) (*user.Group, error)) (bool, error) {
	oldUserExists, err := userExists(salmonUserName, lookupUser)
	if err != nil {
		return false, err
	}
	newUserExists, err := userExists(montrayUserName, lookupUser)
	if err != nil {
		return false, err
	}
	oldGroupExists, err := groupExists(salmonGroupName, lookupGroup)
	if err != nil {
		return false, err
	}
	newGroupExists, err := groupExists(montrayGroupName, lookupGroup)
	if err != nil {
		return false, err
	}
	if !oldUserExists && !oldGroupExists {
		return false, nil
	}
	if newUserExists || newGroupExists {
		return false, fmt.Errorf("cannot rename the Salmon service account because %q already exists", montrayUserName)
	}
	if !oldUserExists || !oldGroupExists {
		return false, fmt.Errorf("incomplete Salmon service account: user exists=%t, group exists=%t", oldUserExists, oldGroupExists)
	}
	if err := run("usermod", "--login", montrayUserName, salmonUserName); err != nil {
		return false, fmt.Errorf("rename Salmon service user: %w", err)
	}
	if err := run("groupmod", "--new-name", montrayGroupName, salmonGroupName); err != nil {
		_ = run("usermod", "--login", salmonUserName, montrayUserName)
		return false, fmt.Errorf("rename Salmon service group: %w", err)
	}
	return true, nil
}

// userExists distinguishes a genuinely absent account from an NSS lookup
// failure; treating the latter as absence could create a conflicting account.
func userExists(name string, lookup func(string) (*user.User, error)) (bool, error) {
	_, err := lookup(name)
	if err == nil {
		return true, nil
	}
	var unknown user.UnknownUserError
	if errors.As(err, &unknown) {
		return false, nil
	}
	return false, fmt.Errorf("look up service user %q: %w", name, err)
}

// groupExists applies the same fail-closed lookup semantics as userExists.
func groupExists(name string, lookup func(string) (*user.Group, error)) (bool, error) {
	_, err := lookup(name)
	if err == nil {
		return true, nil
	}
	var unknown user.UnknownGroupError
	if errors.As(err, &unknown) {
		return false, nil
	}
	return false, fmt.Errorf("look up service group %q: %w", name, err)
}

// removeFormerSalmonServerFiles runs only after Montray has been verified active
// and retains the user-edited Salmon configuration as a recovery copy.
func removeFormerSalmonServerFiles(output io.Writer, installation salmonServerInstallation, configFilename string) error {
	for _, path := range []string{installation.unit, installation.executable, installation.sysusers} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove former Salmon file %s: %w", path, err)
		}
	}
	if filepath.Clean(installation.config) != filepath.Clean(configFilename) && pathExists(installation.config) {
		backup := installation.config + ".pre-montray"
		if !pathExists(backup) {
			if err := os.Rename(installation.config, backup); err != nil {
				return fmt.Errorf("retain former Salmon config at %s: %w", backup, err)
			}
		} else {
			return fmt.Errorf("cannot retain former Salmon config: both %s and backup %s exist", installation.config, backup)
		}
	}
	if err := runCommand("systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("reload systemd after removing Salmon service: %w", err)
	}
	_, err := fmt.Fprintln(output, "Migration complete. Former Salmon service files were removed; its configuration was retained with a .pre-montray suffix.")
	return err
}

// restartFormerSalmonService is best-effort rollback; callers preserve the
// original migration error because it is the actionable failure.
func restartFormerSalmonService(unitExists bool) {
	if unitExists {
		_ = runCommand("systemctl", "enable", "salmon.service")
		_ = runCommand("systemctl", "restart", "salmon.service")
	}
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
