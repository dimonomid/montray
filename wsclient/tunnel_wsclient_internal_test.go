package wsclient

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/gorilla/websocket"

	"github.com/dimonomid/montray/v2/logs"
)

func TestWSClientWaitsForTunnelReadiness(t *testing.T) {
	requests := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- struct{}{}
		connection, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		for {
			if _, _, err := connection.NextReader(); err != nil {
				return
			}
		}
	}))

	logger := logs.NewLogger(logs.LoggerParams{Clock: clock.New()})
	serverEvents := make(chan ServerEvent, 16)
	tunnel := NewTunnelSupervisor(TunnelSupervisorParams{
		ServerID: "remote",
		Command: TunnelCommandSpec{
			Command:              []string{"sh", "-c", "sleep 0.15; echo tunnel-ready; exec sleep 30"},
			ReadinessProbeString: "tunnel-ready",
		},
		Logger:       logger,
		EventCh:      serverEvents,
		RestartDelay: time.Hour,
	})
	client, err := New(Params{
		Config:         ConfigServer{ID: "remote", Addr: strings.TrimPrefix(server.URL, "http://")},
		Logger:         logger,
		EventCh:        serverEvents,
		ReconnectDelay: time.Hour,
		Tunnel:         tunnel,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.Close()
		tunnel.Close()
		server.Close()
	})

	select {
	case <-requests:
		t.Fatal("WebSocket connection was attempted before tunnel readiness")
	case event := <-serverEvents:
		t.Fatalf("server event %#v was published before tunnel readiness", event)
	case <-time.After(50 * time.Millisecond):
	}

	select {
	case <-requests:
	case <-time.After(3 * time.Second):
		t.Fatal("WebSocket connection was not attempted after tunnel readiness")
	}

	for i, wantKind := range []ServerEventKind{
		ServerEventKindTunnel,
		ServerEventKindConnectionError,
		ServerEventKindConnection,
	} {
		select {
		case event := <-serverEvents:
			if event.Kind != wantKind {
				t.Fatalf("event #%d kind = %q, want %q; event = %#v", i+1, event.Kind, wantKind, event)
			}
			switch wantKind {
			case ServerEventKindTunnel:
				if event.Tunnel.Kind != TunnelEventReady {
					t.Fatalf("event #%d tunnel kind = %d, want ready; event = %#v", i+1, event.Tunnel.Kind, event)
				}
			case ServerEventKindConnectionError:
				if event.ConnectionError != "" {
					t.Fatalf("event #%d connection error = %q, want empty", i+1, event.ConnectionError)
				}
			case ServerEventKindConnection:
				if event.Connection.EventKind != EventKindConnected {
					t.Fatalf("event #%d connection kind = %q, want connected; event = %#v",
						i+1, event.Connection.EventKind, event)
				}
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for event #%d (%s)", i+1, wantKind)
		}
	}
}
