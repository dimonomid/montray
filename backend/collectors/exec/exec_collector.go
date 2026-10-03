package exec

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dimonomid/montray"
	"github.com/dimonomid/montray/backend/collectors"
	"github.com/dimonomid/montray/logs"
	"github.com/juju/errors"
)

// maxOutputLineBytes limits how much of a command's first stdout line is kept
// for incident details. Longer lines are truncated and marked with an ellipsis.
const maxOutputLineBytes = 200

const (
	defaultPollInterval              = time.Minute
	defaultPollIntervalWhenUnhealthy = 5 * time.Second
	defaultTimeoutCeiling            = time.Minute
)

// firstLineWriter retains up to maxOutputLineBytes from the first line. It
// continues to accept all subsequent output but discards it, effectively acting
// like /dev/null after the retained text. Draining the output prevents a
// verbose command from blocking, while discarding it keeps memory usage bounded.
type firstLineWriter struct {
	line      []byte
	complete  bool
	truncated bool
}

func (w *firstLineWriter) Write(p []byte) (int, error) {
	written := len(p)
	if w.complete {
		return written, nil
	}
	if newline := bytes.IndexByte(p, '\n'); newline >= 0 {
		p = p[:newline]
		w.complete = true
	}
	remaining := maxOutputLineBytes - len(w.line)
	if len(p) > remaining {
		p = p[:remaining]
		w.complete = true
		w.truncated = true
	}
	w.line = append(w.line, p...)
	return written, nil
}

func (w *firstLineWriter) String() string {
	line := strings.TrimSpace(string(w.line))
	if line != "" && w.truncated {
		line += "…"
	}
	return line
}

type Collector struct {
	params CollectorParams

	teardown  chan struct{}
	torndown  chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
}

var _ collectors.Collector = &Collector{}

type CollectorParams struct {
	Common collectors.Params

	Config Config
}

func NewCollector(params CollectorParams) (*Collector, error) {
	if params.Common.Logger == nil {
		panic("Logger is required")
	}
	params.Common.Logger = params.Common.Logger.WithNamespaceAppended("ExecCollector")
	if params.Config.Conditions == nil {
		params.Config.Conditions = []ConfigCondition{
			{ExitCode: "0", Result: montray.ItemStateOK},
			{Result: montray.ItemStateError},
		}
	}
	applyConfigDefaults(&params.Config)
	if err := validateConfig(params.Config); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Collector{
		params:   params,
		teardown: make(chan struct{}),
		torndown: make(chan struct{}),
		ctx:      ctx,
		cancel:   cancel,
	}

	go c.run()

	params.Common.Logger.Log(logs.Info, "Started; polling every %s (%s when unhealthy), command timeout %s",
		c.params.Config.PollInterval, c.params.Config.PollIntervalWhenUnhealthy, c.params.Config.Timeout)

	return c, nil
}

func applyConfigDefaults(config *Config) {
	if config.PollInterval == 0 {
		config.PollInterval = defaultPollInterval
	}
	if config.PollIntervalWhenUnhealthy == 0 {
		config.PollIntervalWhenUnhealthy = defaultPollIntervalWhenUnhealthy
	}
	if config.Timeout == 0 {
		config.Timeout = min(defaultTimeoutCeiling, config.PollInterval, config.PollIntervalWhenUnhealthy)
	}
}

func validateConfig(config Config) error {
	if len(config.Command) == 0 || config.Command[0] == "" {
		return fmt.Errorf("command must not be empty")
	}
	if config.PollInterval < 0 {
		return fmt.Errorf("pollInterval must not be negative")
	}
	if config.PollIntervalWhenUnhealthy < 0 {
		return fmt.Errorf("pollIntervalWhenUnhealthy must not be negative")
	}
	if config.Timeout < 0 {
		return fmt.Errorf("timeout must not be negative")
	}
	if config.Timeout > config.PollInterval {
		return fmt.Errorf("timeout %s must not exceed pollInterval %s", config.Timeout, config.PollInterval)
	}
	if config.Timeout > config.PollIntervalWhenUnhealthy {
		return fmt.Errorf("timeout %s must not exceed pollIntervalWhenUnhealthy %s",
			config.Timeout, config.PollIntervalWhenUnhealthy)
	}
	if len(config.Conditions) == 0 {
		return fmt.Errorf("conditions must not be empty")
	}
	for i, condition := range config.Conditions {
		if condition.ExitCode != "" {
			if _, err := strconv.Atoi(condition.ExitCode); err != nil {
				return fmt.Errorf("condition #%d has invalid exitCode %q", i, condition.ExitCode)
			}
		}
		if !montray.IsItemStateValid(condition.Result) {
			return fmt.Errorf("condition #%d has invalid result %q", i, condition.Result)
		}
	}
	return nil
}

func (c *Collector) Close() {
	c.closeOnce.Do(func() {
		c.cancel()
		close(c.teardown)
	})
	<-c.torndown
}

func (c *Collector) getItemKey(key string) montray.ItemKey {
	return montray.ItemKey(c.params.Common.ID + "." + key)
}

func (c *Collector) run() {
	defer close(c.torndown)

	tickerNormal := time.NewTicker(c.params.Config.PollInterval)
	tickerWhenUnhealthy := time.NewTicker(c.params.Config.PollIntervalWhenUnhealthy)
	defer tickerNormal.Stop()
	defer tickerWhenUnhealthy.Stop()

	var lastItemResult *montray.Item

	runAndHandle := func() bool {
		itemResult := c.runCommand()

		update := &collectors.Update{
			Items: map[montray.ItemKey]*montray.Item{
				itemResult.Key: itemResult,
			},
		}
		select {
		case c.params.Common.UpdatesChan <- update:
		case <-c.teardown:
			return false
		}

		lastItemResult = itemResult
		return true
	}

	// Run right away, without waiting for the ticker.
	if !runAndHandle() {
		return
	}

	for {

		ticker := tickerNormal
		if lastItemResult.State != montray.ItemStateOK {
			ticker = tickerWhenUnhealthy
		}

		select {
		case <-ticker.C:
			if !runAndHandle() {
				return
			}

		case <-c.teardown:
			return
		}
	}
}

func (c *Collector) runCommand() *montray.Item {
	ret := &montray.Item{
		Key: c.getItemKey("exec_result"),

		// State will be populated below. Details starts with the configured
		// description and gains the dynamic execution result.
		Details: c.params.Config.Description,
	}

	commandCtx, cancel := context.WithTimeout(c.ctx, c.params.Config.Timeout)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, c.params.Config.Command[0], c.params.Config.Command[1:]...)
	var stdout firstLineWriter
	// Capture stdout through a pipe instead of cmd.Output or assigning stdout
	// directly to firstLineWriter. The pipe lets us drain output concurrently,
	// so a verbose command cannot block when the OS pipe buffer fills, without
	// buffering all of its output in memory.
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		ret.State = montray.ItemStateError
		ret.Details = appendDetails(ret.Details, errors.Annotatef(err, "failed to capture command output").Error())
		return ret
	}
	if err := cmd.Start(); err != nil {
		ret.State = montray.ItemStateError
		ret.Details = appendDetails(ret.Details, errors.Annotatef(err, "failed to start command").Error())
		return ret
	}
	stdoutDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&stdout, stdoutPipe)
		close(stdoutDone)
	}()

	// StdoutPipe must be fully drained before Wait: Wait closes the pipe and can
	// otherwise discard output that the reader has not consumed yet. On
	// cancellation or timeout we close it ourselves, because a descendant may
	// have inherited stdout and could keep the reader waiting for EOF after the
	// command is killed.
	select {
	case <-stdoutDone:
	case <-commandCtx.Done():
		_ = stdoutPipe.Close()
		<-stdoutDone
	}
	err = cmd.Wait()
	if err != nil {
		if commandCtx.Err() == context.DeadlineExceeded {
			ret.State = montray.ItemStateError
			ret.Details = appendDetails(ret.Details, fmt.Sprintf("Command timed out after %s", c.params.Config.Timeout))
			return ret
		}
		if c.ctx.Err() != nil {
			ret.State = montray.ItemStateError
			ret.Details = appendDetails(ret.Details, "command was canceled: "+c.ctx.Err().Error())
			return ret
		}
		// If the command ran normally but just returned a non-zero status code,
		// we don't handle it as an error here (since status codes are being handled
		// accordingly to the config)
		if _, ok := err.(*exec.ExitError); ok {
			err = nil
		} else {
			// Apparently the error is something else, like IO issues; so file it
			// as an error.
			ret.State = montray.ItemStateError
			ret.Details = appendDetails(ret.Details, errors.Annotatef(err, "failed to run command").Error())
			return ret
		}
	}

	exitCodeStr := strconv.Itoa(cmd.ProcessState.ExitCode())

	for i, cond := range c.params.Config.Conditions {
		if cond.ExitCode != "" && cond.ExitCode != exitCodeStr {
			continue
		}

		// Found the matching condition, so use its result
		ret.State = cond.Result
		c.params.Common.Logger.Log(logs.Debug, "Command exited with code %s; matched condition #%d %+v",
			exitCodeStr, i, cond)
		break
	}

	// If no condition matched, assume error
	if ret.State == "" {
		ret.State = montray.ItemStateError
		c.params.Common.Logger.Log(logs.Warning, "Command exited with code %s; no condition matched, assuming error",
			exitCodeStr)
	}

	dynamicDetails := stdout.String()
	if dynamicDetails == "" {
		dynamicDetails = "exit code: " + exitCodeStr
	}
	ret.Details = appendDetails(ret.Details, dynamicDetails)

	return ret
}

func appendDetails(existing, additional string) string {
	if existing == "" {
		return additional
	}
	return existing + ": " + additional
}
