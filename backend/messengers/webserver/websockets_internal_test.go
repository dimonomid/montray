package webserver

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/benbjohnson/clock"

	"github.com/dimonomid/montray/v2"
	"github.com/dimonomid/montray/v2/backend/itemsboard"
	"github.com/dimonomid/montray/v2/backend/messengers"
	"github.com/dimonomid/montray/v2/logs"
	"github.com/gorilla/websocket"
)

var testLoggerInternal = logs.NewLogger(logs.LoggerParams{Clock: clock.New()})

func TestWSTransmitFailureUnsubscribesConnection(t *testing.T) {
	serverConnections := make(chan *websocket.Conn, 1)
	handlerDone := make(chan struct{})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serverConnections <- connection
		<-handlerDone
	}))
	t.Cleanup(func() {
		close(handlerDone)
		httpServer.Close()
	})

	clientConnection, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	serverConnection := <-serverConnections

	ctx, cancel := context.WithCancel(context.Background())
	connection := &wsConn{
		conn:      serverConnection,
		ctx:       ctx,
		ctxCancel: cancel,
	}
	webserver := &Webserver{
		params:  Params{Common: messengers.Params{Logger: testLoggerInternal, ItemsBoard: itemsboard.New()}},
		subs:    make(map[int]chan *montray.Notification),
		wsConns: make(map[int]*wsConn),
	}
	var notifications chan *montray.Notification
	connection.subID, notifications, _ = webserver.subscribe(connection)

	// Resetting the client TCP connection makes the server's write side fail
	// without a server receive loop performing cleanup first.
	tcpConnection, ok := clientConnection.UnderlyingConn().(*net.TCPConn)
	if !ok {
		t.Fatalf("underlying connection has type %T, want *net.TCPConn", clientConnection.UnderlyingConn())
	}
	if err := tcpConnection.SetLinger(0); err != nil {
		t.Fatal(err)
	}
	if err := clientConnection.Close(); err != nil {
		t.Fatal(err)
	}

	// Queue several writes so the test does not depend on the first write being
	// the one that observes the peer's TCP reset.
	for i := 0; i < cap(notifications); i++ {
		notifications <- &montray.Notification{}
	}
	go webserver.wsTxLoop(connection, notifications)

	select {
	case <-connection.ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("transmit failure did not unsubscribe the connection")
	}

	webserver.subsMtx.Lock()
	_, subscribed := webserver.subs[connection.subID]
	webserver.subsMtx.Unlock()
	if subscribed {
		t.Fatal("connection remains registered after transmit failure")
	}
}

func TestWebsocketSubscriptionQueueSize(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connection := &wsConn{ctx: ctx, ctxCancel: cancel}
	webserver := &Webserver{
		params:  Params{Common: messengers.Params{Logger: testLoggerInternal}},
		subs:    make(map[int]chan *montray.Notification),
		wsConns: make(map[int]*wsConn),
	}

	_, notifications, ok := webserver.subscribe(connection)
	if !ok {
		t.Fatal("subscription was rejected")
	}
	if got := cap(notifications); got != 64 {
		t.Fatalf("subscription queue capacity = %d, want 64", got)
	}
}

func TestWebsocketClientLifecycleIsLoggedAtInfo(t *testing.T) {
	logPath := t.TempDir() + "/webserver.log"
	logger := logs.NewLogger(logs.LoggerParams{
		Clock: clock.New(),
		Sinks: []logs.LoggerSinkParams{{Filepath: logPath, MinLevel: logs.Info}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	connection := &wsConn{ctx: ctx, ctxCancel: cancel}
	webserver := &Webserver{
		params:  Params{Common: messengers.Params{Logger: logger}},
		subs:    make(map[int]chan *montray.Notification),
		wsConns: make(map[int]*wsConn),
	}

	id, _, ok := webserver.subscribe(connection)
	if !ok {
		t.Fatal("subscription was rejected")
	}
	webserver.unsubscribe(id)

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[I] WebSocket client 0 connected from unknown address",
		"[I] WebSocket client 0 disconnected from unknown address",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("log %q does not contain %q", data, want)
		}
	}
}

func TestRunDisconnectsSubscriberWhoseQueueIsFull(t *testing.T) {
	incoming := make(chan *montray.Notification, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connection := &wsConn{ctx: ctx, ctxCancel: cancel}
	webserver := &Webserver{
		params:  Params{Common: messengers.Params{Logger: testLoggerInternal, NotificationsChan: incoming}},
		server:  &http.Server{},
		subs:    make(map[int]chan *montray.Notification),
		wsConns: make(map[int]*wsConn),
	}
	id, notifications, ok := webserver.subscribe(connection)
	if !ok {
		t.Fatal("subscription was rejected")
	}
	for i := 0; i < cap(notifications); i++ {
		notifications <- &montray.Notification{}
	}
	runDone := make(chan struct{})
	go func() {
		webserver.run()
		close(runDone)
	}()
	incoming <- &montray.Notification{}

	select {
	case <-connection.ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("subscriber with a full notification queue was not canceled")
	}
	webserver.subsMtx.Lock()
	_, subscribed := webserver.subs[id]
	webserver.subsMtx.Unlock()
	if subscribed {
		t.Fatal("subscriber with a full notification queue remains registered")
	}

	close(incoming)
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("webserver run loop did not stop")
	}
}
