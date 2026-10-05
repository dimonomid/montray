package wsclient

import (
	"testing"
	"time"

	"github.com/dimonomid/clock"

	"github.com/dimonomid/montray/v2"
	"github.com/dimonomid/montray/v2/logs"
)

var testLoggerInternal = logs.NewLogger(logs.LoggerParams{Clock: clock.New()})

func TestNextReconnectDelayUsesLinearBackoff(t *testing.T) {
	delay := time.Duration(0)
	for attempt := 1; attempt <= 12; attempt++ {
		delay = nextReconnectDelay(delay)
		want := time.Duration(attempt) * time.Second
		if want > 10*time.Second {
			want = 10 * time.Second
		}
		if delay != want {
			t.Fatalf("delay after attempt %d = %s, want %s", attempt, delay, want)
		}
	}
}

func TestSendOngoingIncidentsWaitsForCapacity(t *testing.T) {
	first := &montray.Notification{}
	second := &montray.Notification{}
	events := make(chan ServerEvent, 1)
	events <- ServerEvent{Kind: ServerEventKindOngoingIncidents, OngoingIncidents: first}
	client := &WSClient{
		params:    Params{Config: ConfigServer{ID: "test"}, Logger: testLoggerInternal, EventCh: events},
		interrupt: make(chan struct{}),
	}
	result := make(chan bool, 1)
	go func() { result <- client.sendOngoingIncidents(second) }()

	if got := <-events; got.OngoingIncidents != first {
		t.Fatalf("first notification = %p, want %p", got.OngoingIncidents, first)
	}
	select {
	case sent := <-result:
		if !sent {
			t.Fatal("incident send was canceled")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocking incident send did not complete after capacity became available")
	}
	if got := <-events; got.OngoingIncidents != second {
		t.Fatalf("second notification = %p, want %p", got.OngoingIncidents, second)
	}
}

func TestSendOngoingIncidentsUnblocksWhenInterrupted(t *testing.T) {
	events := make(chan ServerEvent, 1)
	events <- ServerEvent{Kind: ServerEventKindOngoingIncidents, OngoingIncidents: &montray.Notification{}}
	client := &WSClient{
		params:    Params{Config: ConfigServer{ID: "test"}, Logger: testLoggerInternal, EventCh: events},
		interrupt: make(chan struct{}),
	}
	result := make(chan bool, 1)
	go func() { result <- client.sendOngoingIncidents(&montray.Notification{}) }()

	close(client.interrupt)
	select {
	case sent := <-result:
		if sent {
			t.Fatal("incident was reported sent after interruption")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocking incident send did not unblock after interruption")
	}
}

func TestSendConnectionEventWaitsForCapacity(t *testing.T) {
	first := ConnectionEvent{EventKind: EventKindConnected}
	second := ConnectionEvent{EventKind: EventKindDisconnected}
	events := make(chan ServerEvent, 1)
	events <- ServerEvent{Kind: ServerEventKindConnection, Connection: first}
	client := &WSClient{
		params:    Params{Config: ConfigServer{ID: "test"}, Logger: testLoggerInternal, EventCh: events},
		interrupt: make(chan struct{}),
	}
	result := make(chan bool, 1)
	go func() { result <- client.sendConnectionEvent(second) }()

	if got := <-events; got.Connection != first {
		t.Fatalf("first connection event = %#v, want %#v", got.Connection, first)
	}
	select {
	case sent := <-result:
		if !sent {
			t.Fatal("connection event send was canceled")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocking connection event send did not complete after capacity became available")
	}
	if got := <-events; got.Connection != second {
		t.Fatalf("second connection event = %#v, want %#v", got.Connection, second)
	}
}

func TestSendConnectionEventUnblocksWhenInterrupted(t *testing.T) {
	events := make(chan ServerEvent, 1)
	events <- ServerEvent{Kind: ServerEventKindConnection, Connection: ConnectionEvent{EventKind: EventKindConnected}}
	client := &WSClient{
		params:    Params{Config: ConfigServer{ID: "test"}, Logger: testLoggerInternal, EventCh: events},
		interrupt: make(chan struct{}),
	}
	result := make(chan bool, 1)
	go func() {
		result <- client.sendConnectionEvent(ConnectionEvent{EventKind: EventKindDisconnected})
	}()

	close(client.interrupt)
	select {
	case sent := <-result:
		if sent {
			t.Fatal("connection event was reported sent after interruption")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocking connection event send did not unblock after interruption")
	}
}
