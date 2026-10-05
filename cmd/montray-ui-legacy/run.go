package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"

	"github.com/dimonomid/clock"
	"github.com/getlantern/systray"
	"github.com/skratchdot/open-golang/open"

	"github.com/dimonomid/montray/v2/internal/setup"
	"github.com/dimonomid/montray/v2/logs"
)

// watchApp owns the configuration and lifecycle state of one tray instance.
type watchApp struct {
	config       *config
	configPath   string
	clock        clock.Clock
	logger       *logs.Logger
	core         *montrayLegacyCore
	statusServer *localStatusServer
	restart      atomic.Bool
}

// run starts the tray and turns SIGINT or SIGTERM into a normal tray exit, so
// onExit gets a chance to tear down every owned resource.
func (app *watchApp) run() bool {
	terminationSignals := make(chan os.Signal, 1)
	signal.Notify(terminationSignals, syscall.SIGINT, syscall.SIGTERM)
	stopSignalHandler := make(chan struct{})
	go waitForWatchTerminationSignal(terminationSignals, stopSignalHandler, func(sig os.Signal) {
		// Restore the default handling before teardown starts, so a second signal
		// can still terminate the process if graceful shutdown gets stuck.
		signal.Stop(terminationSignals)
		app.logger.Log(logs.Info, "Received %s; shutting down", sig)
		systray.Quit()
	})

	// systray.Run must be the only operation that runs the tray app: it locks
	// its OS thread before invoking onReady.
	systray.Run(app.onReady, app.onExit)

	signal.Stop(terminationSignals)
	close(stopSignalHandler)
	return app.restart.Load()
}

// waitForWatchTerminationSignal invokes onSignal for the first termination
// signal, or returns when the tray has already exited normally.
func waitForWatchTerminationSignal(
	signals <-chan os.Signal,
	stop <-chan struct{},
	onSignal func(os.Signal),
) {
	select {
	case sig := <-signals:
		onSignal(sig)
	case <-stop:
	}
}

// onReady initializes the tray application after systray has locked its OS
// thread.
func (app *watchApp) onReady() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		panic(err.Error())
	}

	mitemStatus := systray.AddMenuItem(trayStatusTitle(trayState{}), "")
	mitemRestart := systray.AddMenuItem("Restart and reload configuration", "")
	mitemExit := systray.AddMenuItem("Exit", "")

	notify := newDesktopNotificationSink()

	loadTrayIcons()

	applyIcon(trayState{Alerting: overallStateUnknown})
	canonicalStatePath := filepath.Join(homeDir, stateFilename)
	legacyStatePath := filepath.Join(homeDir, legacyStateFilename)
	copiedLegacyState, err := preserveLegacyState(canonicalStatePath, legacyStatePath)
	if err != nil {
		app.logger.Log(logs.Error, "Failed to migrate legacy state: %s", err)
		os.Exit(1)
	}
	if copiedLegacyState {
		app.logger.Log(logs.Info, "Copied legacy state from %s to %s", canonicalStatePath, legacyStatePath)
	}
	app.core, err = newMontrayLegacyCore(montrayLegacyCoreParams{
		Config:        app.config.WSClient,
		StatePath:     legacyStatePath,
		Notifications: notify,
		Clock:         app.clock,
		Logger:        app.logger,
		OnIconState: func(state trayState) {
			applyIcon(state)
			mitemStatus.SetTitle(trayStatusTitle(state))
		},
	})
	if err != nil {
		app.logger.Log(logs.Error, "Failed to start: %s", err)
		os.Exit(1)
	}

	app.statusServer = setupWebserver(app.core.statusWebserver)
	port := app.statusServer.Addr().(*net.TCPAddr).Port

	app.logger.Log(logs.Info, "Status UI is available at http://localhost:%d/status", port)

	go func() {
		if err := app.statusServer.Serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			app.logger.Log(logs.Error, "Status webserver stopped unexpectedly: %s", err)
			systray.Quit()
		}
	}()

	go func() {
		for {
			select {
			case <-mitemStatus.ClickedCh:
				open.Run(fmt.Sprintf("http://localhost:%d/status", port))

			case <-mitemRestart.ClickedCh:
				if !restartConfigIsValid(app.configPath, notify, app.logger) {
					continue
				}
				app.restart.Store(true)
				systray.Quit()

			case <-mitemExit.ClickedCh:
				systray.Quit()
			}
		}
	}()
}

// restartConfigIsValid keeps the working instance alive when an edited config
// cannot be loaded, and reports the reason through both normal user channels.
func restartConfigIsValid(configPath string, notify notificator, logger *logs.Logger) bool {
	if _, err := loadConfig(configPath); err != nil {
		logger.Log(logs.Error, "Configuration reload failed: %s", err)
		notify.Push("Configuration reload failed", err.Error())
		return false
	}
	return true
}

// watchConfigReadError adds setup guidance when the default configuration is
// missing.
func watchConfigReadError(configFilename string, err error) error {
	defaultConfigFilename, defaultConfigErr := defaultWatchConfigPath()
	if configNotFound(err) && defaultConfigErr == nil && configFilename == defaultConfigFilename {
		executable := setup.ShellArgument(os.Args[0])
		return fmt.Errorf("failed to read config from %s: %w\n\nHint: Run the following command to create the default configuration, desktop-autostart entry, and application launcher:\n\n    %s setup\n\nTo create only the default configuration without installing the desktop integration, run:\n\n    %s setup create-config\n", configFilename, err, executable, executable)
	}
	return fmt.Errorf("failed to read config from %s: %w", configFilename, err)
}

// onExit shuts down Montray UI Legacy resources after the tray exits.
func (app *watchApp) onExit() {
	app.logger.Log(logs.Info, "Shutting down")
	if app.statusServer != nil {
		if err := app.statusServer.Close(); err != nil {
			app.logger.Log(logs.Error, "Failed to close status webserver: %s", err)
		}
	}
	if app.core != nil {
		app.core.Close()
	}
	app.logger.Log(logs.Info, "Shutdown complete")
}
