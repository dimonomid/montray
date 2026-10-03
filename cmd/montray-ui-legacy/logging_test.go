package main

import (
	"github.com/benbjohnson/clock"

	"github.com/dimonomid/montray/logs"
)

var watchTestLogger = logs.NewLogger(logs.LoggerParams{Clock: clock.New()})
