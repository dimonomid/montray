package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequirePersistentExecutablePathRejectsOnlyTemporaryTree(t *testing.T) {
	temporary := t.TempDir()
	for _, path := range []string{temporary, filepath.Join(temporary, "unpacked", "montray-ui-legacy")} {
		if _, err := requirePersistentExecutablePath(path, temporary); err == nil || !strings.Contains(err.Error(), "temporary executable") {
			t.Fatalf("requirePersistentExecutablePath(%q) error = %v, want temporary-path rejection", path, err)
		}
	}

	stable := temporary + "-download/montray-server"
	got, err := requirePersistentExecutablePath(stable, temporary)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(stable) {
		t.Fatalf("requirePersistentExecutablePath() = %q, want %q", got, filepath.Clean(stable))
	}
}

func TestInstallExecutablePreservesDestinationByDefault(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "download", "montray")
	destination := filepath.Join(directory, "usr", "local", "bin", "montray")
	if err := os.MkdirAll(filepath.Dir(source), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("new executable"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("old executable"), 0700); err != nil {
		t.Fatal(err)
	}

	installed, err := InstallExecutable(source, destination, false)
	if err != nil {
		t.Fatal(err)
	}
	if installed {
		t.Fatal("InstallExecutable() replaced an existing executable without reinstall")
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "old executable"; got != want {
		t.Fatalf("installed executable = %q, want %q", got, want)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0700); got != want {
		t.Fatalf("installed executable mode = %o, want %o", got, want)
	}
}

func TestInstallExecutableReplacesDestinationDuringReinstall(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "download", "montray")
	destination := filepath.Join(directory, "usr", "local", "bin", "montray")
	if err := os.MkdirAll(filepath.Dir(source), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("new executable"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("old executable"), 0700); err != nil {
		t.Fatal(err)
	}

	installed, err := InstallExecutable(source, destination, true)
	if err != nil {
		t.Fatal(err)
	}
	if !installed {
		t.Fatal("InstallExecutable() did not reinstall the executable")
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "new executable"; got != want {
		t.Fatalf("installed executable = %q, want %q", got, want)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0755); got != want {
		t.Fatalf("installed executable mode = %o, want %o", got, want)
	}
}
