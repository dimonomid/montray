package setup

import (
	"errors"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSystemdUnitArgument(t *testing.T) {
	argument := "a b%\"\\$`"
	if got, want := systemdUnitArgument(argument), "\"a b%%\\\"\\\\$`\""; got != want {
		t.Fatalf("systemdUnitArgument() = %q, want %q", got, want)
	}
}

func TestInstallSystemdServiceWritesUnitAndEnablesIt(t *testing.T) {
	unitPath := filepath.Join(t.TempDir(), "systemd", "montray.service")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0755); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	run := func(name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}

	created, err := InstallSystemdService(unitPath, "montray.service", "[Service]\n", run)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("InstallSystemdService() did not report creating a new unit")
	}
	data, err := ioutil.ReadFile(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "[Service]\n"; got != want {
		t.Fatalf("unit contents = %q, want %q", got, want)
	}
	wantCalls := [][]string{{"systemctl", "daemon-reload"}, {"systemctl", "enable", "montray.service"}}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("systemctl calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestInstallSystemdServiceStopsWhenReloadFails(t *testing.T) {
	unitPath := filepath.Join(t.TempDir(), "systemd", "montray.service")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0755); err != nil {
		t.Fatal(err)
	}
	called := 0
	_, err := InstallSystemdService(unitPath, "montray.service", "[Service]\n", func(string, ...string) error {
		called++
		return errors.New("no systemd")
	})
	if err == nil || called != 1 {
		t.Fatalf("InstallSystemdService() = %v after %d calls, want reload failure after one call", err, called)
	}
}

func TestInstallSystemdServiceRequiresExistingUnitDirectory(t *testing.T) {
	unitPath := filepath.Join(t.TempDir(), "missing", "montray.service")
	called := false
	created, err := InstallSystemdService(unitPath, "montray.service", "[Service]\n", func(string, ...string) error {
		called = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "access systemd unit directory") {
		t.Fatalf("InstallSystemdService() error = %v, want missing-directory error", err)
	}
	if created {
		t.Fatal("InstallSystemdService() reported creating a unit")
	}
	if called {
		t.Fatal("InstallSystemdService() called systemctl without a unit directory")
	}
	if _, statErr := os.Stat(filepath.Dir(unitPath)); !os.IsNotExist(statErr) {
		t.Fatalf("missing unit directory was created: %v", statErr)
	}
}

func TestInstallSystemdServiceDoesNotOverwriteExistingUnit(t *testing.T) {
	unitPath := filepath.Join(t.TempDir(), "systemd", "montray.service")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(unitPath, []byte("user unit\n"), 0644); err != nil {
		t.Fatal(err)
	}

	created, err := InstallSystemdService(unitPath, "montray.service", "generated unit\n", func(string, ...string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("InstallSystemdService() reported creating an existing unit")
	}
	data, err := ioutil.ReadFile(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "user unit\n"; got != want {
		t.Fatalf("unit contents = %q, want preserved contents %q", got, want)
	}
}

func TestReinstallSystemdServiceUpdatesExistingUnit(t *testing.T) {
	unitPath := filepath.Join(t.TempDir(), "systemd", "montray.service")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(unitPath, []byte("old unit\n"), 0644); err != nil {
		t.Fatal(err)
	}

	created, err := ReinstallSystemdService(unitPath, "montray.service", "generated unit\n", func(string, ...string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("ReinstallSystemdService() reported an existing unit as new")
	}
	data, err := ioutil.ReadFile(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "generated unit\n"; got != want {
		t.Fatalf("unit contents = %q, want updated contents %q", got, want)
	}
}
