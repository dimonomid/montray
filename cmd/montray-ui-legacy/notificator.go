package main

import desktopnotificator "github.com/0xAX/notificator"

// notificator is the application-level notification interface. Keeping
// it local allows notification behavior to be replaced in tests.
type notificator interface {
	Push(title, text string)
}

// desktopNotificationSink adapts the desktop notificator library to the
// application interface.
type desktopNotificationSink struct {
	notificator *desktopnotificator.Notificator
}

func newDesktopNotificationSink() notificator {
	return &desktopNotificationSink{notificator: desktopnotificator.New(desktopnotificator.Options{})}
}

func (n *desktopNotificationSink) Push(title, text string) {
	n.notificator.Push(title, text, "", desktopnotificator.UR_NORMAL)
}
