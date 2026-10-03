package systemd

import (
	"time"

	"github.com/dimonomid/montray"
)

type Config struct {
	UnitRules []ConfigUnitRule `yaml:"unitRules"`
}

type ConfigUnitRule struct {
	// Names, when non-empty, limits the rule to these exact unit names, such as
	// "gpg-agent.service".
	Names []string `yaml:"names"`

	// Type, if not empty, makes the rule to only apply to the units of this type.
	Type string `yaml:"type"`

	// Conditions contains the conditions to check. If none matches, a
	// montray.ItemStateError is assumed.
	Conditions []ConfigCondition `yaml:"conditions"`
}

type ConfigCondition struct {
	// State matches systemd's high-level ActiveState when non-empty.
	State UnitState `yaml:"state"`

	// SubState matches systemd's unit-type-specific SubState when non-empty.
	// Multiple populated fields in one condition must all match.
	SubState string `yaml:"subState"`

	// SubStateContains matches when systemd's SubState contains this text.
	// It is mutually exclusive with SubState.
	SubStateContains string `yaml:"subStateContains"`

	// Resolve controls how an incident created by this condition is resolved.
	// When nil, a later OK condition resolves the incident immediately.
	Resolve *ConfigResolve `yaml:"resolve"`

	// Result is the outcome of the condition if it's true.
	Result montray.ItemState `yaml:"result"`
}

// ConfigResolve defines the recovery policy for an incident created by a
// systemd condition.
type ConfigResolve struct {
	// After is how long the unit must continuously remain in one of States.
	After time.Duration `yaml:"after"`

	// States contains the systemd states that count as recovery.
	States []UnitState `yaml:"states"`
}
