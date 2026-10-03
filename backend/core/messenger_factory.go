package core

import (
	"fmt"

	"github.com/dimonomid/montray/v2"
	"github.com/dimonomid/montray/v2/backend/itemsboard"
	"github.com/dimonomid/montray/v2/backend/messengers"
	"github.com/dimonomid/montray/v2/backend/messengers/filelogger"
	"github.com/dimonomid/montray/v2/backend/messengers/webserver"
	"github.com/dimonomid/montray/v2/logs"
)

// messengerWCtx contains the messenger and its context (e.g. channels for that
// messenger)
type messengerWCtx struct {
	messenger messengers.Messenger
	logger    *logs.Logger

	notificationsChan chan *montray.Notification
	tornDown          chan struct{}
}

func createMessenger(
	cfg Messenger, commonParams messengers.Params,
) (messengers.Messenger, error) {
	numTypes := 0
	if cfg.FileLogger != nil {
		numTypes++
	}
	if cfg.Webserver != nil {
		numTypes++
	}
	if numTypes != 1 {
		return nil, fmt.Errorf("config contains %d messenger types; exactly one messenger type is required", numTypes)
	}

	if cfg.FileLogger != nil {
		c, err := filelogger.New(filelogger.Params{
			Common: commonParams,
			Config: *cfg.FileLogger,
		})
		if err != nil {
			return nil, fmt.Errorf("creating filelogger: %w", err)
		}

		return c, nil
	}

	if cfg.Webserver != nil {
		c, err := webserver.New(webserver.Params{
			Common: commonParams,
			Config: *cfg.Webserver,
		})
		if err != nil {
			return nil, fmt.Errorf("creating webserver: %w", err)
		}

		return c, nil
	}

	return nil, fmt.Errorf("no valid messenger configuration")
}

func createMessengers(cfgs []Messenger, ib *itemsboard.ItemsBoard, logger *logs.Logger) ([]messengerWCtx, error) {
	ret := make([]messengerWCtx, 0, len(cfgs))

	for i, cfg := range cfgs {
		messengerLogger := logger.WithContext("messenger_index", fmt.Sprintf("%d", i))
		mwCtx := messengerWCtx{
			notificationsChan: make(chan *montray.Notification, 32),
			tornDown:          make(chan struct{}),
			logger:            messengerLogger,
		}

		commonParams := messengers.Params{
			Logger:            messengerLogger,
			ItemsBoard:        ib,
			NotificationsChan: mwCtx.notificationsChan,
			TornDown:          mwCtx.tornDown,
		}

		var err error
		mwCtx.messenger, err = createMessenger(cfg, commonParams)
		if err != nil {
			for _, created := range ret {
				close(created.notificationsChan)
				<-created.tornDown
			}
			return nil, fmt.Errorf("creating messenger from config #%d: %w", i, err)
		}

		ret = append(ret, mwCtx)
	}

	return ret, nil
}
