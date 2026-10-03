package main

import (
	"github.com/benbjohnson/clock"

	"github.com/dimonomid/montray/v2/logs"
)

var watchTestLogger = logs.NewLogger(logs.LoggerParams{Clock: clock.New()})
