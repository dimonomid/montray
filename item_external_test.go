package montray_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dimonomid/montray/v2"
)

func TestIsItemStateValid(t *testing.T) {
	for _, state := range []montray.ItemState{
		montray.ItemStateOK,
		montray.ItemStateWarning,
		montray.ItemStateError,
	} {
		if !montray.IsItemStateValid(state) {
			t.Errorf("IsItemStateValid(%q) = false, want true", state)
		}
	}

	for _, state := range []montray.ItemState{"", "unknown", "OK"} {
		if montray.IsItemStateValid(state) {
			t.Errorf("IsItemStateValid(%q) = true, want false", state)
		}
	}
}

func TestItemJSONUsesDetails(t *testing.T) {
	data, err := json.Marshal(montray.Item{Key: "probe", State: montray.ItemStateError, Details: "command failed"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"details":"command failed"`) {
		t.Fatalf("item JSON = %s, want details field", data)
	}
	if strings.Contains(string(data), `"comment"`) {
		t.Fatalf("item JSON = %s, contains obsolete comment field", data)
	}
}
