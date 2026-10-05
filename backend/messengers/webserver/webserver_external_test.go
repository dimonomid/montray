package webserver_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dimonomid/clock"
	"github.com/gorilla/websocket"

	"github.com/dimonomid/montray/v2"
	"github.com/dimonomid/montray/v2/backend/itemsboard"
	"github.com/dimonomid/montray/v2/backend/messengers"
	server "github.com/dimonomid/montray/v2/backend/messengers/webserver"
	"github.com/dimonomid/montray/v2/logs"
)

var testLogger = logs.NewLogger(logs.LoggerParams{Clock: clock.New()})

type websocketEnvelope struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

func TestServerPublishesStatusSnapshotAndUpdates(t *testing.T) {
	board := itemsboard.New()
	initial := incident("disk.free", montray.ItemStateError, "full")
	board.Set([]*montray.ItemWContext{initial})
	webserver, notifications, done := startServer(t, board)

	response, err := http.Get("http://" + webserver.Addr().String() + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status response = %s", response.Status)
	}
	var status struct {
		OngoingIncidents []*montray.ItemWContext `json:"ongoingIncidents"`
	}
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	assertIncidentKeys(t, status.OngoingIncidents, "disk.free")

	connection := dialServer(t, webserver, nil)
	initialMessage := readEnvelope(t, connection)
	if initialMessage.Event != "OngoingIncidentsSnapshot" {
		t.Fatalf("initial event = %q", initialMessage.Event)
	}
	assertNotificationTotal(t, initialMessage.Data, "disk.free")

	update := &montray.Notification{
		Time: time.Now(),
		OngoingIncidents: montray.OngoingIncidentsWDelta{
			Total: []*montray.ItemWContext{incident("systemd.sync", montray.ItemStateWarning, "failed")},
		},
	}
	notifications <- update
	updateMessage := readEnvelope(t, connection)
	if updateMessage.Event != "OngoingIncidentsUpdate" {
		t.Fatalf("update event = %q", updateMessage.Event)
	}
	assertNotificationTotal(t, updateMessage.Data, "systemd.sync")

	_ = connection.Close()
	close(notifications)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not shut down")
	}
}

func TestServerRejectsCrossOriginWebsockets(t *testing.T) {
	webserver, notifications, done := startServer(t, itemsboard.New())
	header := http.Header{"Origin": []string{"https://example.invalid"}}
	url := "ws://" + webserver.Addr().String() + "/api/v1/wsconnect"
	connection, response, err := websocket.DefaultDialer.Dial(url, header)
	if connection != nil {
		_ = connection.Close()
	}
	if err == nil {
		t.Fatal("cross-origin websocket was accepted")
	}
	if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("response = %#v, want HTTP 403", response)
	}
	close(notifications)
	<-done
}

func TestServerClosesWebsocketWhenClientSendsMessage(t *testing.T) {
	webserver, notifications, done := startServer(t, itemsboard.New())
	for name, messageType := range map[string]int{
		"text":   websocket.TextMessage,
		"binary": websocket.BinaryMessage,
	} {
		t.Run(name, func(t *testing.T) {
			connection := dialServer(t, webserver, nil)
			_ = readEnvelope(t, connection)

			if err := connection.WriteMessage(messageType, []byte(`{"command":"Authenticate"}`)); err != nil {
				t.Fatal(err)
			}
			_ = connection.SetReadDeadline(time.Now().Add(3 * time.Second))
			if _, _, err := connection.ReadMessage(); !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
				t.Fatalf("read error = %v, want policy-violation close", err)
			}
		})
	}

	close(notifications)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not shut down")
	}
}

func TestServerReportsBindFailure(t *testing.T) {
	first, firstNotifications, firstDone := startServer(t, itemsboard.New())
	notifications := make(chan *montray.Notification)
	done := make(chan struct{})
	_, err := server.New(server.Params{
		Common: messengers.Params{Logger: testLogger, ItemsBoard: itemsboard.New(), NotificationsChan: notifications, TornDown: done},
		Config: server.Config{ListenAddress: first.Addr().String()},
	})
	if err == nil {
		t.Fatal("second server on occupied address started successfully")
	}
	close(firstNotifications)
	<-firstDone
}

func TestServerRejectsInvalidTLSConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		tls     *server.ConfigTLS
		wantErr string
	}{
		{name: "missing certificate", tls: &server.ConfigTLS{}, wantErr: "tls.certFile can't be empty"},
		{name: "missing key", tls: &server.ConfigTLS{CertFile: "certificate.pem"}, wantErr: "tls.keyFile can't be empty"},
		{
			name:    "unreadable certificate pair",
			tls:     &server.ConfigTLS{CertFile: "missing-certificate.pem", KeyFile: "missing-key.pem"},
			wantErr: "loading TLS certificate and key",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := server.New(server.Params{
				Common: messengers.Params{Logger: testLogger},
				Config: server.Config{ListenAddress: "127.0.0.1:0", TLS: test.tls},
			})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("New() error = %v, want it to contain %q", err, test.wantErr)
			}
		})
	}
}

func TestServerRejectsInvalidBearerAuthConfiguration(t *testing.T) {
	validHash := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("valid-token")))
	tests := []struct {
		name    string
		auth    []server.ConfigAuth
		wantErr string
	}{
		{
			name: "missing ID", auth: []server.ConfigAuth{{BearerTokenHash: validHash}},
			wantErr: ".id is required",
		},
		{
			name: "duplicate ID", auth: []server.ConfigAuth{
				{ID: "laptop", BearerTokenHash: validHash}, {ID: "laptop", BearerTokenHash: validHash},
			},
			wantErr: ".id \"laptop\" is duplicated",
		},
		{
			name: "missing authentication method", auth: []server.ConfigAuth{{ID: "laptop"}},
			wantErr: "contains 0 authentication methods; exactly one is required",
		},
		{
			name: "missing hash prefix", auth: []server.ConfigAuth{{ID: "laptop", BearerTokenHash: "abcd"}},
			wantErr: "must start with \"sha256:\"",
		},
		{
			name: "invalid hash", auth: []server.ConfigAuth{{ID: "laptop", BearerTokenHash: "sha256:abcd"}},
			wantErr: "32-byte hexadecimal",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := server.New(server.Params{
				Common: messengers.Params{Logger: testLogger},
				Config: server.Config{ListenAddress: "127.0.0.1:0", Auth: test.auth},
			})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("New() error = %v, want it to contain %q", err, test.wantErr)
			}
		})
	}
}

func TestServerRequiresBearerTokenForEntireAPI(t *testing.T) {
	token := "test-bearer-token"
	tokenHash := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(token)))
	logPath := filepath.Join(t.TempDir(), "webserver.log")
	logger := logs.NewLogger(logs.LoggerParams{
		Clock: clock.New(),
		Sinks: []logs.LoggerSinkParams{{Filepath: logPath, MinLevel: logs.Info}},
	})
	board := itemsboard.New()
	board.Set([]*montray.ItemWContext{incident("disk.free", montray.ItemStateError, "full")})
	notifications := make(chan *montray.Notification)
	done := make(chan struct{})
	webserver, err := server.New(server.Params{
		Common: messengers.Params{Logger: logger, ItemsBoard: board, NotificationsChan: notifications, TornDown: done},
		Config: server.Config{
			ListenAddress: "127.0.0.1:0",
			Auth:          []server.ConfigAuth{{ID: "laptop", BearerTokenHash: tokenHash}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		close(notifications)
		<-done
	})

	statusURL := "http://" + webserver.Addr().String() + "/api/v1/status"
	for _, test := range []struct {
		name       string
		authorizer string
		wantStatus int
	}{
		{name: "missing", wantStatus: http.StatusUnauthorized},
		{name: "wrong", authorizer: "Bearer wrong", wantStatus: http.StatusUnauthorized},
		{name: "valid", authorizer: "Bearer " + token, wantStatus: http.StatusOK},
	} {
		t.Run("status "+test.name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodGet, statusURL, nil)
			if err != nil {
				t.Fatal(err)
			}
			if test.authorizer != "" {
				request.Header.Set("Authorization", test.authorizer)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != test.wantStatus {
				t.Fatalf("status = %s, want %d", response.Status, test.wantStatus)
			}
			if test.wantStatus == http.StatusUnauthorized && response.Header.Get("WWW-Authenticate") != "Bearer" {
				t.Fatalf("WWW-Authenticate = %q, want Bearer", response.Header.Get("WWW-Authenticate"))
			}
		})
	}

	websocketURL := "ws://" + webserver.Addr().String() + "/api/v1/wsconnect"
	if connection, response, err := websocket.DefaultDialer.Dial(websocketURL, nil); err == nil {
		_ = connection.Close()
		t.Fatal("unauthenticated WebSocket connection succeeded")
	} else if response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated WebSocket response = %#v, error = %v", response, err)
	}
	header := http.Header{"Authorization": []string{"Bearer " + token}}
	connection, _, err := websocket.DefaultDialer.Dial(websocketURL, header)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	message := readEnvelope(t, connection)
	if message.Event != "OngoingIncidentsSnapshot" {
		t.Fatalf("initial event = %q", message.Event)
	}
	assertNotificationTotal(t, message.Data, "disk.free")
	logContents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logContents), "WebSocket client 0 connected") || !strings.Contains(string(logContents), "(client_id:laptop)") {
		t.Fatalf("authenticated connection log = %q, want client_id tag", logContents)
	}
}

func TestServerShutdownClosesActiveWebsocket(t *testing.T) {
	webserver, notifications, done := startServer(t, itemsboard.New())
	connection := dialServer(t, webserver, nil)
	_ = readEnvelope(t, connection)

	close(notifications)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not shut down")
	}
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := connection.ReadMessage(); err == nil {
		t.Fatal("websocket remained open after server shutdown")
	}
}

func TestServerShutdownRejectsConcurrentWebsocketSubscriptions(t *testing.T) {
	webserver, notifications, done := startServer(t, itemsboard.New())
	url := "ws://" + webserver.Addr().String() + "/api/v1/wsconnect"
	start := make(chan struct{})
	connections := make(chan *websocket.Conn, 64)
	var attempts sync.WaitGroup
	for i := 0; i < cap(connections); i++ {
		attempts.Add(1)
		go func() {
			defer attempts.Done()
			<-start
			connection, _, err := websocket.DefaultDialer.Dial(url, nil)
			if err == nil {
				connections <- connection
			}
		}()
	}
	close(start)
	close(notifications)
	attempts.Wait()
	close(connections)

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not shut down")
	}
	for connection := range connections {
		_ = connection.SetReadDeadline(time.Now().Add(time.Second))
		for {
			if _, _, err := connection.ReadMessage(); err != nil {
				if netError, ok := err.(net.Error); ok && netError.Timeout() {
					_ = connection.Close()
					t.Fatal("concurrently upgraded websocket survived server shutdown")
				}
				break
			}
		}
		_ = connection.Close()
	}
}

func startServer(t *testing.T, board *itemsboard.ItemsBoard) (*server.Webserver, chan *montray.Notification, chan struct{}) {
	t.Helper()
	notifications := make(chan *montray.Notification, 4)
	done := make(chan struct{})
	webserver, err := server.New(server.Params{
		Common: messengers.Params{Logger: testLogger, ItemsBoard: board, NotificationsChan: notifications, TornDown: done},
		Config: server.Config{ListenAddress: "127.0.0.1:0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return webserver, notifications, done
}

func dialServer(t *testing.T, webserver *server.Webserver, header http.Header) *websocket.Conn {
	t.Helper()
	url := "ws://" + webserver.Addr().String() + "/api/v1/wsconnect"
	connection, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		t.Fatal(err)
	}
	return connection
}

func readEnvelope(t *testing.T, connection *websocket.Conn) websocketEnvelope {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(3 * time.Second))
	var message websocketEnvelope
	if err := connection.ReadJSON(&message); err != nil {
		t.Fatal(err)
	}
	return message
}

func assertNotificationTotal(t *testing.T, data json.RawMessage, keys ...string) {
	t.Helper()
	var notification montray.Notification
	if err := json.Unmarshal(data, &notification); err != nil {
		t.Fatal(err)
	}
	assertIncidentKeys(t, notification.OngoingIncidents.Total, keys...)
}

func assertIncidentKeys(t *testing.T, incidents []*montray.ItemWContext, keys ...string) {
	t.Helper()
	got := make([]string, 0, len(incidents))
	for _, value := range incidents {
		got = append(got, string(value.Key))
	}
	if strings.Join(got, ",") != strings.Join(keys, ",") {
		t.Fatalf("incident keys = %#v, want %#v", got, keys)
	}
}

func incident(key montray.ItemKey, state montray.ItemState, details string) *montray.ItemWContext {
	return &montray.ItemWContext{Item: montray.Item{Key: key, State: state, Details: details}, IncidentStartedAt: time.Now()}
}
