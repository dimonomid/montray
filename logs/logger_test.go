package logs_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/benbjohnson/clock"

	"github.com/dimonomid/montray/v2/logs"
)

func TestLoggerFormatsNamespaceContextAndLevel(t *testing.T) {
	clk := clock.NewMock()
	clk.Set(time.Date(2026, 8, 27, 12, 34, 56, 123000000, time.FixedZone("test", 2*60*60)))
	path := t.TempDir() + "/montray.log"
	logger := logs.NewLogger(logs.LoggerParams{
		Clock: clk,
		Sinks: []logs.LoggerSinkParams{{Filepath: path, MinLevel: logs.Info}},
	}).WithNamespaceAppended("Montray").WithNamespaceAppended("Core").
		WithContext("z", "last").WithContext("a", "first")

	logger.Log(logs.Debug, "hidden")
	logger.Log(logs.Info, "Monitoring %s", "started")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	want := "2026-08-27 10:34:56.123 [I] [Montray/Core] Monitoring started (a:first, z:last)\n"
	if got != want {
		t.Fatalf("log = %q, want %q", got, want)
	}
	if strings.Contains(got, "hidden") {
		t.Fatalf("log contains message below sink level: %q", got)
	}
}

func TestParseLogLevel(t *testing.T) {
	for value, want := range map[string]logs.LogLevel{
		"debug":   logs.Debug,
		"info":    logs.Info,
		"warn":    logs.Warning,
		"warning": logs.Warning,
		"error":   logs.Error,
	} {
		got, err := logs.ParseLogLevel(value)
		if err != nil {
			t.Errorf("ParseLogLevel(%q) returned an error: %v", value, err)
		} else if got != want {
			t.Errorf("ParseLogLevel(%q) = %v, want %v", value, got, want)
		}
	}
	if _, err := logs.ParseLogLevel("verbose"); err == nil {
		t.Fatal("ParseLogLevel accepted an invalid level")
	}
}

func TestLoggerFormatsAllLevels(t *testing.T) {
	clk := clock.NewMock()
	path := t.TempDir() + "/montray.log"
	logger := logs.NewLogger(logs.LoggerParams{
		Clock: clk,
		Sinks: []logs.LoggerSinkParams{{Filepath: path, MinLevel: logs.Debug}},
	})

	logger.Log(logs.Debug, "debug")
	logger.Log(logs.Info, "info")
	logger.Log(logs.Warning, "warning")
	logger.Log(logs.Error, "error")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[D] debug", "[I] info", "[W] warning", "[E] error"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("log %q does not contain %q", data, want)
		}
	}
}
