use std::io;
use std::process::Stdio;
use std::time::Duration;

use time::OffsetDateTime;
use tokio::io::{AsyncRead, AsyncReadExt};
use tokio::process::{Child, Command};
use tokio::sync::{mpsc, watch};
use tokio::task::JoinHandle;
use tokio::time::{sleep, timeout};

use crate::config::ServerConfig;
use crate::domain::Event;

use super::client::run_connection_loop;

const READY_MARKER: &str = "MONTRAY_TUNNEL_READY";
const MAX_FAILURE_OUTPUT_BYTES: usize = 1024;
const DEFAULT_RESTART_DELAY: Duration = Duration::from_secs(5);
/// Matches Go's `exec.Cmd.WaitDelay` for inherited tunnel output pipes.
const OUTPUT_DRAIN_TIMEOUT: Duration = Duration::from_secs(1);

/// Executable tunnel command plus an optional output token proving readiness.
#[derive(Clone, Debug, Eq, PartialEq)]
pub(crate) struct CommandSpec {
    pub program: String,
    /// Complete argv without shell interpretation.
    pub args: Vec<String>,
    /// May arrive on stdout or stderr and span reads; absence means immediately ready.
    pub readiness_probe: Option<String>,
}

impl CommandSpec {
    /// Adapts either supported configuration shape to the one process supervisor.
    pub fn for_server(server: &ServerConfig) -> Option<Self> {
        let tunnel = server.tunnel.as_ref()?;
        if let Some(custom) = &tunnel.custom_command {
            let (program, args) = custom
                .command
                .split_first()
                .expect("validated custom tunnel command has an executable");
            return Some(Self {
                program: program.clone(),
                args: args.to_vec(),
                readiness_probe: custom
                    .readiness_probe
                    .as_ref()
                    .map(|probe| probe.contains_output.clone()),
            });
        }

        let ssh = tunnel
            .ssh
            .as_ref()
            .expect("validated tunnel has exactly one command adapter");
        Some(Self::for_ssh(server, ssh))
    }

    /// Reproduces the hardened external-OpenSSH invocation used by the Go watcher.
    ///
    /// `ExitOnForwardFailure` plus `LocalCommand` is the readiness protocol: the
    /// marker is printed only after SSH has established the session and accepted
    /// the local forward. The WebSocket must never dial before that marker.
    fn for_ssh(server: &ServerConfig, ssh: &crate::config::SshTunnelConfig) -> Self {
        let port = if ssh.port == 0 { 22 } else { ssh.port };
        let mut args = vec![
            "-N".into(),
            "-T".into(),
            "-o".into(),
            "BatchMode=yes".into(),
            "-o".into(),
            "ExitOnForwardFailure=yes".into(),
            "-o".into(),
            "ConnectTimeout=15".into(),
            "-o".into(),
            "ServerAliveInterval=10".into(),
            "-o".into(),
            "ServerAliveCountMax=3".into(),
            "-o".into(),
            "PermitLocalCommand=yes".into(),
            "-o".into(),
            format!("LocalCommand=echo {READY_MARKER}"),
            "-p".into(),
            port.to_string(),
            "-L".into(),
            format!("{}:{}", server.addr, ssh.remote_server_addr),
        ];
        args.extend(ssh.extra_ssh_args.iter().cloned());
        args.push(format!("{}@{}", ssh.user, ssh.host));
        Self {
            program: "ssh".into(),
            args,
            readiness_probe: Some(READY_MARKER.into()),
        }
    }
}

/// Owns the tunnel child and the WebSocket nested inside it for one server.
///
/// Ownership is intentionally hierarchical: the tunnel starts first, the client
/// runs only while it is ready, and shutdown waits for both tasks and the child
/// process. This ordering prevents leaked SSH processes and misleading socket
/// incidents when the tunnel is the root cause. SSH and custom commands both
/// enter this exact supervisor after their small configuration adapters run.
pub(crate) async fn run(
    server: ServerConfig,
    events: mpsc::Sender<Event>,
    shutdown: watch::Receiver<bool>,
) {
    let server_id = server.id.clone();
    let spec = CommandSpec::for_server(&server).expect("tunneled server has a command");
    run_with_spec(server, spec, events, shutdown, DEFAULT_RESTART_DELAY).await;
    log::info!("server {server_id} tunnel stopped");
}

/// Supervises repeated tunnel-process generations using an injectable delay.
async fn run_with_spec(
    server: ServerConfig,
    spec: CommandSpec,
    events: mpsc::Sender<Event>,
    mut shutdown: watch::Receiver<bool>,
    restart_delay: Duration,
) {
    loop {
        if *shutdown.borrow() {
            return;
        }
        log::info!("server {} starting tunnel with {}", server.id, spec.program);
        let mut child = match spawn(&spec) {
            Ok(child) => child,
            Err(error) => {
                let details = format!("Failed to start tunnel command: {error}");
                log::error!("server {}: {details}", server.id);
                if !send_tunnel_failure(&events, &server.id, details).await
                    || !wait_to_restart(&mut shutdown, restart_delay).await
                {
                    return;
                }
                continue;
            }
        };
        // On Unix this is also the process-group ID because `spawn` assigns the
        // child as leader. Keep it after `wait`, when `Child::id()` becomes `None`.
        let process_group_id = child.id();

        let stdout = child.stdout.take().expect("tunnel stdout is piped");
        let stderr = child.stderr.take().expect("tunnel stderr is piped");
        let (ready_tx, mut ready_rx) = mpsc::channel(2);
        let stdout_task = tokio::spawn(capture_output(
            stdout,
            spec.readiness_probe.clone(),
            ready_tx.clone(),
        ));
        let stderr_task = tokio::spawn(capture_output(
            stderr,
            spec.readiness_probe.clone(),
            ready_tx,
        ));

        /// Mutually exclusive ways the current tunnel generation can leave startup.
        enum BeforeReady {
            Ready,
            Exited(io::Result<std::process::ExitStatus>),
            Shutdown,
        }
        let outcome = if spec.readiness_probe.is_none() {
            BeforeReady::Ready
        } else {
            tokio::select! {
                marker = ready_rx.recv() => if marker.is_some() { BeforeReady::Ready } else {
                    BeforeReady::Exited(child.wait().await)
                },
                status = child.wait() => BeforeReady::Exited(status),
                _ = shutdown.changed() => BeforeReady::Shutdown,
            }
        };

        match outcome {
            BeforeReady::Ready => {
                log::info!("server {} tunnel is ready", server.id);
                if events
                    .send(Event::TunnelReady {
                        server_id: server.id.clone(),
                        at: wall_clock_now(),
                    })
                    .await
                    .is_err()
                {
                    stop_generation(
                        &server.id,
                        &mut child,
                        process_group_id,
                        stdout_task,
                        stderr_task,
                    )
                    .await;
                    return;
                }
                let (stop_tx, stop_rx) = watch::channel(false);
                let mut connection = tokio::spawn(run_connection_loop(
                    server.clone(),
                    events.clone(),
                    stop_rx,
                    true,
                ));
                /// First owner to finish determines teardown of the nested pair.
                enum WhileReady {
                    Exited(io::Result<std::process::ExitStatus>),
                    ClientStopped,
                    Shutdown,
                }
                let outcome = tokio::select! {
                    status = child.wait() => WhileReady::Exited(status),
                    _ = &mut connection => WhileReady::ClientStopped,
                    _ = shutdown.changed() => WhileReady::Shutdown,
                };
                let _ = stop_tx.send(true);
                match outcome {
                    WhileReady::Exited(status) => {
                        let _ = connection.await;
                        terminate_process_group(process_group_id);
                        let details = failure_details(
                            &server.id,
                            status,
                            stdout_task,
                            stderr_task,
                            spec.readiness_probe.as_deref(),
                        )
                        .await;
                        log::error!("server {} tunnel failed: {details}", server.id);
                        if !send_tunnel_failure(&events, &server.id, details).await
                            || !wait_to_restart(&mut shutdown, restart_delay).await
                        {
                            return;
                        }
                    }
                    WhileReady::ClientStopped => {
                        stop_generation(
                            &server.id,
                            &mut child,
                            process_group_id,
                            stdout_task,
                            stderr_task,
                        )
                        .await;
                        return;
                    }
                    WhileReady::Shutdown => {
                        let _ = connection.await;
                        stop_generation(
                            &server.id,
                            &mut child,
                            process_group_id,
                            stdout_task,
                            stderr_task,
                        )
                        .await;
                        return;
                    }
                }
            }
            BeforeReady::Exited(status) => {
                terminate_process_group(process_group_id);
                let details = failure_details(
                    &server.id,
                    status,
                    stdout_task,
                    stderr_task,
                    spec.readiness_probe.as_deref(),
                )
                .await;
                log::error!("server {} tunnel failed: {details}", server.id);
                if !send_tunnel_failure(&events, &server.id, details).await
                    || !wait_to_restart(&mut shutdown, restart_delay).await
                {
                    return;
                }
            }
            BeforeReady::Shutdown => {
                stop_generation(
                    &server.id,
                    &mut child,
                    process_group_id,
                    stdout_task,
                    stderr_task,
                )
                .await;
                return;
            }
        }
    }
}

/// Spawns the configured executable without a shell and with kill-on-drop as a backstop.
fn spawn(spec: &CommandSpec) -> io::Result<Child> {
    let mut command = Command::new(&spec.program);
    command
        .args(&spec.args)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true);
    isolate_from_terminal_signals(&mut command);
    command.spawn()
}

#[cfg(unix)]
/// Keeps terminal Ctrl+C from killing the child before the parent can reap it cleanly.
fn isolate_from_terminal_signals(command: &mut Command) {
    command.process_group(0);
}

#[cfg(windows)]
/// Gives SSH a process group distinct from the parent console process.
fn isolate_from_terminal_signals(command: &mut Command) {
    const CREATE_NEW_PROCESS_GROUP: u32 = 0x0000_0200;
    command.creation_flags(CREATE_NEW_PROCESS_GROUP);
}

#[cfg(not(any(unix, windows)))]
fn isolate_from_terminal_signals(_command: &mut Command) {}

/// Kills and reaps one command generation, then bounds output-pipe cleanup.
async fn stop_generation(
    server_id: &str,
    child: &mut Child,
    process_group_id: Option<u32>,
    stdout_task: JoinHandle<CapturedOutput>,
    stderr_task: JoinHandle<CapturedOutput>,
) {
    stop_child(child, process_group_id).await;
    let output = drain_output_readers(stdout_task, stderr_task).await;
    if output.timed_out {
        log::warn!(
            "server {server_id} tunnel output pipes remained open after termination; continuing teardown"
        );
    }
}

/// Requests tree termination where supported and reaps the direct child.
async fn stop_child(child: &mut Child, process_group_id: Option<u32>) {
    terminate_process_group(process_group_id);
    let _ = child.start_kill();
    let _ = child.wait().await;
}

#[cfg(unix)]
/// Kills descendants that inherited the isolated tunnel process group.
fn terminate_process_group(process_group_id: Option<u32>) {
    let Some(process_group_id) = process_group_id.and_then(|id| libc::pid_t::try_from(id).ok())
    else {
        return;
    };
    // SAFETY: `spawn` created a new group whose PGID is the captured child PID.
    // A negative PID asks `kill` to signal every process remaining in that group.
    if unsafe { libc::kill(-process_group_id, libc::SIGKILL) } != 0 {
        let error = io::Error::last_os_error();
        if error.raw_os_error() != Some(libc::ESRCH) {
            log::warn!("failed to kill tunnel process group {process_group_id}: {error}");
        }
    }
}

#[cfg(not(unix))]
/// Other platforms rely on direct-child termination plus bounded pipe draining.
fn terminate_process_group(_process_group_id: Option<u32>) {}

async fn wait_to_restart(shutdown: &mut watch::Receiver<bool>, delay: Duration) -> bool {
    log::info!("tunnel will restart in {}s", delay.as_secs_f64());
    tokio::select! {
        _ = sleep(delay) => true,
        _ = shutdown.changed() => false,
    }
}

async fn send_tunnel_failure(events: &mpsc::Sender<Event>, server_id: &str, error: String) -> bool {
    events
        .send(Event::TunnelFailed {
            server_id: server_id.to_owned(),
            at: wall_clock_now(),
            error,
        })
        .await
        .is_ok()
}

/// Joins both pipe readers and turns exit status plus bounded output into one incident body.
async fn failure_details(
    server_id: &str,
    status: io::Result<std::process::ExitStatus>,
    stdout_task: JoinHandle<CapturedOutput>,
    stderr_task: JoinHandle<CapturedOutput>,
    readiness_probe: Option<&str>,
) -> String {
    let output = drain_output_readers(stdout_task, stderr_task).await;
    let mut details = match status {
        Ok(status) if status.success() => "Tunnel command exited unexpectedly".to_owned(),
        Ok(status) => format!("Tunnel command exited: {status}"),
        Err(error) => format!("Waiting for tunnel command failed: {error}"),
    };
    if output.timed_out {
        log::warn!(
            "server {server_id} tunnel output pipes remained open after command exit; continuing restart"
        );
        details.push_str(
            r#"

Tunnel output pipes remained open after the command exited"#,
        );
    }
    let failure_output = failure_output(
        &output.stderr.text(),
        &output.stdout.text(),
        readiness_probe,
    );
    if !failure_output.is_empty() {
        details.push_str("\n\n");
        details.push_str(&failure_output);
    }
    details
}

/// Output retained from readers that finished before the common deadline.
struct DrainedOutput {
    stdout: CapturedOutput,
    stderr: CapturedOutput,
    /// At least one inherited pipe remained open after the direct child ended.
    timed_out: bool,
}

/// Waits concurrently for both pipes, aborting either reader after one second.
async fn drain_output_readers(
    stdout_task: JoinHandle<CapturedOutput>,
    stderr_task: JoinHandle<CapturedOutput>,
) -> DrainedOutput {
    drain_output_readers_with_timeout(stdout_task, stderr_task, OUTPUT_DRAIN_TIMEOUT).await
}

/// Injectable-timeout implementation used by the inherited-pipe regression test.
async fn drain_output_readers_with_timeout(
    stdout_task: JoinHandle<CapturedOutput>,
    stderr_task: JoinHandle<CapturedOutput>,
    deadline: Duration,
) -> DrainedOutput {
    let (stdout, stderr) = tokio::join!(
        drain_output_reader(stdout_task, deadline),
        drain_output_reader(stderr_task, deadline),
    );
    DrainedOutput {
        stdout: stdout.0,
        stderr: stderr.0,
        timed_out: stdout.1 || stderr.1,
    }
}

/// Returns captured output or aborts a reader whose inherited pipe never closes.
async fn drain_output_reader(
    mut task: JoinHandle<CapturedOutput>,
    deadline: Duration,
) -> (CapturedOutput, bool) {
    match timeout(deadline, &mut task).await {
        Ok(result) => (result.unwrap_or_default(), false),
        Err(_) => {
            task.abort();
            let _ = task.await;
            (CapturedOutput::default(), true)
        }
    }
}

/// Captured diagnostic tail from one tunnel-process output stream.
#[derive(Default)]
struct CapturedOutput {
    tail: TailBuffer,
}

impl CapturedOutput {
    fn text(&self) -> String {
        self.tail.text()
    }
}

/// Drains one child pipe continuously while independently probing for readiness.
///
/// Both stdout and stderr must be drained concurrently: tunnel processes can
/// write enough output to fill either pipe and otherwise deadlock the child.
async fn capture_output<R>(
    mut reader: R,
    probe: Option<String>,
    ready: mpsc::Sender<()>,
) -> CapturedOutput
where
    R: AsyncRead + Unpin,
{
    let mut output = CapturedOutput::default();
    let mut matcher = probe.as_deref().map(str::as_bytes).map(ProbeMatcher::new);
    let mut buffer = [0; 256];
    loop {
        match reader.read(&mut buffer).await {
            Ok(0) => break,
            Ok(count) => {
                let chunk = &buffer[..count];
                output.tail.push(chunk);
                if matcher.as_mut().is_some_and(|matcher| matcher.push(chunk)) {
                    let _ = ready.try_send(());
                }
            }
            Err(error) => {
                output.tail.push(
                    format!(
                        r#"
reading tunnel output failed: {error}"#
                    )
                    .as_bytes(),
                );
                break;
            }
        }
    }
    output
}

/// Streaming exact-substring matcher that handles a marker split across reads.
struct ProbeMatcher {
    probe: Vec<u8>,
    /// At most `probe.len() - 1` trailing bytes needed for the next boundary.
    tail: Vec<u8>,
    /// Makes notification edge-triggered even if the marker is printed repeatedly.
    complete: bool,
}

impl ProbeMatcher {
    fn new(probe: &[u8]) -> Self {
        Self {
            probe: probe.to_vec(),
            tail: Vec::new(),
            complete: probe.is_empty(),
        }
    }

    /// Feeds one chunk and returns true exactly once when the probe first appears.
    fn push(&mut self, bytes: &[u8]) -> bool {
        if self.complete {
            return false;
        }
        let mut combined = Vec::with_capacity(self.tail.len() + bytes.len());
        combined.extend_from_slice(&self.tail);
        combined.extend_from_slice(bytes);
        if combined
            .windows(self.probe.len())
            .any(|window| window == self.probe)
        {
            self.complete = true;
            self.tail.clear();
            return true;
        }
        let keep = (self.probe.len() - 1).min(combined.len());
        self.tail = combined[combined.len() - keep..].to_vec();
        false
    }
}

/// Bounded suffix buffer used to keep incident details useful without unbounded RAM.
#[derive(Default)]
struct TailBuffer {
    bytes: Vec<u8>,
    /// Records loss even if subsequent chunks fit, so rendered text shows an ellipsis.
    truncated: bool,
}

impl TailBuffer {
    /// Appends output while retaining only the newest bytes, which tend to hold
    /// the process's final and most useful diagnostic.
    fn push(&mut self, bytes: &[u8]) {
        if bytes.len() >= MAX_FAILURE_OUTPUT_BYTES {
            self.bytes = bytes[bytes.len() - MAX_FAILURE_OUTPUT_BYTES..].to_vec();
            self.truncated = true;
            return;
        }
        let overflow = self
            .bytes
            .len()
            .saturating_add(bytes.len())
            .saturating_sub(MAX_FAILURE_OUTPUT_BYTES);
        if overflow > 0 {
            self.bytes.drain(..overflow);
            self.truncated = true;
        }
        self.bytes.extend_from_slice(bytes);
    }

    /// Decodes even malformed process output lossily and marks a discarded
    /// prefix so the resulting incident is not mistaken for complete output.
    fn text(&self) -> String {
        let text = String::from_utf8_lossy(&self.bytes).trim().to_owned();
        if self.truncated && !text.is_empty() {
            format!("…{text}")
        } else {
            text
        }
    }
}

/// Selects human diagnostics, preferring stderr, and strips protocol-only probe lines.
fn failure_output(stderr: &str, stdout: &str, readiness_probe: Option<&str>) -> String {
    fn clean(output: &str, probe: Option<&str>) -> String {
        output
            .lines()
            .filter(|line| Some(line.trim()) != probe)
            .collect::<Vec<_>>()
            .join("\n")
            .trim()
            .to_owned()
    }
    let stderr = clean(stderr, readiness_probe);
    if stderr.is_empty() {
        clean(stdout, readiness_probe)
    } else {
        stderr
    }
}

fn wall_clock_now() -> OffsetDateTime {
    OffsetDateTime::now_utc()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::{
        CustomTunnelCommandConfig, SshTunnelConfig, TunnelConfig, TunnelReadinessProbeConfig,
    };
    use futures_util::StreamExt;
    use tokio::net::TcpListener;
    use tokio::time::timeout;

    fn plain_server(addr: String) -> ServerConfig {
        ServerConfig {
            id: "remote".into(),
            addr,
            tls: None,
            auth: None,
            tunnel: None,
        }
    }

    #[test]
    fn builds_the_same_ssh_forward_as_the_old_client() {
        let server = ServerConfig {
            id: "remote".into(),
            addr: "127.0.0.1:41992".into(),
            tls: None,
            auth: None,
            tunnel: Some(TunnelConfig {
                ssh: Some(SshTunnelConfig {
                    host: "montray.example.com".into(),
                    user: "monitor".into(),
                    port: 2222,
                    remote_server_addr: "127.0.0.1:41990".into(),
                    extra_ssh_args: vec![
                        "-i".into(),
                        "/etc/montray/key".into(),
                        "-J".into(),
                        "bastion.example.com".into(),
                    ],
                }),
                custom_command: None,
            }),
        };
        let command = CommandSpec::for_server(&server).unwrap();
        assert_eq!(command.program, "ssh");
        assert_eq!(
            command.args,
            [
                "-N",
                "-T",
                "-o",
                "BatchMode=yes",
                "-o",
                "ExitOnForwardFailure=yes",
                "-o",
                "ConnectTimeout=15",
                "-o",
                "ServerAliveInterval=10",
                "-o",
                "ServerAliveCountMax=3",
                "-o",
                "PermitLocalCommand=yes",
                "-o",
                "LocalCommand=echo MONTRAY_TUNNEL_READY",
                "-p",
                "2222",
                "-L",
                "127.0.0.1:41992:127.0.0.1:41990",
                "-i",
                "/etc/montray/key",
                "-J",
                "bastion.example.com",
                "monitor@montray.example.com",
            ]
        );
        assert_eq!(command.readiness_probe.as_deref(), Some(READY_MARKER));
    }

    #[test]
    fn defaults_the_ssh_port_to_22() {
        let server = ServerConfig {
            id: "remote".into(),
            addr: "localhost:41992".into(),
            tls: None,
            auth: None,
            tunnel: Some(TunnelConfig {
                ssh: Some(SshTunnelConfig {
                    host: "host".into(),
                    user: "user".into(),
                    port: 0,
                    remote_server_addr: "localhost:41990".into(),
                    extra_ssh_args: Vec::new(),
                }),
                custom_command: None,
            }),
        };
        let command = CommandSpec::for_server(&server).unwrap();
        assert!(command.args.windows(2).any(|args| args == ["-p", "22"]));
    }

    #[test]
    fn custom_command_is_a_direct_adapter_to_the_generic_spec() {
        let mut server = plain_server("localhost:41992".into());
        server.tunnel = Some(TunnelConfig {
            ssh: None,
            custom_command: Some(CustomTunnelCommandConfig {
                command: vec!["my-tunnel".into(), "--flag".into(), "value".into()],
                readiness_probe: Some(TunnelReadinessProbeConfig {
                    contains_output: "READY".into(),
                }),
            }),
        });
        assert_eq!(
            CommandSpec::for_server(&server).unwrap(),
            CommandSpec {
                program: "my-tunnel".into(),
                args: vec!["--flag".into(), "value".into()],
                readiness_probe: Some("READY".into()),
            }
        );
    }

    #[test]
    fn readiness_probe_matches_across_output_chunks() {
        let mut matcher = ProbeMatcher::new(b"tunnel-ready");
        assert!(!matcher.push(b"ignored tun"));
        assert!(!matcher.push(b"nel-"));
        assert!(matcher.push(b"ready trailing"));
        assert!(!matcher.push(b"tunnel-ready"));
    }

    #[test]
    fn failure_output_prefers_stderr_and_removes_protocol_marker() {
        assert_eq!(
            failure_output(
                r#"MONTRAY_TUNNEL_READY
ssh failed"#,
                "less useful",
                Some(READY_MARKER)
            ),
            "ssh failed"
        );
        assert_eq!(
            failure_output(
                "",
                r#"MONTRAY_TUNNEL_READY
stdout failed"#,
                Some(READY_MARKER),
            ),
            "stdout failed"
        );
        assert_eq!(
            failure_output("", "ordinary output", None),
            "ordinary output"
        );
    }

    #[test]
    fn failure_output_tail_is_bounded() {
        let mut tail = TailBuffer::default();
        tail.push(&vec![b'x'; MAX_FAILURE_OUTPUT_BYTES]);
        tail.push(b"useful ending");
        let text = tail.text();
        assert!(text.starts_with('…'));
        assert!(text.ends_with("useful ending"));
        assert!(text.len() <= MAX_FAILURE_OUTPUT_BYTES + '…'.len_utf8());
    }

    #[tokio::test(flavor = "current_thread")]
    async fn output_reader_deadline_aborts_a_pipe_that_never_closes() {
        let stdout_task = tokio::spawn(std::future::pending::<CapturedOutput>());
        let stderr_task = tokio::spawn(async { CapturedOutput::default() });

        let output =
            drain_output_readers_with_timeout(stdout_task, stderr_task, Duration::from_millis(10))
                .await;

        assert!(output.timed_out);
        assert!(output.stdout.text().is_empty());
        assert!(output.stderr.text().is_empty());
    }

    #[cfg(unix)]
    #[tokio::test(flavor = "current_thread")]
    async fn websocket_dial_waits_for_marker_from_stderr() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let server = plain_server(listener.local_addr().unwrap().to_string());
        let spec = CommandSpec {
            program: "sh".into(),
            args: vec![
                "-c".into(),
                format!("sleep 0.15; echo {READY_MARKER} >&2; exec sleep 30"),
            ],
            readiness_probe: Some(READY_MARKER.into()),
        };
        let (events_tx, mut events_rx) = mpsc::channel(16);
        let (shutdown_tx, shutdown_rx) = watch::channel(false);
        let task = tokio::spawn(run_with_spec(
            server,
            spec,
            events_tx,
            shutdown_rx,
            Duration::from_secs(60),
        ));

        assert!(
            timeout(Duration::from_millis(50), listener.accept())
                .await
                .is_err(),
            "WebSocket TCP connection was attempted before tunnel readiness"
        );
        let first_event = timeout(Duration::from_secs(3), events_rx.recv())
            .await
            .unwrap()
            .unwrap();
        assert!(matches!(first_event, Event::TunnelReady { .. }));
        timeout(Duration::from_secs(3), listener.accept())
            .await
            .expect("WebSocket TCP connection was not attempted after readiness")
            .unwrap();

        shutdown_tx.send(true).unwrap();
        timeout(Duration::from_secs(3), task)
            .await
            .expect("tunnel task did not stop")
            .unwrap();
    }

    #[cfg(unix)]
    #[tokio::test(flavor = "current_thread")]
    async fn custom_command_without_probe_is_ready_immediately() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let mut server = plain_server(listener.local_addr().unwrap().to_string());
        server.tunnel = Some(TunnelConfig {
            ssh: None,
            custom_command: Some(CustomTunnelCommandConfig {
                command: vec!["sh".into(), "-c".into(), "exec sleep 30".into()],
                readiness_probe: None,
            }),
        });
        let (events_tx, mut events_rx) = mpsc::channel(16);
        let (shutdown_tx, shutdown_rx) = watch::channel(false);
        let task = tokio::spawn(run(server, events_tx, shutdown_rx));

        let ready = timeout(Duration::from_secs(3), events_rx.recv())
            .await
            .unwrap()
            .unwrap();
        assert!(matches!(ready, Event::TunnelReady { .. }));
        timeout(Duration::from_secs(3), listener.accept())
            .await
            .expect("WebSocket TCP connection was not attempted immediately")
            .unwrap();

        shutdown_tx.send(true).unwrap();
        timeout(Duration::from_secs(3), task)
            .await
            .expect("custom tunnel task did not stop")
            .unwrap();
    }

    #[cfg(unix)]
    #[tokio::test(flavor = "current_thread")]
    async fn failed_tunnel_reports_output_then_restarts_and_becomes_ready() {
        let directory = tempfile::tempdir().unwrap();
        let attempt = directory.path().join("first-attempt");
        let server = plain_server("127.0.0.1:9".into());
        let spec = CommandSpec {
            program: "sh".into(),
            args: vec![
                "-c".into(),
                format!(
                    "if [ -e \"$1\" ]; then echo {READY_MARKER}; exec sleep 30; fi; : > \"$1\"; echo useful-ssh-error >&2; exit 7"
                ),
                "sh".into(),
                attempt.to_string_lossy().into_owned(),
            ],
            readiness_probe: Some(READY_MARKER.into()),
        };
        let (events_tx, mut events_rx) = mpsc::channel(16);
        let (shutdown_tx, shutdown_rx) = watch::channel(false);
        let task = tokio::spawn(run_with_spec(
            server,
            spec,
            events_tx,
            shutdown_rx,
            Duration::from_millis(10),
        ));

        let failure = timeout(Duration::from_secs(3), events_rx.recv())
            .await
            .unwrap()
            .unwrap();
        assert!(matches!(
            failure,
            Event::TunnelFailed { error, .. } if error.contains("useful-ssh-error")
        ));
        let ready = timeout(Duration::from_secs(3), events_rx.recv())
            .await
            .unwrap()
            .unwrap();
        assert!(matches!(ready, Event::TunnelReady { .. }));

        shutdown_tx.send(true).unwrap();
        timeout(Duration::from_secs(3), task)
            .await
            .expect("tunnel task did not stop")
            .unwrap();
    }

    #[cfg(unix)]
    #[tokio::test(flavor = "current_thread")]
    async fn background_child_inheriting_pipes_does_not_block_failure_or_restart() {
        let directory = tempfile::tempdir().unwrap();
        let attempted = directory.path().join("attempted");
        let server = plain_server("127.0.0.1:9".into());
        let spec = CommandSpec {
            program: "sh".into(),
            args: vec![
                "-c".into(),
                format!(
                    r#"if [ -e "$1" ]; then echo {READY_MARKER}; exec sleep 30; fi
: > "$1"
sleep 30 &
echo leader-exited >&2
exit 7"#
                ),
                "sh".into(),
                attempted.to_string_lossy().into_owned(),
            ],
            readiness_probe: Some(READY_MARKER.into()),
        };
        let (events_tx, mut events_rx) = mpsc::channel(16);
        let (shutdown_tx, shutdown_rx) = watch::channel(false);
        let task = tokio::spawn(run_with_spec(
            server,
            spec,
            events_tx,
            shutdown_rx,
            Duration::from_millis(10),
        ));

        let failure = timeout(Duration::from_secs(3), events_rx.recv())
            .await
            .expect("inherited output pipes blocked tunnel failure")
            .unwrap();
        assert!(matches!(
            failure,
            Event::TunnelFailed { error, .. } if error.contains("leader-exited")
        ));
        let ready = timeout(Duration::from_secs(3), events_rx.recv())
            .await
            .expect("inherited output pipes blocked tunnel restart")
            .unwrap();
        assert!(matches!(ready, Event::TunnelReady { .. }));

        shutdown_tx.send(true).unwrap();
        timeout(Duration::from_secs(3), task)
            .await
            .expect("restarted tunnel did not stop")
            .unwrap();
    }

    #[cfg(unix)]
    #[tokio::test(flavor = "current_thread")]
    async fn background_child_inheriting_pipes_does_not_block_shutdown() {
        let server = plain_server("127.0.0.1:9".into());
        let spec = CommandSpec {
            program: "sh".into(),
            args: vec!["-c".into(), "sleep 30 & wait".into()],
            readiness_probe: None,
        };
        let (events_tx, mut events_rx) = mpsc::channel(16);
        let (shutdown_tx, shutdown_rx) = watch::channel(false);
        let task = tokio::spawn(run_with_spec(
            server,
            spec,
            events_tx,
            shutdown_rx,
            Duration::from_secs(60),
        ));

        let ready = timeout(Duration::from_secs(3), events_rx.recv())
            .await
            .unwrap()
            .unwrap();
        assert!(matches!(ready, Event::TunnelReady { .. }));
        shutdown_tx.send(true).unwrap();
        timeout(Duration::from_secs(3), task)
            .await
            .expect("inherited output pipes blocked tunnel shutdown")
            .unwrap();
    }

    #[cfg(unix)]
    #[tokio::test(flavor = "current_thread")]
    async fn tunnel_death_closes_an_active_websocket_and_reports_only_tunnel_failure() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let server = plain_server(listener.local_addr().unwrap().to_string());
        let websocket_server = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.unwrap();
            let mut socket = tokio_tungstenite::accept_async(stream).await.unwrap();
            socket.next().await
        });

        let directory = tempfile::tempdir().unwrap();
        let stop = directory.path().join("stop-tunnel");
        let spec = CommandSpec {
            program: "sh".into(),
            args: vec![
                "-c".into(),
                format!(
                    "echo {READY_MARKER}; while [ ! -e \"$1\" ]; do sleep 0.01; done; echo tunnel-process-died >&2; exit 7"
                ),
                "sh".into(),
                stop.to_string_lossy().into_owned(),
            ],
            readiness_probe: Some(READY_MARKER.into()),
        };
        let (events_tx, mut events_rx) = mpsc::channel(16);
        let (shutdown_tx, shutdown_rx) = watch::channel(false);
        let task = tokio::spawn(run_with_spec(
            server,
            spec,
            events_tx,
            shutdown_rx,
            Duration::from_secs(60),
        ));

        let ready = timeout(Duration::from_secs(3), events_rx.recv())
            .await
            .unwrap()
            .unwrap();
        assert!(matches!(ready, Event::TunnelReady { .. }));
        let connected = timeout(Duration::from_secs(3), events_rx.recv())
            .await
            .unwrap()
            .unwrap();
        assert!(matches!(connected, Event::Connected { .. }));

        std::fs::write(&stop, b"stop").unwrap();
        let failure = timeout(Duration::from_secs(3), events_rx.recv())
            .await
            .unwrap()
            .unwrap();
        assert!(matches!(
            failure,
            Event::TunnelFailed { error, .. } if error.contains("tunnel-process-died")
        ));
        assert!(
            events_rx.try_recv().is_err(),
            "tunnel death emitted a redundant WebSocket disconnect"
        );
        timeout(Duration::from_secs(3), websocket_server)
            .await
            .expect("server did not observe the WebSocket closing")
            .unwrap();

        shutdown_tx.send(true).unwrap();
        timeout(Duration::from_secs(3), task)
            .await
            .expect("tunnel task did not stop")
            .unwrap();
    }
}
