package core_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/benbjohnson/clock"

	"github.com/dimonomid/montray/v2"
	"github.com/dimonomid/montray/v2/backend/collectors/exec"
	"github.com/dimonomid/montray/v2/backend/collectors/systemd"
	"github.com/dimonomid/montray/v2/backend/core"
	"github.com/dimonomid/montray/v2/backend/messengers/filelogger"
	"github.com/dimonomid/montray/v2/backend/messengers/webserver"
	"github.com/dimonomid/montray/v2/logs"
)

var testLogger = logs.NewLogger(logs.LoggerParams{Clock: clock.New()})

func TestCorePublishesCommandIncidentLifecycle(t *testing.T) {
	directory := t.TempDir()
	probePath := directory + "/probe-state"
	logPath := directory + "/events.log"
	applicationLogPath := directory + "/montray.log"
	clk := clock.New()
	applicationLogger := logs.NewLogger(logs.LoggerParams{
		Clock: clk,
		Sinks: []logs.LoggerSinkParams{{Filepath: applicationLogPath, MinLevel: logs.Info}},
	})
	if err := os.WriteFile(probePath, []byte("failed\n"), 0600); err != nil {
		t.Fatal(err)
	}

	monitor, err := core.NewCore(core.Config{
		Collectors: []core.Collector{
			{
				ID: "probe",
				Exec: &exec.Config{
					Description:               "probe must be healthy",
					Command:                   []string{"sh", "-c", `test "$(cat "$1")" = healthy`, "sh", probePath},
					PollInterval:              10 * time.Millisecond,
					PollIntervalWhenUnhealthy: 10 * time.Millisecond,
				},
			},
			{
				ID: "second-probe",
				Exec: &exec.Config{
					Command:                   []string{"sh", "-c", "exit 8"},
					PollInterval:              time.Hour,
					PollIntervalWhenUnhealthy: time.Hour,
					Conditions:                []exec.ConfigCondition{{Result: montray.ItemStateWarning}},
				},
			},
		},
		Messengers: []core.Messenger{{FileLogger: &filelogger.Config{FileName: logPath}}},
	}, core.Params{Clock: clk, Logger: applicationLogger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(monitor.Close)

	waitForFileText(t, logPath, "[ error ] probe.exec_result")
	waitForFileText(t, logPath, "[ warning ] second-probe.exec_result")
	waitForFileText(t, applicationLogPath, "Incident started: probe.exec_result (error)")
	waitForFileText(t, applicationLogPath, "Incident started: second-probe.exec_result (warning)")
	if err := os.WriteFile(probePath, []byte("healthy\n"), 0600); err != nil {
		t.Fatal(err)
	}
	waitForFileText(t, logPath, "[ ok ] probe.exec_result")
	waitForFileText(t, applicationLogPath, "Incident resolved: probe.exec_result: probe must be healthy: exit code: 0")

	done := make(chan struct{})
	go func() {
		monitor.Close()
		monitor.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Core.Close did not complete")
	}
}

func TestCoreRejectsAmbiguousComponentConfiguration(t *testing.T) {
	validExec := &exec.Config{Command: []string{"true"}, Conditions: []exec.ConfigCondition{{Result: montray.ItemStateOK}}}
	validSystemd := &systemd.Config{}
	_, err := core.NewCore(core.Config{Collectors: []core.Collector{{
		ID: "ambiguous", Exec: validExec, Systemd: validSystemd,
	}}}, core.Params{Clock: clock.New(), Logger: testLogger})
	if err == nil || !strings.Contains(err.Error(), "exactly one collector type") {
		t.Fatalf("ambiguous collector error = %v", err)
	}

	_, err = core.NewCore(core.Config{Messengers: []core.Messenger{{
		FileLogger: &filelogger.Config{}, Webserver: &webserver.Config{ListenAddress: "127.0.0.1:0"},
	}}}, core.Params{Clock: clock.New(), Logger: testLogger})
	if err == nil || !strings.Contains(err.Error(), "exactly one messenger type") {
		t.Fatalf("ambiguous messenger error = %v", err)
	}
}

func TestCoreRejectsDuplicateAndInvalidCollectorConfiguration(t *testing.T) {
	validExec := func() *exec.Config {
		return &exec.Config{Command: []string{"true"}, Conditions: []exec.ConfigCondition{{Result: montray.ItemStateOK}}}
	}
	tests := []struct {
		name       string
		collectors []core.Collector
		want       string
	}{
		{
			name: "duplicate IDs",
			collectors: []core.Collector{
				{ID: "same", Exec: validExec()},
				{ID: "same", Exec: validExec()},
			},
			want: "duplicate id",
		},
		{
			name:       "empty command",
			collectors: []core.Collector{{ID: "probe", Exec: &exec.Config{}}},
			want:       "command must not be empty",
		},
		{
			name: "invalid result",
			collectors: []core.Collector{{
				ID: "probe",
				Exec: &exec.Config{
					Command:    []string{"true"},
					Conditions: []exec.ConfigCondition{{Result: "banana"}},
				},
			}},
			want: "invalid result",
		},
		{
			name: "invalid interval",
			collectors: []core.Collector{{
				ID: "probe",
				Exec: &exec.Config{
					Command:      []string{"true"},
					PollInterval: -time.Second,
				},
			}},
			want: "must not be negative",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := core.NewCore(core.Config{Collectors: test.collectors}, core.Params{Clock: clock.New(), Logger: testLogger})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want text %q", err, test.want)
			}
		})
	}
}

func TestCoreRequiresClock(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != "Clock is required" {
			t.Fatalf("panic = %#v, want %q", recovered, "Clock is required")
		}
	}()

	_, _ = core.NewCore(core.Config{}, core.Params{})
}

func waitForFileText(t *testing.T, path, text string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), text) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	data, _ := os.ReadFile(path)
	t.Fatalf("timed out waiting for %q in %q", text, data)
}
