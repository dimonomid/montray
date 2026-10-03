package wsclient

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/benbjohnson/clock"

	"github.com/dimonomid/montray/logs"
)

func TestTunnelCommandBuildsSSHForward(t *testing.T) {
	server := ConfigServer{
		Addr: "127.0.0.1:41992",
		Tunnel: &ConfigTunnel{SSH: &ConfigSSHTunnel{
			Host:             "montray.example.com",
			User:             "monitor",
			Port:             2222,
			RemoteServerAddr: "127.0.0.1:41990",
			ExtraSSHArgs:     []string{"-i", "/etc/montray/key", "-J", "bastion.example.com"},
		}},
	}

	got, err := TunnelCommand(server)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ssh", "-N", "-T",
		"-o", "BatchMode=yes",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ConnectTimeout=15",
		"-o", "ServerAliveInterval=10",
		"-o", "ServerAliveCountMax=3",
		"-o", "PermitLocalCommand=yes",
		"-o", "LocalCommand=echo MONTRAY_TUNNEL_READY",
		"-p", "2222",
		"-L", "127.0.0.1:41992:127.0.0.1:41990",
		"-i", "/etc/montray/key", "-J", "bastion.example.com",
		"monitor@montray.example.com",
	}
	if !reflect.DeepEqual(got.Command, want) || got.ReadinessProbeString != sshTunnelReadySignal {
		t.Fatalf("command = %#v, want %#v", got, want)
	}
}

func TestTunnelCommandDefaultsSSHPort(t *testing.T) {
	command, err := TunnelCommand(ConfigServer{
		Addr: "localhost:41992",
		Tunnel: &ConfigTunnel{SSH: &ConfigSSHTunnel{
			Host: "host", User: "user", RemoteServerAddr: "localhost:41990",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := range command.Command {
		if command.Command[i] == "-p" && i+1 < len(command.Command) && command.Command[i+1] == "22" {
			return
		}
	}
	t.Fatalf("command %#v does not contain default SSH port", command)
}

func TestTunnelCommandPreservesCustomArguments(t *testing.T) {
	want := []string{"ssh", "-N", "host"}
	server := ConfigServer{Tunnel: &ConfigTunnel{CustomCommand: &ConfigCustomTunnelCommand{
		Command:        want,
		ReadinessProbe: &ConfigTunnelReadinessProbe{ContainsOutput: "ready"},
	}}}
	got, err := TunnelCommand(server)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Command, want) || got.ReadinessProbeString != "ready" {
		t.Fatalf("command = %#v, want %#v", got, want)
	}
	got.Command[0] = "changed"
	if server.Tunnel.CustomCommand.Command[0] != "ssh" {
		t.Fatal("returned command aliases configuration storage")
	}
}

func TestTunnelSupervisorRestartsExitedCommand(t *testing.T) {
	marker := t.TempDir() + "/runs"
	events := make(chan ServerEvent, 32)
	supervisor := NewTunnelSupervisor(TunnelSupervisorParams{
		ServerID:     "remote",
		Command:      TunnelCommandSpec{Command: []string{"sh", "-c", `printf x >> "$1"`, "sh", marker}},
		Logger:       logs.NewLogger(logs.LoggerParams{Clock: clock.New()}),
		EventCh:      events,
		RestartDelay: 10 * time.Millisecond,
	})
	t.Cleanup(supervisor.Close)

	deadline := time.Now().Add(3 * time.Second)
	for {
		data, err := os.ReadFile(marker)
		if err == nil && len(data) >= 2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("tunnel command did not restart; marker = %q, error = %v", data, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTunnelSupervisorCloseStopsActiveCommand(t *testing.T) {
	events := make(chan ServerEvent, 32)
	supervisor := NewTunnelSupervisor(TunnelSupervisorParams{
		ServerID:     "remote",
		Command:      TunnelCommandSpec{Command: []string{"sh", "-c", "exec sleep 30"}},
		Logger:       logs.NewLogger(logs.LoggerParams{Clock: clock.New()}),
		EventCh:      events,
		RestartDelay: time.Hour,
	})
	time.Sleep(20 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		supervisor.Close()
		supervisor.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not stop the active tunnel command")
	}
}

func TestTunnelSupervisorWaitsForOutputProbe(t *testing.T) {
	events := make(chan ServerEvent, 32)
	supervisor := NewTunnelSupervisor(TunnelSupervisorParams{
		ServerID: "remote",
		Command: TunnelCommandSpec{
			Command:              []string{"sh", "-c", "sleep 0.1; echo tunnel-ready; exec sleep 30"},
			ReadinessProbeString: "tunnel-ready",
		},
		Logger:       logs.NewLogger(logs.LoggerParams{Clock: clock.New()}),
		EventCh:      events,
		RestartDelay: time.Hour,
	})
	t.Cleanup(supervisor.Close)

	ready := make(chan bool, 1)
	interrupt := make(chan struct{})
	go func() { ready <- supervisor.WaitReady(interrupt) }()
	select {
	case <-ready:
		t.Fatal("tunnel became ready before the probe matched")
	case <-time.After(30 * time.Millisecond):
	}
	select {
	case result := <-ready:
		if !result {
			t.Fatal("WaitReady was canceled")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tunnel did not become ready after probe output")
	}
}

func TestTunnelSupervisorWithoutProbeIsReadyAfterStart(t *testing.T) {
	events := make(chan ServerEvent, 32)
	supervisor := NewTunnelSupervisor(TunnelSupervisorParams{
		ServerID: "remote",
		Command: TunnelCommandSpec{
			Command: []string{"sh", "-c", "exec sleep 30"},
		},
		Logger:       logs.NewLogger(logs.LoggerParams{Clock: clock.New()}),
		EventCh:      events,
		RestartDelay: time.Hour,
	})
	t.Cleanup(supervisor.Close)

	interrupt := make(chan struct{})
	ready := make(chan bool, 1)
	go func() { ready <- supervisor.WaitReady(interrupt) }()
	select {
	case result := <-ready:
		if !result {
			t.Fatal("WaitReady was canceled")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tunnel without a probe was not ready after starting")
	}
}

func TestTunnelSupervisorReportsFailureBeforeReadiness(t *testing.T) {
	events := make(chan ServerEvent, 32)
	supervisor := NewTunnelSupervisor(TunnelSupervisorParams{
		ServerID: "remote",
		Command: TunnelCommandSpec{
			Command:              []string{"sh", "-c", "echo less-useful-stdout; echo useful-stderr >&2; exit 7"},
			ReadinessProbeString: "ready",
		},
		Logger:       logs.NewLogger(logs.LoggerParams{Clock: clock.New()}),
		EventCh:      events,
		RestartDelay: time.Hour,
	})
	t.Cleanup(supervisor.Close)

	select {
	case serverEvent := <-events:
		event := serverEvent.Tunnel
		if event.Kind != TunnelEventFailed || !strings.Contains(event.Error, "useful-stderr") {
			t.Fatalf("event = %#v, want failed tunnel event", event)
		}
		if strings.Contains(event.Error, "less-useful-stdout") {
			t.Fatalf("event = %#v, want stderr to take precedence over stdout", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tunnel failure was not reported")
	}
	if supervisor.IsReady() {
		t.Fatal("failed tunnel was marked ready")
	}
}

func TestTunnelSupervisorWaitsForRestartedProcessReadiness(t *testing.T) {
	marker := t.TempDir() + "/first-attempt"
	events := make(chan ServerEvent, 32)
	supervisor := NewTunnelSupervisor(TunnelSupervisorParams{
		ServerID: "remote",
		Command: TunnelCommandSpec{
			Command: []string{
				"sh", "-c",
				`if [ -e "$1" ]; then echo ready; exec sleep 30; fi; : > "$1"; exit 7`,
				"sh", marker,
			},
			ReadinessProbeString: "ready",
		},
		Logger:       logs.NewLogger(logs.LoggerParams{Clock: clock.New()}),
		EventCh:      events,
		RestartDelay: 10 * time.Millisecond,
	})
	t.Cleanup(supervisor.Close)

	select {
	case serverEvent := <-events:
		event := serverEvent.Tunnel
		if event.Kind != TunnelEventFailed {
			t.Fatalf("first event = %#v, want failure", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first tunnel process did not report failure")
	}

	interrupt := make(chan struct{})
	if !supervisor.WaitReady(interrupt) {
		t.Fatal("restarted tunnel did not become ready")
	}
}

func TestReadinessProbeWriterMatchesAcrossWrites(t *testing.T) {
	ready := false
	writer := newReadinessProbeWriter("tunnel-ready", func() { ready = true })
	for _, part := range []string{"ignored tun", "nel-", "ready trailing"} {
		if _, err := writer.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if !ready {
		t.Fatal("writer did not match output split across writes")
	}
}

func TestTunnelFailureOutputRemovesReadinessProbe(t *testing.T) {
	got := tunnelFailureOutput("PROBE", "PROBE\ntunnel failed", "PROBE")
	if got != "tunnel failed" {
		t.Fatalf("output = %q, want %q", got, "tunnel failed")
	}
}

func TestTailOutputWriterKeepsBoundedSuffix(t *testing.T) {
	writer := &tailOutputWriter{}
	input := strings.Repeat("x", maxTunnelFailureOutputBytes) + "useful ending"
	if _, err := writer.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}

	got := writer.String()
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "useful ending") {
		t.Fatalf("captured output = %q", got)
	}
	if len(got) > maxTunnelFailureOutputBytes+len("…") {
		t.Fatalf("captured output length = %d, want at most %d", len(got), maxTunnelFailureOutputBytes+len("…"))
	}
}
