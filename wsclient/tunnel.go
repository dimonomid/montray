package wsclient

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

	"github.com/dimonomid/montray/v2/logs"
)

const (
	defaultTunnelRestartDelay = 5 * time.Second
	tunnelCommandWaitDelay    = time.Second
	sshTunnelReadySignal      = "MONTRAY_TUNNEL_READY"
	// maxTunnelFailureOutputBytes bounds the tail of stdout or stderr retained
	// from one tunnel process for inclusion in a failure incident.
	maxTunnelFailureOutputBytes = 1024
)

// TunnelCommandSpec describes the process that provides a tunnel and the
// output string, if any, that signals its readiness.
type TunnelCommandSpec struct {
	// Command contains the executable followed by its arguments.
	Command []string
	// ReadinessProbeString marks the tunnel ready when it appears in either
	// stdout or stderr. An empty string means the tunnel is ready once process
	// started.
	ReadinessProbeString string
}

// TunnelEventKind identifies a tunnel lifecycle event.
type TunnelEventKind int

const (
	// TunnelEventReady indicates that the tunnel can accept connections.
	TunnelEventReady TunnelEventKind = iota
	// TunnelEventFailed indicates that the tunnel process failed.
	TunnelEventFailed
)

// TunnelEvent reports a readiness transition or process failure.
type TunnelEvent struct {
	// Kind identifies whether the tunnel became ready or failed.
	Kind TunnelEventKind
	// Time records when the event occurred.
	Time time.Time
	// Error describes a failure and is empty for readiness events.
	Error string
}

// TunnelSupervisor keeps a tunnel command running and tracks whether its
// current process has reported readiness.
type TunnelSupervisor struct {
	// params contains the immutable settings used for every process generation.
	params TunnelSupervisorParams
	// cancel stops the active command and prevents further restarts.
	cancel context.CancelFunc
	// done is closed after the supervisor loop exits.
	done chan struct{}
	// once makes Close safe to call more than once.
	once sync.Once

	// readyMtx protects readyCh and ready.
	readyMtx sync.RWMutex
	// readyCh is closed when the current process generation becomes ready or
	// exits, waking callers so they can observe the new state or generation.
	readyCh chan struct{}
	// ready reports whether the current process generation is ready.
	ready bool
}

// TunnelSupervisorParams contains the dependencies and process settings used
// by a TunnelSupervisor.
type TunnelSupervisorParams struct {
	// ServerID identifies the server whose tunnel is being supervised.
	ServerID string
	// Command describes the tunnel process and its readiness signal.
	Command TunnelCommandSpec
	// Logger receives tunnel lifecycle messages.
	Logger *logs.Logger
	// EventCh receives tunnel lifecycle events in the server's ordered event
	// stream.
	EventCh chan<- ServerEvent
	// RestartDelay controls how long to wait after a failure before restarting.
	RestartDelay time.Duration
}

// NewTunnelSupervisor starts supervising the configured tunnel command.
func NewTunnelSupervisor(params TunnelSupervisorParams) *TunnelSupervisor {
	if params.Logger == nil {
		panic("Logger is required")
	}
	if params.EventCh == nil {
		panic("EventCh is required")
	}
	if len(params.Command.Command) == 0 || params.Command.Command[0] == "" {
		panic("Tunnel command is required")
	}
	if params.RestartDelay == 0 {
		params.RestartDelay = defaultTunnelRestartDelay
	}
	params.Command.Command = append([]string(nil), params.Command.Command...)
	params.Logger = params.Logger.WithNamespaceAppended("Tunnel")

	ctx, cancel := context.WithCancel(context.Background())
	t := &TunnelSupervisor{
		params:  params,
		cancel:  cancel,
		done:    make(chan struct{}),
		readyCh: make(chan struct{}),
	}
	go t.run(ctx)
	return t
}

// Close stops the active tunnel command and waits for the supervisor to exit.
func (t *TunnelSupervisor) Close() {
	t.once.Do(func() {
		t.params.Logger.Log(logs.Info, "Shutting down")
		t.cancel()
		<-t.done
		t.params.Logger.Log(logs.Info, "Shutdown complete")
	})
}

// WaitReady waits until the current tunnel process reports readiness. When a
// process exits before becoming ready, its gate is replaced and waiters
// continue waiting for the next process generation.
func (t *TunnelSupervisor) WaitReady(interrupt <-chan struct{}) bool {
	for {
		t.readyMtx.RLock()
		ready := t.ready
		readyCh := t.readyCh
		t.readyMtx.RUnlock()
		if ready {
			return true
		}

		select {
		case <-readyCh:
		case <-t.done:
			return false
		case <-interrupt:
			return false
		}
	}
}

// IsReady reports whether the current tunnel process has reported readiness.
func (t *TunnelSupervisor) IsReady() bool {
	t.readyMtx.RLock()
	defer t.readyMtx.RUnlock()
	return t.ready
}

// run starts the tunnel command, observes its lifecycle, and restarts it after
// failures until the context is canceled.
func (t *TunnelSupervisor) run(ctx context.Context) {
	defer close(t.done)
	for {
		command := t.params.Command.Command
		t.params.Logger.Log(logs.Info, "Starting tunnel with %s", command[0])
		// TODO: Terminate subprocesses created by the tunnel command on restart or
		// Watch shutdown. As documented, tunnel commands must not create
		// subprocesses, so for now a command that violates this contract may leave
		// them running. Proper cleanup requires platform-specific process-tree
		// handling and is intentionally deferred.
		cmd := exec.CommandContext(ctx, command[0], command[1:]...)
		isolateTunnelCommandFromTerminalSignals(cmd)
		cmd.WaitDelay = tunnelCommandWaitDelay
		stdout := &tailOutputWriter{}
		stderr := &tailOutputWriter{}
		cmd.Stdout = stdout
		cmd.Stderr = stderr

		t.readyMtx.RLock()
		generationReady := t.readyCh
		t.readyMtx.RUnlock()
		if probe := t.params.Command.ReadinessProbeString; probe != "" {
			onReady := func() { t.markReady(ctx, generationReady) }
			cmd.Stdout = io.MultiWriter(newReadinessProbeWriter(probe, onReady), stdout)
			cmd.Stderr = io.MultiWriter(newReadinessProbeWriter(probe, onReady), stderr)
		}

		if err := cmd.Start(); err != nil {
			if ctx.Err() != nil {
				return
			}
			t.setUnready()
			t.emit(ctx, TunnelEvent{
				Kind:  TunnelEventFailed,
				Time:  time.Now(),
				Error: fmt.Sprintf("Failed to start tunnel command: %s", err),
			})
			if !t.waitToRestart(ctx) {
				return
			}
			continue
		}
		if t.params.Command.ReadinessProbeString == "" {
			t.markReady(ctx, generationReady)
		}

		err := cmd.Wait()
		if ctx.Err() != nil {
			t.params.Logger.Log(logs.Debug, "Tunnel stopped")
			return
		}
		t.setUnready()

		details := "Tunnel command exited unexpectedly"
		if err != nil {
			details = "Tunnel command exited: " + err.Error()
		}
		if output := tunnelFailureOutput(stderr.String(), stdout.String(), t.params.Command.ReadinessProbeString); output != "" {
			details += "\n\n" + output
		}
		t.emit(ctx, TunnelEvent{Kind: TunnelEventFailed, Time: time.Now(), Error: details})
		if !t.waitToRestart(ctx) {
			return
		}
	}
}

// markReady marks a process generation ready and emits its readiness event.
// It ignores signals belonging to a process generation that has already exited.
func (t *TunnelSupervisor) markReady(ctx context.Context, generationReady chan struct{}) {
	t.readyMtx.Lock()
	if t.readyCh != generationReady || t.ready {
		t.readyMtx.Unlock()
		return
	}
	// Publish readiness before releasing WaitReady callers so a subsequent
	// connection event cannot overtake this event in the shared server stream.
	t.emit(ctx, TunnelEvent{Kind: TunnelEventReady, Time: time.Now()})
	if ctx.Err() != nil {
		t.readyMtx.Unlock()
		return
	}
	t.ready = true
	close(t.readyCh)
	t.readyMtx.Unlock()

	t.params.Logger.Log(logs.Info, "Tunnel is ready")
}

// setUnready replaces the readiness gate for the next process generation.
func (t *TunnelSupervisor) setUnready() {
	t.readyMtx.Lock()
	if !t.ready {
		close(t.readyCh)
	}
	t.readyCh = make(chan struct{})
	t.ready = false
	t.readyMtx.Unlock()
}

// emit publishes an event unless supervision has been canceled.
func (t *TunnelSupervisor) emit(ctx context.Context, event TunnelEvent) {
	select {
	case t.params.EventCh <- ServerEvent{Kind: ServerEventKindTunnel, Tunnel: event}:
	case <-ctx.Done():
	}
}

// waitToRestart waits for the configured restart delay or cancellation.
func (t *TunnelSupervisor) waitToRestart(ctx context.Context) bool {
	t.params.Logger.Log(logs.Warning, "Tunnel failed; restarting in %s", t.params.RestartDelay)
	timer := time.NewTimer(t.params.RestartDelay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// readinessProbeWriter searches a command output stream for its readiness
// probe. Only the suffix that could begin a match in the next Write is kept.
type readinessProbeWriter struct {
	// probe is the output sequence that signals readiness.
	probe []byte
	// tail retains the possible beginning of a match split across writes.
	tail []byte
	// onReady is called the first time probe is found.
	onReady func()
	// ready prevents repeated searches and callbacks after the probe is found.
	ready bool
}

// newReadinessProbeWriter returns a writer that calls onReady after observing
// probe, including when probe is split across multiple writes.
func newReadinessProbeWriter(probe string, onReady func()) *readinessProbeWriter {
	return &readinessProbeWriter{probe: []byte(probe), onReady: onReady}
}

// Write searches p and the retained suffix of the previous write for the
// readiness probe. It always consumes the complete input.
func (w *readinessProbeWriter) Write(p []byte) (int, error) {
	written := len(p)
	if w.ready {
		return written, nil
	}
	combined := make([]byte, 0, len(w.tail)+len(p))
	combined = append(combined, w.tail...)
	combined = append(combined, p...)
	if bytes.Contains(combined, w.probe) {
		w.ready = true
		w.tail = nil
		w.onReady()
		return written, nil
	}

	keep := len(w.probe) - 1
	if keep > len(combined) {
		keep = len(combined)
	}
	w.tail = append(w.tail[:0], combined[len(combined)-keep:]...)
	return written, nil
}

// tailOutputWriter consumes a command output stream while retaining only its
// most recent bytes for a possible failure incident.
type tailOutputWriter struct {
	// tail contains at most maxTunnelFailureOutputBytes of the latest output.
	tail []byte
	// truncated records that older output was discarded.
	truncated bool
}

// Write retains the newest part of p and always consumes the complete input so
// a verbose tunnel process cannot block on a full output pipe.
func (w *tailOutputWriter) Write(p []byte) (int, error) {
	written := len(p)
	if len(p) >= maxTunnelFailureOutputBytes {
		w.tail = append(w.tail[:0], p[len(p)-maxTunnelFailureOutputBytes:]...)
		w.truncated = true
		return written, nil
	}

	overflow := len(w.tail) + len(p) - maxTunnelFailureOutputBytes
	if overflow > 0 {
		copy(w.tail, w.tail[overflow:])
		w.tail = w.tail[:len(w.tail)-overflow]
		w.truncated = true
	}
	w.tail = append(w.tail, p...)
	return written, nil
}

// String returns the retained output with surrounding whitespace removed and
// an ellipsis when output preceding it was discarded.
func (w *tailOutputWriter) String() string {
	output := strings.TrimSpace(string(w.tail))
	if output != "" && w.truncated {
		return "…" + output
	}
	return output
}

// tunnelFailureOutput selects stderr when available, otherwise stdout. Lines
// consisting only of the readiness signal are removed because they are
// supervisor protocol rather than useful failure output.
func tunnelFailureOutput(stderr, stdout, readinessProbe string) string {
	clean := func(output string) string {
		if readinessProbe != "" {
			lines := strings.Split(output, "\n")
			kept := lines[:0]
			for _, line := range lines {
				if strings.TrimSpace(line) != readinessProbe {
					kept = append(kept, line)
				}
			}
			output = strings.Join(kept, "\n")
		}
		return strings.TrimSpace(output)
	}
	if stderr = clean(stderr); stderr != "" {
		return stderr
	}
	return clean(stdout)
}

// TunnelCommand converts a server's tunnel configuration into the command and
// readiness signal used by the supervisor. It returns nil for a direct server.
func TunnelCommand(server ConfigServer) (*TunnelCommandSpec, error) {
	if server.Tunnel == nil {
		return nil, nil
	}
	if custom := server.Tunnel.CustomCommand; custom != nil {
		spec := &TunnelCommandSpec{Command: append([]string(nil), custom.Command...)}
		if custom.ReadinessProbe != nil {
			spec.ReadinessProbeString = custom.ReadinessProbe.ContainsOutput
		}
		return spec, nil
	}
	if server.Tunnel.SSH == nil {
		return nil, fmt.Errorf("tunnel has neither ssh nor customCommand")
	}

	ssh := server.Tunnel.SSH
	port := ssh.Port
	if port == 0 {
		port = 22
	}
	destination := ssh.User + "@" + ssh.Host
	args := []string{
		"ssh",
		"-N",
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ConnectTimeout=15",
		"-o", "ServerAliveInterval=" + strconv.Itoa(int(heartbeatPeriod/time.Second)),
		"-o", "ServerAliveCountMax=" + strconv.Itoa(maxNumHeartbeatPeriodsUntilDisconnect),
		"-o", "PermitLocalCommand=yes",
		"-o", "LocalCommand=echo " + sshTunnelReadySignal,
		"-p", strconv.Itoa(port),
		"-L", server.Addr + ":" + ssh.RemoteServerAddr,
	}
	args = append(args, ssh.ExtraSSHArgs...)
	args = append(args, destination)
	return &TunnelCommandSpec{Command: args, ReadinessProbeString: sshTunnelReadySignal}, nil
}
