package filelogger_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/benbjohnson/clock"

	"github.com/dimonomid/montray"
	"github.com/dimonomid/montray/backend/itemsboard"
	"github.com/dimonomid/montray/backend/messengers"
	"github.com/dimonomid/montray/backend/messengers/filelogger"
	"github.com/dimonomid/montray/logs"
)

func TestLoggerWritesObservableIncidentTransitions(t *testing.T) {
	path := t.TempDir() + "/events.log"
	notifications := make(chan *montray.Notification, 1)
	done := make(chan struct{})
	_, err := filelogger.New(filelogger.Params{
		Common: messengers.Params{
			Logger:            logs.NewLogger(logs.LoggerParams{Clock: clock.New()}),
			ItemsBoard:        itemsboard.New(),
			NotificationsChan: notifications,
			TornDown:          done,
		},
		Config: filelogger.Config{FileName: path},
	})
	if err != nil {
		t.Fatal(err)
	}

	incident := &montray.ItemWContext{Item: montray.Item{Key: "systemd.sync", State: montray.ItemStateError, Details: "failed"}}
	notifications <- &montray.Notification{
		Time: time.Date(2026, 8, 23, 12, 34, 56, 0, time.Local),
		OngoingIncidents: montray.OngoingIncidentsWDelta{
			Added:   []*montray.ItemWContext{incident},
			Updated: []*montray.ItemWContext{{Item: montray.Item{Key: "disk.free", State: montray.ItemStateWarning, Details: "low"}}},
			Removed: []*montray.ItemWContext{{Item: montray.Item{Key: "network.dns", State: montray.ItemStateError}}},
		},
	}
	close(notifications)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("logger did not shut down")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[ ok ] network.dns",
		"[ error ] systemd.sync (failed)",
		"[ warning ][ updated ] disk.free (low)",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("log %q does not contain %q", data, want)
		}
	}
}
