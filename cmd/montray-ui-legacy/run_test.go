package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

type restartTestNotifier struct {
	title string
	body  string
}

func (n *restartTestNotifier) Push(title, body string) {
	n.title = title
	n.body = body
}

func TestRestartConfigValidationNotifiesAndKeepsRunningForInvalidConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "montray.yml")
	if err := os.WriteFile(configPath, []byte("wsClient: [invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	notify := &restartTestNotifier{}

	if restartConfigIsValid(configPath, notify, watchTestLogger) {
		t.Fatal("invalid configuration was accepted")
	}
	if notify.title != "Configuration reload failed" {
		t.Fatalf("notification title = %q", notify.title)
	}
	if notify.body == "" {
		t.Fatal("notification omitted configuration error")
	}
}

func TestRestartConfigValidationAcceptsValidConfigWithoutNotification(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "montray.yml")
	config := "wsClient:\n  servers:\n    - id: local\n      addr: localhost:8080\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	notify := &restartTestNotifier{}

	if !restartConfigIsValid(configPath, notify, watchTestLogger) {
		t.Fatal("valid configuration was rejected")
	}
	if notify.title != "" || notify.body != "" {
		t.Fatalf("unexpected notification: %q: %q", notify.title, notify.body)
	}
}

func TestRestartCommandPreservesExecutableArgumentsAndStreams(t *testing.T) {
	arguments := []string{
		"/opt/montray-ui-legacy",
		"--config", "/tmp/custom config.yml",
		"--log-level", "debug",
	}
	command := newRestartCommand("/opt/montray-ui-legacy", arguments)

	if command.Path != "/opt/montray-ui-legacy" {
		t.Fatalf("command path = %q", command.Path)
	}
	if len(command.Args) != len(arguments) {
		t.Fatalf("command args = %#v, want %#v", command.Args, arguments)
	}
	for i := range arguments {
		if command.Args[i] != arguments[i] {
			t.Fatalf("command args = %#v, want %#v", command.Args, arguments)
		}
	}
	if command.Stdin != os.Stdin || command.Stdout != os.Stdout || command.Stderr != os.Stderr {
		t.Fatal("restart command did not preserve standard streams")
	}
}

func TestWatchTerminationSignalRequestsTrayExit(t *testing.T) {
	signals := make(chan os.Signal, 1)
	stop := make(chan struct{})
	received := make(chan os.Signal, 1)
	done := make(chan struct{})
	go func() {
		waitForWatchTerminationSignal(signals, stop, func(sig os.Signal) {
			received <- sig
		})
		close(done)
	}()

	signals <- syscall.SIGINT
	select {
	case got := <-received:
		if got != syscall.SIGINT {
			t.Fatalf("signal = %v, want %v", got, syscall.SIGINT)
		}
	case <-time.After(time.Second):
		t.Fatal("termination signal did not request tray exit")
	}
	<-done
}

func TestWatchTerminationSignalHandlerStopsAfterTrayExit(t *testing.T) {
	signals := make(chan os.Signal, 1)
	stop := make(chan struct{})
	called := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		waitForWatchTerminationSignal(signals, stop, func(os.Signal) {
			called <- struct{}{}
		})
		close(done)
	}()

	close(stop)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("signal handler did not stop after tray exit")
	}
	select {
	case <-called:
		t.Fatal("normal tray exit requested another exit")
	default:
	}
}
