package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/dimonomid/montray/v2"
)

func TestSnoozeDeadlineUsesWallClock(t *testing.T) {
	state := &snoozeState{
		path:    filepath.Join(t.TempDir(), "state.json"),
		snoozed: make(map[string]snoozeEntry),
	}
	now := time.Now()
	if now == now.Round(0) {
		t.Fatal("test clock does not contain a monotonic reading")
	}

	if err := state.Snooze("server.disk", time.Hour, now); err != nil {
		t.Fatal(err)
	}
	deadline := state.snoozed["server.disk"].SnoozedUntil
	if deadline != deadline.Round(0) {
		t.Fatal("snooze deadline retained a monotonic clock reading")
	}
	if want := now.Round(0).Add(time.Hour); !deadline.Equal(want) {
		t.Fatalf("snooze deadline = %s, want %s", deadline, want)
	}
}

func TestExpiredSnoozeCallbackSkipsResolvedIncident(t *testing.T) {
	state := &incidentState{}
	called := false
	state.OnSnoozeExpired = func(item montray.ItemWContext) {
		called = true
	}

	state.ongoingIncidents.Set([]*montray.ItemWContext{{
		Item: montray.Item{Key: "server.disk"},
	}})
	state.ongoingIncidents.Set(nil) // The incident resolved while snoozed.
	state.notifySnoozeExpired([]string{"server.disk"})

	if called {
		t.Fatal("resolved incident generated a snooze-expiration callback")
	}
}
