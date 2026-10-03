package setup

import "testing"

func TestShellArgument(t *testing.T) {
	for _, test := range []struct {
		argument string
		want     string
	}{
		{"montray", "montray"},
		{"/tmp/my config.yml", "'/tmp/my config.yml'"},
		{"$HOME/montray.yml", "'$HOME/montray.yml'"},
		{"it's.yml", "'it'\"'\"'s.yml'"},
		{"", "''"},
	} {
		if got := ShellArgument(test.argument); got != test.want {
			t.Errorf("ShellArgument(%q) = %q, want %q", test.argument, got, test.want)
		}
	}
}
