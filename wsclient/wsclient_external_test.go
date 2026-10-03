package wsclient_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/benbjohnson/clock"

	"github.com/dimonomid/montray/v2"
	"github.com/dimonomid/montray/v2/logs"
	"github.com/dimonomid/montray/v2/wsclient"
	"github.com/gorilla/websocket"
)

var testLogger = logs.NewLogger(logs.LoggerParams{Clock: clock.New()})

func TestClientLogsReceivedServerIncidentTotal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		_ = connection.WriteJSON(map[string]interface{}{
			"event": "OngoingIncidentsSnapshot",
			"data": montray.Notification{OngoingIncidents: montray.OngoingIncidentsWDelta{
				Total: []*montray.ItemWContext{{Item: montray.Item{
					Key: "disk", State: montray.ItemStateError, Details: "almost full",
				}}},
			}},
		})
		for {
			if _, _, err := connection.NextReader(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	logPath := t.TempDir() + "/watch.log"
	logger := logs.NewLogger(logs.LoggerParams{
		Clock: clock.New(),
		Sinks: []logs.LoggerSinkParams{{Filepath: logPath, MinLevel: logs.Info}},
	})
	events := make(chan wsclient.ServerEvent, 16)
	client, err := wsclient.New(wsclient.Params{
		Config:         wsclient.ConfigServer{ID: "test", Addr: strings.TrimPrefix(server.URL, "http://")},
		Logger:         logger,
		EventCh:        events,
		ReconnectDelay: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)

	deadline := time.After(3 * time.Second)
	received := false
	for !received {
		select {
		case event := <-events:
			if event.Kind == wsclient.ServerEventKindOngoingIncidents {
				received = true
			}
		case <-deadline:
			t.Fatal("client did not receive the incident snapshot")
		}
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(data)
	for _, want := range []string{
		"[I] [WSClient] Received incident snapshot; ongoing incidents:",
		`"key":"disk"`,
		`"details":"almost full"`,
	} {
		if !strings.Contains(logText, want) {
			t.Errorf("log %q does not contain %q", logText, want)
		}
	}
}

func TestClientRejectsUnusableCAFile(t *testing.T) {
	invalidCAFile := t.TempDir() + "/invalid-ca.pem"
	if err := os.WriteFile(invalidCAFile, []byte("not a certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		caFile  string
		wantErr string
	}{
		{name: "missing", caFile: t.TempDir() + "/missing-ca.pem", wantErr: "read TLS CA file"},
		{name: "invalid", caFile: invalidCAFile, wantErr: "contains no valid certificates"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := wsclient.New(wsclient.Params{
				Config:  wsclient.ConfigServer{ID: "test", Addr: "127.0.0.1:41990", TLS: &wsclient.ConfigTLS{CAFile: test.caFile}},
				Logger:  testLogger,
				EventCh: make(chan wsclient.ServerEvent, 1),
			})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("New() error = %v, want it to contain %q", err, test.wantErr)
			}
		})
	}
}

func TestClientRejectsUnusableBearerTokenFile(t *testing.T) {
	directory := t.TempDir()
	emptyFile := directory + "/empty.token"
	if err := os.WriteFile(emptyFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	whitespaceFile := directory + "/whitespace.token"
	if err := os.WriteFile(whitespaceFile, []byte("part one"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		tokenFile string
		wantErr   string
	}{
		{name: "missing", tokenFile: directory + "/missing.token", wantErr: "read bearer token file"},
		{name: "empty", tokenFile: emptyFile, wantErr: "is empty"},
		{name: "internal whitespace", tokenFile: whitespaceFile, wantErr: "contains whitespace"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := wsclient.New(wsclient.Params{
				Config: wsclient.ConfigServer{
					ID: "test", Addr: "127.0.0.1:41990",
					Auth: &wsclient.ConfigAuth{BearerTokenFile: test.tokenFile},
				},
				Logger:  testLogger,
				EventCh: make(chan wsclient.ServerEvent, 1),
			})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("New() error = %v, want it to contain %q", err, test.wantErr)
			}
		})
	}
}

func TestClientDisconnectsFromMalformedServerMessages(t *testing.T) {
	tests := []struct {
		name            string
		message         string
		wantError       string
		allowWriteError bool
	}{
		{name: "malformed envelope", message: `{"event":`},
		{name: "malformed notification", message: `{"event":"OngoingIncidentsUpdate","data":"invalid"}`},
		{name: "null notification", message: `{"event":"OngoingIncidentsSnapshot","data":null}`},
		{
			name:      "null total item",
			message:   `{"event":"OngoingIncidentsSnapshot","data":{"ongoingIncidents":{"total":[null]}}}`,
			wantError: "ongoingIncidents.total[0] is null",
		},
		{
			name:      "null added item",
			message:   `{"event":"OngoingIncidentsUpdate","data":{"ongoingIncidents":{"added":[null]}}}`,
			wantError: "ongoingIncidents.added[0] is null",
		},
		{
			name:      "null removed item",
			message:   `{"event":"OngoingIncidentsUpdate","data":{"ongoingIncidents":{"removed":[null]}}}`,
			wantError: "ongoingIncidents.removed[0] is null",
		},
		{
			name:      "null updated item",
			message:   `{"event":"OngoingIncidentsUpdate","data":{"ongoingIncidents":{"updated":[null]}}}`,
			wantError: "ongoingIncidents.updated[0] is null",
		},
		{
			name:      "empty item key",
			message:   `{"event":"OngoingIncidentsSnapshot","data":{"ongoingIncidents":{"total":[{"key":"","state":"error"}]}}}`,
			wantError: "ongoingIncidents.total[0].key is empty",
		},
		{
			name:      "invalid item state",
			message:   `{"event":"OngoingIncidentsSnapshot","data":{"ongoingIncidents":{"total":[{"key":"disk","state":"broken"}]}}}`,
			wantError: `ongoingIncidents.total[0].state "broken" is invalid`,
		},
		{
			name:      "negative healthy item count",
			message:   `{"event":"OngoingIncidentsSnapshot","data":{"ongoingIncidents":{"numItemsOK":-1}}}`,
			wantError: "ongoingIncidents.numItemsOK is negative",
		},
		{
			name:            "message exceeds read limit",
			message:         strings.Repeat("x", 2<<20),
			wantError:       "read limit exceeded",
			allowWriteError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			serverConnections := make(chan *websocket.Conn, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				connection, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				serverConnections <- connection
				for {
					if _, _, err := connection.NextReader(); err != nil {
						_ = connection.Close()
						return
					}
				}
			}))
			t.Cleanup(server.Close)

			events := make(chan wsclient.ServerEvent, 16)
			client, err := wsclient.New(wsclient.Params{
				Config:         wsclient.ConfigServer{ID: "test", Addr: strings.TrimPrefix(server.URL, "http://")},
				Logger:         testLogger,
				EventCh:        events,
				ReconnectDelay: time.Hour,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(client.Close)

			var connection *websocket.Conn
			select {
			case connection = <-serverConnections:
			case <-time.After(3 * time.Second):
				t.Fatal("client did not connect to test server")
			}
			if err := connection.WriteMessage(websocket.TextMessage, []byte(test.message)); err != nil && !test.allowWriteError {
				t.Fatal(err)
			}

			deadline := time.After(3 * time.Second)
			disconnected := false
			for {
				select {
				case event := <-events:
					switch event.Kind {
					case wsclient.ServerEventKindConnection:
						if event.Connection.EventKind == wsclient.EventKindDisconnected {
							disconnected = true
						}
					case wsclient.ServerEventKindConnectionError:
						if event.ConnectionError != "" {
							if !disconnected {
								t.Fatal("connection error was delivered before disconnection")
							}
							if test.wantError != "" && !strings.Contains(event.ConnectionError, test.wantError) {
								t.Fatalf("connection error %q does not contain %q", event.ConnectionError, test.wantError)
							}
							return
						}
					case wsclient.ServerEventKindOngoingIncidents:
						t.Fatalf("malformed message produced notification %#v", event.OngoingIncidents)
					}
				case <-deadline:
					t.Fatal("client did not disconnect from malformed server message")
				}
			}
		})
	}
}
