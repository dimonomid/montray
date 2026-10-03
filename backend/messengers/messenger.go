package messengers

import (
	"github.com/dimonomid/montray/v2"
	"github.com/dimonomid/montray/v2/backend/itemsboard"
	"github.com/dimonomid/montray/v2/logs"
)

type Messenger interface {
	// String returns a human-readable description of the messenger, might be
	// used in some error strings and the like.
	String() string
}

type Params struct {
	Logger *logs.Logger

	// ItemsBoard is the board from which the messenger can get current state of
	// things. It's guaranteed to be updated _before_ notifications are delivered
	// to NotificationsChan.
	ItemsBoard *itemsboard.ItemsBoard

	// NotificationsChan is where the Messenger will get notifications from. Once
	// it's closed, the Messenger should tear itself down, and after that it
	// closes the TornDown channel below.
	NotificationsChan <-chan *montray.Notification

	// TornDown is closed by the Messenger when it has been torn down completely.
	TornDown chan<- struct{}

	// TODO: some channel for notifications to send errors there; this way if e.g.
	// sending emails has failed, the emailer can send an error, core will then read
	// it and might send this error to the other available messengers.
}
