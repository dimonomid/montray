package core

import (
	"fmt"
	"sync"
	"time"

	"github.com/dimonomid/montray/v2"
	"github.com/dimonomid/montray/v2/backend/collectors"
	"github.com/dimonomid/montray/v2/backend/itemsboard"
	"github.com/dimonomid/montray/v2/logs"
	"github.com/dimonomid/montray/v2/statestracker"

	"github.com/dimonomid/clock"
)

type Core struct {
	params Params

	colls      []collectors.Collector
	messengers []messengerWCtx
	updCh      chan *collectors.Update

	ib *itemsboard.ItemsBoard

	tracker *statestracker.ItemStatesTracker

	shutdown  chan struct{}
	torndown  chan struct{}
	closeOnce sync.Once
}

type Params struct {
	Clock  clock.Clock
	Logger *logs.Logger
}

func NewCore(cfg Config, params Params) (*Core, error) {
	if params.Clock == nil {
		panic("Clock is required")
	}
	if params.Logger == nil {
		panic("Logger is required")
	}
	params.Logger = params.Logger.WithNamespaceAppended("Core")
	updCh := make(chan *collectors.Update, 16)

	collParams := collectors.Params{
		// NOTE: ID will be populated later for every collector individually.

		Logger:      params.Logger,
		UpdatesChan: updCh,
	}

	ib := itemsboard.New()

	colls, err := createCollectors(cfg.Collectors, collParams)
	if err != nil {
		return nil, fmt.Errorf("creating collectors: %w", err)
	}

	messengers, err := createMessengers(cfg.Messengers, ib, params.Logger)
	if err != nil {
		for _, collector := range colls {
			collector.Close()
		}
		return nil, fmt.Errorf("creating messengers: %w", err)
	}

	c := &Core{
		params: params,

		colls:      colls,
		messengers: messengers,
		updCh:      updCh,

		ib: ib,

		tracker: statestracker.NewItemStatesTracker(statestracker.ItemStatesTrackerParams{
			Clock: params.Clock,
		}),

		shutdown: make(chan struct{}),
		torndown: make(chan struct{}),
	}

	go c.run()

	return c, nil
}

func (c *Core) Close() {
	c.closeOnce.Do(c.close)
}

func (c *Core) close() {
	// Close all collectors first
	for _, coll := range c.colls {
		coll.Close()
	}

	// Release notification sends that are applying backpressure. Delivery is no
	// longer useful once shutdown starts, and must not prevent the run goroutine
	// from terminating.
	close(c.shutdown)

	// At this point we're sure that no more collectors will send any messages to
	// the updCh, so we can close it and wait for the run goroutine to exit as
	// well.
	close(c.updCh)

	// TODO: use a timeout
	<-c.torndown

	// Now that we can't send any more notifications, close all messenger
	// channels and wait for all of them to tear down.
	for _, mwCtx := range c.messengers {
		close(mwCtx.notificationsChan)

		// TODO: use a timeout
		<-mwCtx.tornDown
	}
}

func (c *Core) run() {
	for upd := range c.updCh {
		if upd.Err != nil {
			c.params.Logger.Log(logs.Error, "Collector update failed: %+v", upd.Err)
			// TODO: convert it to some kind of internal incident
			continue
		}

		notif := c.tracker.FeedItems(upd.Items)
		if notif != nil {
			for _, item := range notif.OngoingIncidents.Added {
				if item == nil {
					continue
				}
				if item.Details == "" {
					c.params.Logger.Log(logs.Info, "Incident started: %s (%s)", item.Key, item.State)
				} else {
					c.params.Logger.Log(logs.Info, "Incident started: %s (%s): %s", item.Key, item.State, item.Details)
				}
			}
			for _, incident := range notif.OngoingIncidents.Removed {
				if incident == nil {
					continue
				}

				// Removed contains the previous non-OK item. Read the resolving
				// observation from this collector update so the log explains which
				// concrete healthy state ended the incident.
				resolvedItem := upd.Items[incident.Key]
				if resolvedItem != nil && resolvedItem.Details != "" {
					c.params.Logger.Log(logs.Info, "Incident resolved: %s: %s", incident.Key, resolvedItem.Details)
				} else {
					c.params.Logger.Log(logs.Info, "Incident resolved: %s", incident.Key)
				}
			}

			// We must update items board _before_ sending notifications to the
			// messenger channels; this way, when messengers fan notifications out
			// (e.g. webserver will fan those out to all websocket clients), they can
			// be sure that if they first add a new output channel, then start the
			// goroutine to read from this channel, and that goroutine first of all
			// gets the current status from the ItemsBoard, then it's guaranteed that
			// no updates can be missed. It's possible to get a redundant update
			// instead, but that's ok.
			c.ib.Set(notif.OngoingIncidents.Total)

			sendMessengerNotifications(c.messengers, notif, c.shutdown)
		}
	}

	close(c.torndown)
}

// sendMessengerNotifications delivers one notification to all messengers in
// parallel, then waits for every delivery. This preserves notification order
// without making a healthy messenger wait behind a slow one.
func sendMessengerNotifications(messengers []messengerWCtx, notif *montray.Notification, shutdown <-chan struct{}) {
	var wg sync.WaitGroup
	for _, mwCtx := range messengers {
		wg.Add(1)
		go func(mwCtx messengerWCtx) {
			defer wg.Done()
			sendMessengerNotification(mwCtx, notif, shutdown)
		}(mwCtx)
	}
	wg.Wait()
}

func sendMessengerNotification(mwCtx messengerWCtx, notif *montray.Notification, shutdown <-chan struct{}) bool {
	select {
	case <-shutdown:
		return false
	default:
	}

	select {
	case mwCtx.notificationsChan <- notif:
		return true
	case <-shutdown:
		return false
	default:
	}

	started := time.Now()
	mwCtx.logger.Log(logs.Warning, "Notification delivery is blocked; waiting for %s", mwCtx.messenger.String())
	select {
	case mwCtx.notificationsChan <- notif:
		mwCtx.logger.Log(logs.Warning, "Notification delivery to %s resumed after %s", mwCtx.messenger.String(), time.Since(started))
		return true
	case <-shutdown:
		return false
	}
}
