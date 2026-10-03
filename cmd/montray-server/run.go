package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/benbjohnson/clock"

	"github.com/dimonomid/montray/v2/backend/core"
	"github.com/dimonomid/montray/v2/internal/setup"
	"github.com/dimonomid/montray/v2/logs"
)

// runMontrayServer loads the configuration and runs the monitoring core until a
// termination signal arrives.
func runMontrayServer(configFilename string, minLogLevel logs.LogLevel) error {
	cfg, loadedConfigFilename, err := loadRuntimeConfig(configFilename, legacySalmonConfig)
	if err != nil {
		return montrayServerConfigReadError(configFilename, err)
	}

	clk := clock.New()
	logger := logs.NewLogger(logs.LoggerParams{
		Clock: clk,
		Sinks: []logs.LoggerSinkParams{{MinLevel: minLogLevel}},
	}).WithNamespaceAppended("MontrayServer")
	if loadedConfigFilename != configFilename {
		logger.Log(logs.Info, "Using pre-rename configuration at %s", loadedConfigFilename)
	}
	c, err := core.NewCore(cfg.Core, core.Params{Clock: clk, Logger: logger})
	if err != nil {
		return fmt.Errorf("failed to initialize Montray Server core: %w", err)
	}

	logger.Log(logs.Info, "Monitoring started")
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	sig := <-sigCh
	logger.Log(logs.Info, "Received %s; shutting down", sig)
	c.Close()
	logger.Log(logs.Info, "Shutdown complete")
	return nil
}

func loadRuntimeConfig(configFilename, legacyFilename string) (*config, string, error) {
	return loadRuntimeConfigWithFallback(
		configFilename,
		legacyFilename,
		configFilename == defaultMontrayServerConfig,
	)
}

func loadRuntimeConfigWithFallback(configFilename, legacyFilename string, allowLegacy bool) (*config, string, error) {
	cfg, err := loadConfig(configFilename)
	if err == nil || !allowLegacy || !configNotFound(err) {
		return cfg, configFilename, err
	}
	legacyConfig, legacyErr := loadConfig(legacyFilename)
	if legacyErr == nil {
		return legacyConfig, legacyFilename, nil
	}
	return nil, configFilename, err
}

// montrayServerConfigReadError adds setup guidance when the default configuration is
// missing.
func montrayServerConfigReadError(configFilename string, err error) error {
	if configNotFound(err) && configFilename == defaultMontrayServerConfig {
		executable := setup.ShellArgument(os.Args[0])
		return fmt.Errorf("failed to read config from %s: %w\n\nHint: Run the following command to create the default configuration, install the service, and start it:\n\n    sudo %s setup\n\nTo create only the default configuration without installing the service, run:\n\n    sudo %s setup create-config\n", configFilename, err, executable, executable)
	}
	return fmt.Errorf("failed to read config from %s: %w", configFilename, err)
}
