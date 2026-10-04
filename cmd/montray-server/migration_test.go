package main

import (
	"bytes"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func TestRejectSalmonServerInstallationExplainsMigration(t *testing.T) {
	directory := t.TempDir()
	installation := salmonServerInstallation{
		config:     filepath.Join(directory, "salmon.yml"),
		executable: filepath.Join(directory, "salmon"),
		unit:       filepath.Join(directory, "salmon.service"),
		sysusers:   filepath.Join(directory, "salmon.conf"),
	}
	if err := os.WriteFile(installation.config, []byte("config"), 0644); err != nil {
		t.Fatal(err)
	}
	err := rejectSalmonServerInstallation(installation)
	if err == nil {
		t.Fatal("Salmon installation was not rejected")
	}
	for _, want := range []string{installation.config, "setup migrate-from-salmon", "--ignore-salmon"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
}

func TestPrepareMigratedServerConfigCopiesWithoutReplacing(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "salmon.yml")
	destination := filepath.Join(directory, "montray-server.yml")
	data := mustSetupAsset("assets/setup/montray-server.yml")
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareMigratedServerConfig(source, destination); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(destination); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("migrated config = %q, %v; want original", got, err)
	}

	replacement := append([]byte("# retained\n"), data...)
	if err := os.WriteFile(destination, replacement, 0644); err != nil {
		t.Fatal(err)
	}
	if err := prepareMigratedServerConfig(source, destination); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(destination); !bytes.Equal(got, replacement) {
		t.Fatalf("existing config was replaced: %q", got)
	}
}

func TestRenameSalmonServiceAccountPreservesIdentity(t *testing.T) {
	var calls []string
	run := func(name string, args ...string) error {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		return nil
	}
	lookupUser := func(name string) (*user.User, error) {
		if name == salmonUserName {
			return &user.User{Username: name, Uid: "123"}, nil
		}
		return nil, user.UnknownUserError(name)
	}
	lookupGroup := func(name string) (*user.Group, error) {
		if name == salmonGroupName {
			return &user.Group{Name: name, Gid: "123"}, nil
		}
		return nil, user.UnknownGroupError(name)
	}
	renamed, err := renameSalmonServiceAccount(run, lookupUser, lookupGroup)
	if err != nil {
		t.Fatal(err)
	}
	if !renamed {
		t.Fatal("account was not reported as renamed")
	}
	want := []string{"usermod --login _montray salmon", "groupmod --new-name _montray salmon"}
	if strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Fatalf("commands = %#v, want %#v", calls, want)
	}
}

func TestServerMigrationDryRunDoesNotChangeFiles(t *testing.T) {
	directory := t.TempDir()
	installation := salmonServerInstallation{
		config:     filepath.Join(directory, "salmon.yml"),
		executable: filepath.Join(directory, "salmon"),
		unit:       filepath.Join(directory, "salmon.service"),
		sysusers:   filepath.Join(directory, "salmon.conf"),
	}
	if err := os.WriteFile(installation.config, []byte("config"), 0644); err != nil {
		t.Fatal(err)
	}
	existing, err := existingInstallationPaths(installation)
	if err != nil {
		t.Fatal(err)
	}
	output := &bytes.Buffer{}
	if err := printSalmonServerMigrationPlan(output, installation, "/etc/montray-server.yml", existing); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Would migrate", "_montray", "retain the old config as a backup"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("plan %q does not contain %q", output, want)
		}
	}
	if _, err := os.Stat(installation.config); err != nil {
		t.Fatalf("dry-run source disappeared: %v", err)
	}
}
