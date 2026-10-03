package exec_test

import (
	"strings"
	"testing"
	"time"

	"github.com/benbjohnson/clock"

	"github.com/dimonomid/montray/v2"
	"github.com/dimonomid/montray/v2/backend/collectors"
	execcollector "github.com/dimonomid/montray/v2/backend/collectors/exec"
	"github.com/dimonomid/montray/v2/logs"
)

var testLogger = logs.NewLogger(logs.LoggerParams{Clock: clock.New()})

func TestCollectorMapsCommandResultsToItems(t *testing.T) {
	tests := []struct {
		name        string
		description string
		command     []string
		conditions  []execcollector.ConfigCondition
		wantState   montray.ItemState
		wantText    string
		wantAbsent  []string
	}{
		{
			name:       "default conditions accept exit zero",
			command:    []string{"sh", "-c", "exit 0"},
			wantState:  montray.ItemStateOK,
			wantText:   "exit code: 0",
			wantAbsent: []string{"condition"},
		},
		{
			name:       "default conditions reject nonzero exit",
			command:    []string{"sh", "-c", "exit 9"},
			wantState:  montray.ItemStateError,
			wantText:   "exit code: 9",
			wantAbsent: []string{"condition"},
		},
		{
			name:        "matching exit code",
			description: "probe",
			command:     []string{"sh", "-c", "printf 'disk full\\nignored\\n'; exit 7"},
			conditions: []execcollector.ConfigCondition{
				{ExitCode: "0", Result: montray.ItemStateOK},
				{ExitCode: "7", Result: montray.ItemStateWarning},
			},
			wantState: montray.ItemStateWarning,
			wantText:  "probe: disk full",
			wantAbsent: []string{
				"ignored",
				"condition",
				"exit code",
			},
		},
		{
			name:       "unmatched exit code defaults to error",
			command:    []string{"sh", "-c", "exit 9"},
			conditions: []execcollector.ConfigCondition{{ExitCode: "0", Result: montray.ItemStateOK}},
			wantState:  montray.ItemStateError,
			wantText:   "exit code: 9",
			wantAbsent: []string{"condition"},
		},
		{
			name:       "stderr is not used as details",
			command:    []string{"sh", "-c", "printf 'stderr details\\n' >&2; exit 7"},
			conditions: []execcollector.ConfigCondition{{Result: montray.ItemStateError}},
			wantState:  montray.ItemStateError,
			wantText:   "exit code: 7",
			wantAbsent: []string{"stderr details", "condition"},
		},
		{
			name:       "command start failure is an incident",
			command:    []string{"/a/montray-command-that-does-not-exist"},
			conditions: []execcollector.ConfigCondition{{Result: montray.ItemStateOK}},
			wantState:  montray.ItemStateError,
			wantText:   "failed to start command",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			updates := make(chan *collectors.Update, 1)
			collector, err := execcollector.NewCollector(execcollector.CollectorParams{
				Common: collectors.Params{ID: "check", Logger: testLogger, UpdatesChan: updates},
				Config: execcollector.Config{
					Description:               test.description,
					Command:                   test.command,
					PollInterval:              time.Hour,
					PollIntervalWhenUnhealthy: time.Hour,
					Conditions:                test.conditions,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(collector.Close)

			update := receiveUpdate(t, updates)
			got := update.Items["check.exec_result"]
			if got == nil {
				t.Fatalf("update = %#v, want check.exec_result", update)
			}
			if got.State != test.wantState {
				t.Errorf("state = %q, want %q", got.State, test.wantState)
			}
			if !strings.Contains(got.Details, test.wantText) {
				t.Errorf("details = %q, want it to contain %q", got.Details, test.wantText)
			}
			for _, absent := range test.wantAbsent {
				if strings.Contains(got.Details, absent) {
					t.Errorf("details = %q, want it not to contain %q", got.Details, absent)
				}
			}
			if test.description != "" && !strings.HasPrefix(got.Details, test.description+": ") {
				t.Errorf("details = %q, want description prefix %q", got.Details, test.description+": ")
			}
			if strings.HasPrefix(got.Details, ":") {
				t.Errorf("details without description have a leading colon: %q", got.Details)
			}
		})
	}
}

func TestCollectorRejectsExplicitlyEmptyConditions(t *testing.T) {
	collector, err := execcollector.NewCollector(execcollector.CollectorParams{
		Common: collectors.Params{ID: "check", Logger: testLogger, UpdatesChan: make(chan *collectors.Update)},
		Config: execcollector.Config{
			Command:    []string{"true"},
			Conditions: []execcollector.ConfigCondition{},
		},
	})
	if collector != nil {
		collector.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "conditions must not be empty") {
		t.Fatalf("error = %v, want explicit-empty-conditions error", err)
	}
}

func TestCollectorRejectsInvalidTimeout(t *testing.T) {
	tests := []struct {
		name      string
		config    execcollector.Config
		wantError string
	}{
		{
			name: "negative timeout",
			config: execcollector.Config{
				Timeout: -time.Second,
			},
			wantError: "timeout must not be negative",
		},
		{
			name: "timeout exceeds normal interval",
			config: execcollector.Config{
				PollInterval:              10 * time.Millisecond,
				PollIntervalWhenUnhealthy: 30 * time.Millisecond,
				Timeout:                   20 * time.Millisecond,
			},
			wantError: "must not exceed pollInterval",
		},
		{
			name: "timeout exceeds unhealthy interval",
			config: execcollector.Config{
				PollInterval:              30 * time.Millisecond,
				PollIntervalWhenUnhealthy: 10 * time.Millisecond,
				Timeout:                   20 * time.Millisecond,
			},
			wantError: "must not exceed pollIntervalWhenUnhealthy",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.config.Command = []string{"true"}
			collector, err := execcollector.NewCollector(execcollector.CollectorParams{
				Common: collectors.Params{ID: "check", Logger: testLogger, UpdatesChan: make(chan *collectors.Update)},
				Config: test.config,
			})
			if collector != nil {
				collector.Close()
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want it to contain %q", err, test.wantError)
			}
		})
	}
}

func TestCollectorReportsTimeoutAndContinuesPolling(t *testing.T) {
	marker := t.TempDir() + "/first-attempt"
	updates := make(chan *collectors.Update, 2)
	collector, err := execcollector.NewCollector(execcollector.CollectorParams{
		Common: collectors.Params{ID: "check", Logger: testLogger, UpdatesChan: updates},
		Config: execcollector.Config{
			Command: []string{
				"sh", "-c",
				`if [ -e "$1" ]; then exit 0; fi; : > "$1"; exec sleep 30`,
				"sh", marker,
			},
			PollInterval:              time.Hour,
			PollIntervalWhenUnhealthy: 80 * time.Millisecond,
			Timeout:                   40 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(collector.Close)

	first := receiveUpdate(t, updates).Items["check.exec_result"]
	if first == nil || first.State != montray.ItemStateError || !strings.Contains(first.Details, "Command timed out after 40ms") {
		t.Fatalf("first result = %#v, want timeout error", first)
	}
	second := receiveUpdate(t, updates).Items["check.exec_result"]
	if second == nil || second.State != montray.ItemStateOK {
		t.Fatalf("second result = %#v, want recovered OK result", second)
	}
}

func TestCollectorCloseCancelsAStuckCommand(t *testing.T) {
	updates := make(chan *collectors.Update)
	collector, err := execcollector.NewCollector(execcollector.CollectorParams{
		Common: collectors.Params{ID: "check", Logger: testLogger, UpdatesChan: updates},
		Config: execcollector.Config{
			Command:                   []string{"sh", "-c", "sleep 30"},
			PollInterval:              time.Hour,
			PollIntervalWhenUnhealthy: time.Hour,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	done := make(chan struct{})
	go func() {
		collector.Close()
		collector.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not cancel the active command")
	}
}

func receiveUpdate(t *testing.T, updates <-chan *collectors.Update) *collectors.Update {
	t.Helper()
	select {
	case update := <-updates:
		return update
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for collector update")
		return nil
	}
}
