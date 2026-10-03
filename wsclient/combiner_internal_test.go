package wsclient

import (
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/benbjohnson/clock"

	"github.com/dimonomid/montray"
	"github.com/dimonomid/montray/logs"
	"github.com/dimonomid/montray/statestracker"
)

func TestResolveTunnelAddressesAllocatesOmittedAddress(t *testing.T) {
	config := Config{Servers: []ConfigServer{
		{ID: "automatic", Tunnel: &ConfigTunnel{SSH: &ConfigSSHTunnel{
			Host: "example.com", User: "montray", RemoteServerAddr: "127.0.0.1:41990",
		}}},
		{ID: "explicit", Addr: "localhost:41992"},
	}}

	resolved, automatic, err := resolveTunnelAddresses(config)
	if err != nil {
		t.Fatal(err)
	}
	address, err := net.ResolveTCPAddr("tcp4", resolved.Servers[0].Addr)
	if err != nil {
		t.Fatalf("allocated address %q is invalid: %v", resolved.Servers[0].Addr, err)
	}
	if !address.IP.IsLoopback() || address.Port == 0 {
		t.Fatalf("allocated address = %v, want nonzero loopback port", address)
	}
	if resolved.Servers[1].Addr != "localhost:41992" {
		t.Fatalf("explicit address changed to %q", resolved.Servers[1].Addr)
	}
	if !automatic["automatic"] || automatic["explicit"] {
		t.Fatalf("automatic address set = %#v", automatic)
	}
	if config.Servers[0].Addr != "" {
		t.Fatalf("input config was mutated: %#v", config.Servers[0])
	}
	command, err := TunnelCommand(resolved.Servers[0])
	if err != nil {
		t.Fatal(err)
	}
	wantForward := resolved.Servers[0].Addr + ":127.0.0.1:41990"
	if !containsString(command.Command, wantForward) {
		t.Fatalf("SSH command = %#v, want forward %q", command.Command, wantForward)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func combinerTestIncident(key string, stale bool) *montray.ItemWContext {
	return &montray.ItemWContext{
		Item:  montray.Item{Key: montray.ItemKey(key), State: montray.ItemStateError},
		Stale: stale,
	}
}

// incidentWithKey finds an incident in a combined snapshot by its prefixed key.
func incidentWithKey(items []*montray.ItemWContext, key string) *montray.ItemWContext {
	for _, item := range items {
		if item != nil && string(item.Key) == key {
			return item
		}
	}
	return nil
}

func TestCombinerMarksOnlyDisconnectedServerIncidentsStale(t *testing.T) {
	var notifications []*montray.Notification
	combiner := &Combiner{
		params: CombinerParams{OngoingIncidentsHandler: func(notification *montray.Notification) {
			notifications = append(notifications, notification)
		}},
		totalByID: map[string][]*montray.ItemWContext{
			"first":  {combinerTestIncident("first.disk", false)},
			"second": {combinerTestIncident("second.cpu", false)},
		},
	}
	original := combiner.totalByID["first"][0]
	eventTime := time.Now()

	combiner.markServerIncidentsStale("first", eventTime)
	if len(notifications) != 1 {
		t.Fatalf("got %d notifications, want 1", len(notifications))
	}
	notification := notifications[0]
	if !notification.Time.Equal(eventTime) {
		t.Fatalf("notification time = %v, want %v", notification.Time, eventTime)
	}
	if incident := incidentWithKey(notification.OngoingIncidents.Total, "first.disk"); incident == nil || !incident.Stale {
		t.Fatalf("first.disk = %#v, want stale", incident)
	}
	if incident := incidentWithKey(notification.OngoingIncidents.Total, "second.cpu"); incident == nil || incident.Stale {
		t.Fatalf("second.cpu = %#v, want non-stale", incident)
	}
	if len(notification.OngoingIncidents.Updated) != 1 || !notification.OngoingIncidents.Updated[0].Stale {
		t.Fatalf("updated incidents = %#v, want one stale incident", notification.OngoingIncidents.Updated)
	}
	if original.Stale {
		t.Fatal("marking stale mutated a previously published incident")
	}

	combiner.markServerIncidentsStale("first", eventTime.Add(time.Second))
	if len(notifications) != 1 {
		t.Fatalf("repeated stale mark produced %d notifications, want 1", len(notifications))
	}
}

func TestCombinerSourceSnapshotReplacesStaleIncidents(t *testing.T) {
	var latest *montray.Notification
	combiner := &Combiner{
		params: CombinerParams{OngoingIncidentsHandler: func(notification *montray.Notification) {
			latest = notification
		}},
		totalByID: map[string][]*montray.ItemWContext{
			"first":  {combinerTestIncident("first.disk", true)},
			"second": {combinerTestIncident("second.cpu", true)},
		},
	}

	combiner.applyNotification("first", &montray.Notification{OngoingIncidents: montray.OngoingIncidentsWDelta{
		Total: []*montray.ItemWContext{combinerTestIncident("first.disk", false)},
	}})
	if incident := incidentWithKey(latest.OngoingIncidents.Total, "first.disk"); incident == nil || incident.Stale {
		t.Fatalf("replacement first.disk = %#v, want non-stale", incident)
	}
	if incident := incidentWithKey(latest.OngoingIncidents.Total, "second.cpu"); incident == nil || !incident.Stale {
		t.Fatalf("cached second.cpu = %#v, want stale", incident)
	}

	combiner.applyNotification("second", &montray.Notification{})
	if incident := incidentWithKey(latest.OngoingIncidents.Total, "second.cpu"); incident != nil {
		t.Fatalf("empty source snapshot retained second.cpu: %#v", incident)
	}
}

func TestCombinerForgetsOnlyStaleIncidentWithoutResolutionDelta(t *testing.T) {
	clk := clock.NewMock()
	var notifications []*montray.Notification
	combiner := &Combiner{
		params: CombinerParams{
			Clock:  clk,
			Logger: logs.NewLogger(logs.LoggerParams{Clock: clk}),
			OngoingIncidentsHandler: func(notification *montray.Notification) {
				notifications = append(notifications, notification)
			},
		},
		totalByID: map[string][]*montray.ItemWContext{
			"first": {
				combinerTestIncident("first.stale", true),
				combinerTestIncident("first.fresh", false),
			},
			"second": {combinerTestIncident("second.stale", true)},
		},
	}

	if !combiner.ForgetStaleIncident("first.stale") {
		t.Fatal("stale incident was not forgotten")
	}
	if len(notifications) != 1 {
		t.Fatalf("got %d notifications, want 1", len(notifications))
	}
	notification := notifications[0]
	if incidentWithKey(notification.OngoingIncidents.Total, "first.stale") != nil {
		t.Fatalf("forgotten incident remains in total: %#v", notification.OngoingIncidents.Total)
	}
	if incidentWithKey(notification.OngoingIncidents.Total, "first.fresh") == nil ||
		incidentWithKey(notification.OngoingIncidents.Total, "second.stale") == nil {
		t.Fatalf("forget removed an unrelated incident: %#v", notification.OngoingIncidents.Total)
	}
	if len(notification.OngoingIncidents.Added) != 0 ||
		len(notification.OngoingIncidents.Removed) != 0 ||
		len(notification.OngoingIncidents.Updated) != 0 {
		t.Fatalf("forget notification contains a resolution delta: %#v", notification.OngoingIncidents)
	}

	if combiner.ForgetStaleIncident("first.stale") {
		t.Fatal("already-forgotten incident was reported forgotten again")
	}
	if combiner.ForgetStaleIncident("first.fresh") {
		t.Fatal("non-stale incident was forgotten")
	}
	if len(notifications) != 1 {
		t.Fatalf("rejected forget published %d notifications, want 1", len(notifications))
	}

	combiner.applyNotification("first", &montray.Notification{OngoingIncidents: montray.OngoingIncidentsWDelta{
		Total: []*montray.ItemWContext{combinerTestIncident("first.stale", false)},
	}})
	if incident := incidentWithKey(notifications[len(notifications)-1].OngoingIncidents.Total, "first.stale"); incident == nil || incident.Stale {
		t.Fatalf("source snapshot did not restore forgotten incident as fresh: %#v", incident)
	}
}

func TestGetPrefixedItemPreservesStale(t *testing.T) {
	item := getPrefixedItem(combinerTestIncident("disk", true), "bridge")
	if item.Key != "bridge.disk" || !item.Stale {
		t.Fatalf("prefixed incident = %#v, want bridge.disk stale", item)
	}
}

func TestGetPrefixedNotifRejectsNullItem(t *testing.T) {
	_, err := getPrefixedNotif(&montray.Notification{OngoingIncidents: montray.OngoingIncidentsWDelta{
		Total: []*montray.ItemWContext{nil},
	}}, "server")
	if err == nil {
		t.Fatal("null incident was accepted")
	}
}

func TestCombinerReportsTunnelFailureAsInternalIncident(t *testing.T) {
	notifications := make(chan *montray.Notification, 8)
	logPath := t.TempDir() + "/watch.log"
	combiner, err := NewCombiner(CombinerParams{
		Config: Config{Servers: []ConfigServer{{
			ID:   "remote",
			Addr: "127.0.0.1:41992",
			Tunnel: &ConfigTunnel{CustomCommand: &ConfigCustomTunnelCommand{
				Command:        []string{"sh", "-c", "exit 7"},
				ReadinessProbe: &ConfigTunnelReadinessProbe{ContainsOutput: "ready"},
			}},
		}}},
		Logger: logs.NewLogger(logs.LoggerParams{
			Clock: clock.New(),
			Sinks: []logs.LoggerSinkParams{{Filepath: logPath, MinLevel: logs.Info}},
		}),
		Clock: clock.New(),
		OngoingIncidentsHandler: func(notification *montray.Notification) {
			notifications <- notification
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(combiner.Close)

	select {
	case notification := <-notifications:
		incident := incidentWithKey(notification.OngoingIncidents.Total, "internal.tunnel.remote")
		if incident == nil || incident.State != montray.ItemStateError || incident.Details == "" {
			t.Fatalf("notification = %#v, want tunnel failure incident", notification)
		}
		if incidentWithKey(notification.OngoingIncidents.Total, "internal.connection.remote") != nil {
			t.Fatalf("notification = %#v, unexpectedly contains connection incident", notification)
		}
		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		if want := "[Combiner/Tunnel] Starting tunnel with sh (server_id:remote)"; !strings.Contains(string(data), want) {
			t.Fatalf("log %q does not contain %q", data, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tunnel failure did not produce an internal incident")
	}
}

func TestCombinerKeepsReconnectedSnapshotFreshWhenEventsAreQueued(t *testing.T) {
	tests := []struct {
		name   string
		events []ServerEvent
	}{
		{
			name: "WebSocket reconnect",
			events: []ServerEvent{
				{Kind: ServerEventKindConnection, Connection: ConnectionEvent{EventKind: EventKindDisconnected}},
				{Kind: ServerEventKindConnection, Connection: ConnectionEvent{EventKind: EventKindConnected}},
			},
		},
		{
			name: "tunnel restart",
			events: []ServerEvent{
				{Kind: ServerEventKindTunnel, Tunnel: TunnelEvent{Kind: TunnelEventFailed, Error: "tunnel failed"}},
				{Kind: ServerEventKindTunnel, Tunnel: TunnelEvent{Kind: TunnelEventReady}},
				{Kind: ServerEventKindConnection, Connection: ConnectionEvent{EventKind: EventKindConnected}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clk := clock.NewMock()
			updates := make(chan *montray.Notification, 16)
			statuses := make(chan ConnectionEvent, 4)
			combiner := &Combiner{
				params: CombinerParams{
					Clock:  clk,
					Logger: logs.NewLogger(logs.LoggerParams{Clock: clk}),
					OngoingIncidentsHandler: func(notification *montray.Notification) {
						updates <- notification
					},
					ConnectionStatusHandler: func(_ string, event ConnectionEvent) {
						statuses <- event
					},
				},
				internalTracker: statestracker.NewItemStatesTracker(statestracker.ItemStatesTrackerParams{Clock: clk}),
				totalByID: map[string][]*montray.ItemWContext{
					"remote": {combinerTestIncident("remote.disk", false)},
				},
				closeDone: make(chan struct{}),
			}

			events := make(chan ServerEvent, len(test.events)+1)
			for _, event := range test.events {
				event.Connection.Time = clk.Now()
				event.Tunnel.Time = clk.Now()
				events <- event
			}
			events <- ServerEvent{
				Kind: ServerEventKindOngoingIncidents,
				OngoingIncidents: &montray.Notification{OngoingIncidents: montray.OngoingIncidentsWDelta{
					Total: []*montray.ItemWContext{combinerTestIncident("disk", false)},
				}},
			}

			done := make(chan struct{})
			go func() {
				combiner.runWSClient(ConfigServer{ID: "remote"}, events)
				close(done)
			}()

			deadline := time.After(3 * time.Second)
			for {
				select {
				case notification := <-updates:
					incident := incidentWithKey(notification.OngoingIncidents.Total, "remote.disk")
					if incident != nil && !incident.Stale {
						var latestStatus ConnectionEvent
						for i := 0; i < 2; i++ {
							select {
							case latestStatus = <-statuses:
							case <-time.After(3 * time.Second):
								t.Fatal("connection status was not published")
							}
						}
						if latestStatus.EventKind != EventKindConnected {
							t.Fatalf("latest connection status = %s, want connected", latestStatus.EventKind)
						}
						close(combiner.closeDone)
						select {
						case <-done:
						case <-time.After(3 * time.Second):
							t.Fatal("combiner event loop did not stop")
						}
						return
					}
				case <-deadline:
					t.Fatal("reconnected snapshot was not published as fresh")
				}
			}
		})
	}
}
