package main

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestSetupWebserverListensOnlyOnIPv4Loopback(t *testing.T) {
	server := setupWebserver(newStatusWebserver(statusWebserverParams{Logger: watchTestLogger}))
	t.Cleanup(func() { _ = server.Close() })

	address, ok := server.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected listener address type %T", server.Addr())
	}
	if got, want := address.IP.String(), "127.0.0.1"; got != want {
		t.Fatalf("listener IP is %q, want %q", got, want)
	}
}

func TestLocalStatusServerCloseClosesActiveWebsocket(t *testing.T) {
	server, serveDone := startLocalStatusServer(t)
	connection := dialLocalStatusServer(t, server)
	var initial statusWebsocketMessage
	if err := connection.ReadJSON(&initial); err != nil {
		t.Fatal(err)
	}

	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	assertLocalStatusServerStopped(t, serveDone)
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := connection.ReadMessage(); err == nil {
		t.Fatal("status WebSocket remained open after server shutdown")
	}
}

func TestLocalStatusServerCloseRejectsConcurrentWebsockets(t *testing.T) {
	server, serveDone := startLocalStatusServer(t)
	start := make(chan struct{})
	connections := make(chan *websocket.Conn, 64)
	var attempts sync.WaitGroup
	for i := 0; i < cap(connections); i++ {
		attempts.Add(1)
		go func() {
			defer attempts.Done()
			<-start
			url := "ws://" + server.Addr().String() + "/api/v1/wsconnect"
			connection, _, err := websocket.DefaultDialer.Dial(url, nil)
			if err == nil {
				connections <- connection
			}
		}()
	}
	close(start)
	var firstConnection *websocket.Conn
	select {
	case firstConnection = <-connections:
	case <-time.After(3 * time.Second):
		t.Fatal("no concurrent WebSocket upgrade completed")
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	attempts.Wait()
	close(connections)
	assertLocalStatusServerStopped(t, serveDone)

	assertWebsocketClosed := func(connection *websocket.Conn) {
		t.Helper()
		_ = connection.SetReadDeadline(time.Now().Add(time.Second))
		for {
			if _, _, err := connection.ReadMessage(); err != nil {
				if netError, ok := err.(net.Error); ok && netError.Timeout() {
					_ = connection.Close()
					t.Fatal("concurrently upgraded status WebSocket survived shutdown")
				}
				break
			}
		}
		_ = connection.Close()
	}
	assertWebsocketClosed(firstConnection)
	for connection := range connections {
		assertWebsocketClosed(connection)
	}
}

func TestStatusBroadcastDisconnectsClientWhoseQueueIsFull(t *testing.T) {
	status := newStatusWebserver(statusWebserverParams{Logger: watchTestLogger})
	client := &statusWebsocketClient{
		updates: make(chan statusWebsocketMessage, 1),
		done:    make(chan struct{}),
	}
	status.clients[client] = struct{}{}
	first := statusWebsocketMessage{}
	first.Servers = []serverStatus{{ID: "first"}}
	second := statusWebsocketMessage{}
	second.Servers = []serverStatus{{ID: "second"}}
	client.updates <- first
	status.broadcast(second)

	select {
	case <-client.done:
	default:
		t.Fatal("client with a full update queue was not closed")
	}
	if _, exists := status.clients[client]; exists {
		t.Fatal("client with a full update queue remains registered")
	}
	if got := <-client.updates; len(got.Servers) != 1 || got.Servers[0].ID != "first" {
		t.Fatalf("queued status update = %#v, want original update", got)
	}
}

func TestStatusWebsocketQueueSize(t *testing.T) {
	if statusWebsocketQueueSize != 64 {
		t.Fatalf("status WebSocket queue size = %d, want 64", statusWebsocketQueueSize)
	}
}

func TestForgetEndpointForgetsOnlyStaleIncident(t *testing.T) {
	var forgottenKey string
	status := newStatusWebserver(statusWebserverParams{
		Logger: watchTestLogger,
		OnForget: func(key string) bool {
			forgottenKey = key
			return key == "server.disk"
		},
	})
	handler := rootHandler(status)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/forget", strings.NewReader(`{"key":"server.disk"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("forget returned HTTP %d, want %d", response.Code, http.StatusNoContent)
	}
	if forgottenKey != "server.disk" {
		t.Fatalf("forgotten key = %q, want server.disk", forgottenKey)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/forget", strings.NewReader(`{"key":"server.fresh"}`))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("non-stale forget returned HTTP %d, want %d", response.Code, http.StatusConflict)
	}
}

func startLocalStatusServer(t *testing.T) (*localStatusServer, <-chan error) {
	t.Helper()
	server := setupWebserver(newStatusWebserver(statusWebserverParams{Logger: watchTestLogger}))
	t.Cleanup(func() { _ = server.Close() })
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve() }()
	return server, serveDone
}

func dialLocalStatusServer(t *testing.T, server *localStatusServer) *websocket.Conn {
	t.Helper()
	url := "ws://" + server.Addr().String() + "/api/v1/wsconnect"
	connection, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return connection
}

func assertLocalStatusServerStopped(t *testing.T, serveDone <-chan error) {
	t.Helper()
	select {
	case err := <-serveDone:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("status HTTP server did not stop")
	}
}
