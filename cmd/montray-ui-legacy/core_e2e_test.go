package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/gorilla/websocket"

	"github.com/dimonomid/montray"
	"github.com/dimonomid/montray/wsclient"
)

type recordedNotification struct {
	Title string
	Text  string
}

type recordingNotificator struct {
	mu      sync.Mutex
	records []recordedNotification
}

func (n *recordingNotificator) waitForCount(t *testing.T, count int) []string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(n.titles()) >= count {
			return n.titles()
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d notifications; got %#v", count, n.titles())
	return nil
}

func (n *recordingNotificator) Push(title, text string) {
	n.mu.Lock()
	n.records = append(n.records, recordedNotification{Title: title, Text: text})
	n.mu.Unlock()
}

func (n *recordingNotificator) titles() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	titles := make([]string, 0, len(n.records))
	for _, record := range n.records {
		titles = append(titles, record.Title)
	}
	return titles
}

func (n *recordingNotificator) texts() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	texts := make([]string, 0, len(n.records))
	for _, record := range n.records {
		texts = append(texts, record.Text)
	}
	return texts
}

type mockMontrayServer struct {
	server      *httptest.Server
	mu          sync.Mutex
	connections map[*websocket.Conn]struct{}
	connected   chan struct{}
}

func newMockMontrayServer(t *testing.T) *mockMontrayServer {
	t.Helper()
	mock := &mockMontrayServer{
		connections: make(map[*websocket.Conn]struct{}),
		connected:   make(chan struct{}, 16),
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		mock.mu.Lock()
		mock.connections[conn] = struct{}{}
		mock.mu.Unlock()
		mock.connected <- struct{}{}
		for {
			if _, _, err := conn.NextReader(); err != nil {
				mock.mu.Lock()
				delete(mock.connections, conn)
				mock.mu.Unlock()
				_ = conn.Close()
				return
			}
		}
	})
	mock.server = newLocalTestServer(handler)
	t.Cleanup(mock.server.Close)
	return mock
}

func newLocalTestServer(handler http.Handler) *httptest.Server {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	return server
}

func (m *mockMontrayServer) address() string {
	return strings.TrimPrefix(m.server.URL, "http://")
}

func (m *mockMontrayServer) waitConnected(t *testing.T) {
	t.Helper()
	select {
	case <-m.connected:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Montray UI Legacy connection")
	}
}

func (m *mockMontrayServer) send(t *testing.T, notification montray.Notification) {
	t.Helper()
	payload, err := json.Marshal(struct {
		Event string               `json:"event"`
		Data  montray.Notification `json:"data"`
	}{Event: "OngoingIncidentsUpdate", Data: notification})
	if err != nil {
		t.Fatal(err)
	}

	m.mu.Lock()
	connections := make([]*websocket.Conn, 0, len(m.connections))
	for conn := range m.connections {
		connections = append(connections, conn)
	}
	m.mu.Unlock()
	if len(connections) == 0 {
		t.Fatal("mock Montray Server has no connected Montray UI Legacy client")
	}
	for _, conn := range connections {
		if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			t.Fatal(err)
		}
	}
}

func (m *mockMontrayServer) closeConnections() {
	m.mu.Lock()
	connections := make([]*websocket.Conn, 0, len(m.connections))
	for conn := range m.connections {
		connections = append(connections, conn)
	}
	m.mu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
}

func (m *mockMontrayServer) sendHeartbeat(t *testing.T) {
	t.Helper()
	m.mu.Lock()
	connections := make([]*websocket.Conn, 0, len(m.connections))
	for conn := range m.connections {
		connections = append(connections, conn)
	}
	m.mu.Unlock()
	for _, conn := range connections {
		if err := conn.WriteMessage(websocket.BinaryMessage, []byte{0x00}); err != nil {
			t.Fatal(err)
		}
	}
}

type statusMessage struct {
	OngoingIncidents struct {
		Alerting []montray.ItemWContext `json:"alerting"`
		Snoozed  []snoozedIncident      `json:"snoozed"`
	} `json:"ongoingIncidents"`
	Servers []serverStatus `json:"servers"`
}

func connectStatus(t *testing.T, serverURL string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(serverURL, "http") + "/api/v1/wsconnect"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func readStatus(t *testing.T, conn *websocket.Conn) statusMessage {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var message statusMessage
	if err := conn.ReadJSON(&message); err != nil {
		t.Fatal(err)
	}
	return message
}

func readStatusUntil(t *testing.T, conn *websocket.Conn, predicate func(statusMessage) bool) statusMessage {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		var message statusMessage
		if err := conn.ReadJSON(&message); err != nil {
			t.Fatal(err)
		}
		if predicate(message) {
			return message
		}
	}
	t.Fatal("timed out waiting for expected status message")
	return statusMessage{}
}

func hasAlertingKey(message statusMessage, key string) bool {
	for _, incident := range message.OngoingIncidents.Alerting {
		if string(incident.Key) == key {
			return true
		}
	}
	return false
}

// alertingIncident finds key in the alerting incidents carried by a decoded
// status WebSocket message and reports whether it was present.
func alertingIncident(message statusMessage, key string) (montray.ItemWContext, bool) {
	for _, incident := range message.OngoingIncidents.Alerting {
		if string(incident.Key) == key {
			return incident, true
		}
	}
	return montray.ItemWContext{}, false
}

func item(key, details string) *montray.ItemWContext {
	return &montray.ItemWContext{
		Item:              montray.Item{Key: montray.ItemKey(key), State: montray.ItemStateError, Details: details},
		IncidentStartedAt: time.Now(),
	}
}

func TestCoreCombinesTwoMontrayServers(t *testing.T) {
	// Model two independent Montray Servers; Montray UI Legacy must combine their
	// incidents and prefix each key with the configured server ID.
	first := newMockMontrayServer(t)
	second := newMockMontrayServer(t)
	notifications := &recordingNotificator{}
	core, err := newMontrayLegacyCore(montrayLegacyCoreParams{
		Config: wsclient.Config{Servers: []wsclient.ConfigServer{
			{ID: "second", Addr: second.address()},
			{ID: "first", Addr: first.address()},
		}},
		StatePath:      t.TempDir() + "/state.json",
		Notifications:  notifications,
		Clock:          clock.New(),
		Logger:         watchTestLogger,
		ReconnectDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Close)
	status := newLocalTestServer(rootHandler(core.statusWebserver))
	t.Cleanup(status.Close)
	statusConn := connectStatus(t, status.URL)
	// A newly connected browser receives the current, initially empty snapshot.
	_ = readStatus(t, statusConn)
	first.waitConnected(t)
	second.waitConnected(t)
	first.sendHeartbeat(t)
	second.sendHeartbeat(t)
	serverStatus := readStatusUntil(t, statusConn, func(message statusMessage) bool {
		return len(message.Servers) == 2 && message.Servers[0].LastHeartbeatTime != nil && message.Servers[1].LastHeartbeatTime != nil
	})
	if got, want := []string{serverStatus.Servers[0].ID, serverStatus.Servers[1].ID}, []string{"second", "first"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("server status order = %#v, want configuration order %#v", got, want)
	}

	// Both servers report a newly added error. Each should produce a status update
	// and one desktop notification, with the server prefix included in the key.
	first.send(t, montray.Notification{OngoingIncidents: montray.OngoingIncidentsWDelta{
		Total: []*montray.ItemWContext{item("disk", "first")},
		Added: []*montray.ItemWContext{item("disk", "first")},
	}})
	second.send(t, montray.Notification{OngoingIncidents: montray.OngoingIncidentsWDelta{
		Total: []*montray.ItemWContext{item("cpu", "second")},
		Added: []*montray.ItemWContext{item("cpu", "second")},
	}})

	// The two Montray Server messages produce two Montray UI Legacy WebSocket updates. The
	// second one must contain the combined incidents from both servers.
	message := readStatusUntil(t, statusConn, func(message statusMessage) bool {
		return hasAlertingKey(message, "first.disk") && hasAlertingKey(message, "second.cpu")
	})
	keys := map[string]bool{}
	for _, incident := range message.OngoingIncidents.Alerting {
		keys[string(incident.Key)] = true
	}
	if !keys["first.disk"] || !keys["second.cpu"] {
		t.Fatalf("combined status missing incidents: %#v", keys)
	}
	if len(message.Servers) != 2 || !message.Servers[0].Connected || !message.Servers[1].Connected {
		t.Fatalf("unexpected server connection status: %#v", message.Servers)
	}
	// Notification order is intentionally not asserted because the two source
	// WebSockets are concurrent.
	// Receiving the combined status does not guarantee that both desktop
	// notifications have been delivered: status and notification delivery are
	// separate asynchronous outputs.
	gotTitles := notifications.waitForCount(t, 2)
	gotTitleSet := map[string]bool{}
	for _, title := range gotTitles {
		gotTitleSet[title] = true
	}
	if len(gotTitles) != 2 || !gotTitleSet["error: first.disk"] || !gotTitleSet["error: second.cpu"] {
		t.Fatalf("unexpected notifications: %#v", gotTitles)
	}

	// Model a volatile update to first.disk. The browser must receive the new
	// details, but desktop notifications remain suppressed for updates.
	updated := item("disk", "first changed")
	first.send(t, montray.Notification{OngoingIncidents: montray.OngoingIncidentsWDelta{
		Total:   []*montray.ItemWContext{updated},
		Updated: []*montray.ItemWContext{updated},
	}})
	message = readStatusUntil(t, statusConn, func(message statusMessage) bool {
		for _, incident := range message.OngoingIncidents.Alerting {
			if incident.Key == "first.disk" && incident.Details == "first changed" {
				return true
			}
		}
		return false
	})
	foundUpdated := false
	for _, incident := range message.OngoingIncidents.Alerting {
		if incident.Key == "first.disk" && incident.Details == "first changed" {
			foundUpdated = true
		}
	}
	if !foundUpdated {
		t.Fatalf("status did not contain updated incident: %#v", message.OngoingIncidents.Alerting)
	}
	if got := notifications.titles(); len(got) != 2 {
		t.Fatalf("incident update generated a notification: %#v", got)
	}

	// Removing a normal incident should publish its removal and notify the user
	// that the incident recovered.
	first.send(t, montray.Notification{OngoingIncidents: montray.OngoingIncidentsWDelta{
		Removed: []*montray.ItemWContext{updated},
	}})
	_ = readStatusUntil(t, statusConn, func(message statusMessage) bool {
		return !hasAlertingKey(message, "first.disk")
	})
	gotTitles = notifications.waitForCount(t, 3)
	if !gotTitleSet["OK: first.disk"] && !contains(gotTitles, "OK: first.disk") {
		t.Fatalf("incident recovery notification missing: %#v", gotTitles)
	}

	// Model one Montray Server going offline. Montray UI Legacy should expose the
	// connection failure as an internal incident and notify about it.
	second.closeConnections()
	message = readStatusUntil(t, statusConn, func(message statusMessage) bool {
		incident, found := alertingIncident(message, "second.cpu")
		return found && incident.Stale && hasAlertingKey(message, "internal.connection.second")
	})
	internalError := false
	for _, incident := range message.OngoingIncidents.Alerting {
		if incident.Key == "internal.connection.second" && incident.State == montray.ItemStateError {
			internalError = true
		}
	}
	if !internalError {
		t.Fatalf("connection failure incident missing: %#v", message.OngoingIncidents.Alerting)
	}
	gotTitles = notifications.waitForCount(t, 4)
	if len(gotTitles) != 4 || !contains(gotTitles, "error: internal.connection.second") {
		t.Fatalf("unexpected notifications after connection failure: %#v", gotTitles)
	}

	// The client reconnects and clears the internal connection incident, but the
	// retained incident stays stale until the server supplies a new snapshot.
	second.waitConnected(t)
	message = readStatusUntil(t, statusConn, func(message statusMessage) bool {
		incident, found := alertingIncident(message, "second.cpu")
		connected := false
		for _, server := range message.Servers {
			if server.ID == "second" {
				connected = server.Connected
			}
		}
		return connected && found && incident.Stale &&
			!hasAlertingKey(message, "internal.connection.second")
	})
	for _, incident := range message.OngoingIncidents.Alerting {
		if incident.Key == "internal.connection.second" {
			t.Fatalf("connection failure incident was not cleared: %#v", message.OngoingIncidents.Alerting)
		}
	}
	notifications.waitForCount(t, 5)

	// Forgetting removes the stale cached incident without pretending it
	// recovered: there must be no OK desktop notification for second.cpu.
	request, err := json.Marshal(map[string]string{"key": "second.cpu"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(status.URL+"/api/v1/forget", "application/json", bytes.NewReader(request))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("forget returned HTTP %d", response.StatusCode)
	}
	_ = response.Body.Close()
	_ = readStatusUntil(t, statusConn, func(message statusMessage) bool {
		return !hasAlertingKey(message, "second.cpu")
	})
	if got := notifications.titles(); len(got) != 5 || contains(got, "OK: second.cpu") {
		t.Fatalf("forget generated a recovery notification: %#v", got)
	}

	// A new snapshot from the reconnected server restores the forgotten incident
	// as fresh when the source still reports it.
	second.send(t, montray.Notification{OngoingIncidents: montray.OngoingIncidentsWDelta{
		Total: []*montray.ItemWContext{item("cpu", "second refreshed")},
	}})
	message = readStatusUntil(t, statusConn, func(message statusMessage) bool {
		incident, found := alertingIncident(message, "second.cpu")
		return found && !incident.Stale && incident.Details == "second refreshed"
	})

	// An empty source snapshot replaces and removes its stale/fresh cache alike.
	second.send(t, montray.Notification{OngoingIncidents: montray.OngoingIncidentsWDelta{}})
	_ = readStatusUntil(t, statusConn, func(message statusMessage) bool {
		return !hasAlertingKey(message, "second.cpu")
	})
}

func TestConnectedEventClearsPreviousHeartbeat(t *testing.T) {
	previousHeartbeat := time.Now().Add(-2 * time.Hour)
	statusWebserver := newStatusWebserver(statusWebserverParams{Logger: watchTestLogger})
	core := &montrayLegacyCore{
		statusWebserver: statusWebserver,
		incidentState: &incidentState{
			snoozes: &snoozeState{snoozed: make(map[string]snoozeEntry)},
			clock:   clock.New(),
		},
		serverStatuses: map[string]serverStatus{
			"server": {
				ID:                   "server",
				LastHeartbeatTime:    &previousHeartbeat,
				hasConnectedOrFailed: true,
			},
		},
		serverIDs: []string{"server"},
	}

	core.onConnectionEvent("server", wsclient.ConnectionEvent{
		EventKind: wsclient.EventKindConnected,
		Time:      time.Now(),
	})

	status := core.serverStatuses["server"]
	if !status.Connected {
		t.Fatal("server is not marked connected")
	}
	if status.LastHeartbeatTime != nil {
		t.Fatalf("last heartbeat = %v, want nil after connecting", status.LastHeartbeatTime)
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestCoreSnoozedIncidentDoesNotNotify(t *testing.T) {
	// Model a user snoozing an incident before it appears in a Montray Server snapshot.
	// The incident should still be visible in the status UI, but must not alert.
	montrayServer := newMockMontrayServer(t)
	notifications := &recordingNotificator{}
	core, err := newMontrayLegacyCore(montrayLegacyCoreParams{
		Config:         wsclient.Config{Servers: []wsclient.ConfigServer{{ID: "server", Addr: montrayServer.address()}}},
		StatePath:      t.TempDir() + "/state.json",
		Notifications:  notifications,
		Clock:          clock.New(),
		Logger:         watchTestLogger,
		ReconnectDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Close)
	status := newLocalTestServer(rootHandler(core.statusWebserver))
	t.Cleanup(status.Close)
	statusConn := connectStatus(t, status.URL)
	_ = readStatus(t, statusConn)
	montrayServer.waitConnected(t)

	// Snoozing is performed through the same HTTP API used by the real browser.
	request, err := json.Marshal(map[string]string{"key": "server.disk", "duration": "1h"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(status.URL+"/api/v1/snooze", "application/json", bytes.NewReader(request))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("snooze returned HTTP %d", response.StatusCode)
	}
	_ = response.Body.Close()

	// Montray Server now reports the incident as newly added. Montray UI Legacy should classify
	// it as snoozed in the WebSocket payload and emit no desktop notification.
	incident := item("disk", "network is down")
	montrayServer.send(t, montray.Notification{OngoingIncidents: montray.OngoingIncidentsWDelta{
		Total: []*montray.ItemWContext{incident},
		Added: []*montray.ItemWContext{incident},
	}})
	message := readStatusUntil(t, statusConn, func(message statusMessage) bool {
		return len(message.OngoingIncidents.Snoozed) == 1
	})
	if len(message.OngoingIncidents.Snoozed) != 1 || string(message.OngoingIncidents.Snoozed[0].Key) != "server.disk" {
		t.Fatalf("unexpected snoozed status: %#v", message.OngoingIncidents)
	}
	if got := notifications.titles(); len(got) != 0 {
		t.Fatalf("snoozed incident generated notifications: %#v", got)
	}

	// A snoozed incident recovering must disappear silently, without an OK
	// notification.
	montrayServer.send(t, montray.Notification{OngoingIncidents: montray.OngoingIncidentsWDelta{
		Removed: []*montray.ItemWContext{incident},
	}})
	message = readStatusUntil(t, statusConn, func(message statusMessage) bool {
		return len(message.OngoingIncidents.Snoozed) == 0
	})
	if len(message.OngoingIncidents.Snoozed) != 0 {
		t.Fatalf("removed snoozed incident is still shown: %#v", message.OngoingIncidents.Snoozed)
	}
	if got := notifications.titles(); len(got) != 0 {
		t.Fatalf("snoozed recovery generated notifications: %#v", got)
	}

	// Reintroduce it while the snooze is still active so unsnoozing can verify
	// the immediate transition into the alerting list.
	montrayServer.send(t, montray.Notification{OngoingIncidents: montray.OngoingIncidentsWDelta{
		Total: []*montray.ItemWContext{incident},
	}})

	// Unsnoozing should immediately move the same incident back to alerting in
	// the browser payload; this action itself does not create a desktop alert.
	request, err = json.Marshal(map[string]string{"key": "server.disk"})
	if err != nil {
		t.Fatal(err)
	}
	response, err = http.Post(status.URL+"/api/v1/unsnooze", "application/json", bytes.NewReader(request))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("unsnooze returned HTTP %d", response.StatusCode)
	}
	_ = response.Body.Close()
	message = readStatusUntil(t, statusConn, func(message statusMessage) bool {
		return hasAlertingKey(message, "server.disk")
	})
	if len(message.OngoingIncidents.Alerting) != 1 || string(message.OngoingIncidents.Alerting[0].Key) != "server.disk" {
		t.Fatalf("unexpected unsnoozed status: %#v", message.OngoingIncidents)
	}
	if got := notifications.titles(); len(got) != 0 {
		t.Fatalf("manual unsnooze generated notifications: %#v", got)
	}
}

func TestCoreSnoozeExpirationPublishesUpdate(t *testing.T) {
	// Advance a mock clock to cover expiration without waiting for wall time.
	montrayServer := newMockMontrayServer(t)
	mockClock := clock.NewMock()
	notifications := &recordingNotificator{}
	core, err := newMontrayLegacyCore(montrayLegacyCoreParams{
		Config:              wsclient.Config{Servers: []wsclient.ConfigServer{{ID: "server", Addr: montrayServer.address()}}},
		StatePath:           t.TempDir() + "/state.json",
		Notifications:       notifications,
		Clock:               mockClock,
		Logger:              watchTestLogger,
		SnoozeCheckInterval: time.Minute,
		ReconnectDelay:      time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Close)
	status := newLocalTestServer(rootHandler(core.statusWebserver))
	t.Cleanup(status.Close)
	statusConn := connectStatus(t, status.URL)
	_ = readStatus(t, statusConn)
	montrayServer.waitConnected(t)

	incident := item("disk", "network is down")
	montrayServer.send(t, montray.Notification{OngoingIncidents: montray.OngoingIncidentsWDelta{
		Total: []*montray.ItemWContext{incident},
	}})
	_ = readStatusUntil(t, statusConn, func(message statusMessage) bool {
		return hasAlertingKey(message, "server.disk")
	})

	// Snoozing moves the active incident to the snoozed section immediately.
	const snoozeDuration = 15 * time.Minute
	if err := core.incidentState.Snooze("server.disk", snoozeDuration); err != nil {
		t.Fatal(err)
	}
	message := readStatusUntil(t, statusConn, func(message statusMessage) bool {
		return len(message.OngoingIncidents.Snoozed) == 1
	})
	if len(message.OngoingIncidents.Snoozed) != 1 {
		t.Fatalf("incident was not snoozed: %#v", message.OngoingIncidents)
	}
	if got, want := message.OngoingIncidents.Snoozed[0].SnoozedUntil, mockClock.Now().Add(snoozeDuration); !got.Equal(want) {
		t.Fatalf("snooze expiration = %s, want %s", got, want)
	}

	// Once mock time reaches the expiry, the periodic watcher must publish a new
	// snapshot that moves the incident back to alerting.
	mockClock.Add(snoozeDuration)
	message = readStatusUntil(t, statusConn, func(message statusMessage) bool {
		return hasAlertingKey(message, "server.disk")
	})
	if len(message.OngoingIncidents.Alerting) != 1 || string(message.OngoingIncidents.Alerting[0].Key) != "server.disk" {
		t.Fatalf("expired snooze did not return to alerting: %#v", message.OngoingIncidents)
	}
	if got := notifications.waitForCount(t, 1); !reflect.DeepEqual(got, []string{"Snooze ended: server.disk"}) {
		t.Fatalf("expiration notifications = %#v", got)
	}
	if got := notifications.texts()[0]; got != "network is down" {
		t.Fatalf("expiration notification details = %q", got)
	}
}

func TestCoreCloseIsIdempotent(t *testing.T) {
	// Closing the core twice must be safe and must return without leaving the
	// Montray Server connection worker running.
	montrayServer := newMockMontrayServer(t)
	core, err := newMontrayLegacyCore(montrayLegacyCoreParams{
		Config:         wsclient.Config{Servers: []wsclient.ConfigServer{{ID: "server", Addr: montrayServer.address()}}},
		StatePath:      t.TempDir() + "/state.json",
		Clock:          clock.New(),
		Logger:         watchTestLogger,
		ReconnectDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	montrayServer.waitConnected(t)
	core.Close()
	core.Close()
	select {
	case <-core.incidentState.done:
	default:
		t.Fatal("core close did not stop the incident-state worker")
	}
}

func TestCoreRequiresClock(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != "Clock is required" {
			t.Fatalf("panic = %#v, want %q", recovered, "Clock is required")
		}
	}()

	_, _ = newMontrayLegacyCore(montrayLegacyCoreParams{})
}

func TestStatusWebSocketReconnectReceivesLatestSnapshot(t *testing.T) {
	// Model a browser disconnecting and reconnecting after Montray UI Legacy has
	// already received an incident. The new connection should get the latest
	// snapshot immediately, without waiting for another Montray Server message.
	montrayServer := newMockMontrayServer(t)
	core, err := newMontrayLegacyCore(montrayLegacyCoreParams{
		Config:         wsclient.Config{Servers: []wsclient.ConfigServer{{ID: "server", Addr: montrayServer.address()}}},
		StatePath:      t.TempDir() + "/state.json",
		Clock:          clock.New(),
		Logger:         watchTestLogger,
		ReconnectDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Close)
	status := newLocalTestServer(rootHandler(core.statusWebserver))
	t.Cleanup(status.Close)
	firstConnection := connectStatus(t, status.URL)
	_ = readStatus(t, firstConnection)
	montrayServer.waitConnected(t)

	incident := item("disk", "network is down")
	montrayServer.send(t, montray.Notification{OngoingIncidents: montray.OngoingIncidentsWDelta{
		Total: []*montray.ItemWContext{incident},
	}})
	_ = readStatus(t, firstConnection)
	_ = firstConnection.Close()

	secondConnection := connectStatus(t, status.URL)
	message := readStatusUntil(t, secondConnection, func(message statusMessage) bool {
		return hasAlertingKey(message, "server.disk")
	})
	if len(message.OngoingIncidents.Alerting) != 1 || string(message.OngoingIncidents.Alerting[0].Key) != "server.disk" {
		t.Fatalf("reconnected browser did not receive latest snapshot: %#v", message.OngoingIncidents)
	}
}
