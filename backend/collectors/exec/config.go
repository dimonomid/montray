package exec

import (
	"time"

	"github.com/dimonomid/montray"
)

type Config struct {
	// Description explains what the check verifies. It prefixes the dynamic
	// execution details published with the item.
	Description string `yaml:"description"`

	// Command is a slice of strings: first one is the command, all others will be
	// arguments. Example: []string{"bash", "-c", "(( $(df --output=avail / | sed 1d) > 1000000 ))"}
	// The first line written to stdout is used as the item's dynamic details;
	// when stdout is empty, the exit code is used instead. Long lines are
	// truncated to keep command output from consuming unbounded memory.
	Command []string `yaml:"command"`

	// PollInterval is how often to run the command. Default: 1 minute.
	PollInterval time.Duration `yaml:"pollInterval"`

	PollIntervalWhenUnhealthy time.Duration `yaml:"pollIntervalWhenUnhealthy"`

	// Timeout limits one command execution. When omitted, it defaults to the
	// shortest of one minute, PollInterval, and PollIntervalWhenUnhealthy. An
	// explicit timeout must not exceed either polling interval.
	Timeout time.Duration `yaml:"timeout"`

	// Conditions contains the conditions to check. When omitted, exit code 0 is
	// OK and every other exit code is an error. If configured conditions do not
	// match, a montray.ItemStateError is assumed. An explicit empty list is invalid.
	Conditions []ConfigCondition `yaml:"conditions"`
}

type ConfigCondition struct {
	// If ExitCode isn't an empty string, it's interpreted as an int, and the process
	// must exit with that code for the condition to be true.
	// Otherwise, it's ignored.
	//
	// It's a string and not an int just to make the zero value to be
	// distinguishable from 0, which is a valid exit code.
	ExitCode string `yaml:"exitCode"`

	// Result is the outcome of the condition if it's true.
	Result montray.ItemState `yaml:"result"`
}
