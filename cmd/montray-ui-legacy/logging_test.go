package main

import (
	"github.com/dimonomid/clock"

	"github.com/dimonomid/montray/v2/logs"
)

var watchTestLogger = logs.NewLogger(logs.LoggerParams{Clock: clock.New()})
