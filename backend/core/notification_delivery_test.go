package core

import (
	"testing"
	"time"

	"github.com/benbjohnson/clock"

	"github.com/dimonomid/montray/v2"
	"github.com/dimonomid/montray/v2/logs"
)

var notificationTestLogger = logs.NewLogger(logs.LoggerParams{Clock: clock.New()})

type namedMessenger string

func (m namedMessenger) String() string { return string(m) }

func TestSendMessengerNotificationWaitsForCapacity(t *testing.T) {
	first := &montray.Notification{}
	second := &montray.Notification{}
	ch := make(chan *montray.Notification, 1)
	ch <- first
	shutdown := make(chan struct{})
	result := make(chan bool, 1)
	go func() {
		result <- sendMessengerNotification(messengerWCtx{messenger: namedMessenger("test"), logger: notificationTestLogger, notificationsChan: ch}, second, shutdown)
	}()

	if got := <-ch; got != first {
		t.Fatalf("first notification = %p, want %p", got, first)
	}
	select {
	case sent := <-result:
		if !sent {
			t.Fatal("notification send was canceled")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocking notification send did not complete after capacity became available")
	}
	if got := <-ch; got != second {
		t.Fatalf("second notification = %p, want %p", got, second)
	}
}

func TestSendMessengerNotificationUnblocksDuringShutdown(t *testing.T) {
	ch := make(chan *montray.Notification, 1)
	ch <- &montray.Notification{}
	shutdown := make(chan struct{})
	result := make(chan bool, 1)
	go func() {
		result <- sendMessengerNotification(
			messengerWCtx{messenger: namedMessenger("test"), logger: notificationTestLogger, notificationsChan: ch},
			&montray.Notification{},
			shutdown,
		)
	}()

	close(shutdown)
	select {
	case sent := <-result:
		if sent {
			t.Fatal("notification was reported sent during shutdown")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocking notification send did not unblock during shutdown")
	}
}

func TestSlowMessengerDoesNotDelayHealthyMessenger(t *testing.T) {
	slowCh := make(chan *montray.Notification, 1)
	slowCh <- &montray.Notification{}
	healthyCh := make(chan *montray.Notification, 1)
	shutdown := make(chan struct{})
	done := make(chan struct{})
	notif := &montray.Notification{}
	go func() {
		sendMessengerNotifications([]messengerWCtx{
			{messenger: namedMessenger("slow"), logger: notificationTestLogger, notificationsChan: slowCh},
			{messenger: namedMessenger("healthy"), logger: notificationTestLogger, notificationsChan: healthyCh},
		}, notif, shutdown)
		close(done)
	}()

	select {
	case got := <-healthyCh:
		if got != notif {
			t.Fatalf("healthy messenger notification = %p, want %p", got, notif)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("healthy messenger waited behind slow messenger")
	}
	close(shutdown)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("messenger fan-out did not stop during shutdown")
	}
}
