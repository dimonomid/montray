use std::collections::{BTreeMap, HashSet};
use std::sync::Arc;
use std::thread::{self, JoinHandle};

use anyhow::{Context, Result};
use time::OffsetDateTime;
use tokio::sync::{mpsc, watch};
use tokio::task::JoinSet;
use tokio::time::Instant;

use crate::config::Config;
use crate::domain::{
    AppState, Effect, Event, Incident, IncidentState, Reducer, SnoozeAction, UiSnapshot,
};
use crate::network;
use crate::notification::NotificationDispatcher;

/// Thread-safe handoff from the reducer thread to the Slint event loop.
type Publisher = Arc<dyn Fn(UiSnapshot) + Send + Sync>;
/// Blocking persistence callback, executed through Tokio's blocking pool.
type SnoozePersister = Arc<dyn Fn(&BTreeMap<String, OffsetDateTime>) -> Result<()> + Send + Sync>;

const MAX_LOG_DETAILS_CHARS: usize = 1_000;
/// Minimum delay between failed automatic snooze-expiration writes.
const EXPIRY_PERSISTENCE_RETRY_DELAY: std::time::Duration = std::time::Duration::from_secs(10);

/// One deferred structured log record derived from a domain event.
#[derive(Debug, Eq, PartialEq)]
struct EventLogLine {
    level: log::Level,
    message: String,
}

/// Per-server logging phase, deliberately separate from domain state.
#[derive(Default)]
struct EventLogState {
    /// Connected servers whose next notification is a complete synchronization point.
    awaiting_snapshot: HashSet<String>,
    /// Servers seen at least once, including those whose first event lacked `Connected`.
    synchronized: HashSet<String>,
}

impl EventLogState {
    /// Converts an event into stable, bounded log lines before the event is consumed.
    fn lines(&mut self, event: &Event) -> Vec<EventLogLine> {
        match event {
            Event::Connected { server_id, .. } => {
                self.awaiting_snapshot.insert(server_id.clone());
                Vec::new()
            }
            Event::Notification {
                server_id, data, ..
            } => {
                let was_awaiting = self.awaiting_snapshot.remove(server_id);
                let first_snapshot = self.synchronized.insert(server_id.clone());
                let initial = was_awaiting || first_snapshot;
                let mut lines = Vec::new();
                if initial {
                    lines.push(info(format!(
                        "server {server_id} synchronized: {} ongoing incidents",
                        data.total.len()
                    )));
                    lines.extend(
                        data.total
                            .iter()
                            .map(|incident| info(incident_line(server_id, "ongoing", incident))),
                    );
                } else {
                    lines.push(info(format!(
                        "server {server_id} incident update: {} added, {} updated, {} resolved; {} ongoing incidents",
                        data.added.len(),
                        data.updated.len(),
                        data.removed.len(),
                        data.total.len()
                    )));
                    lines.extend(
                        data.added
                            .iter()
                            .map(|incident| info(incident_line(server_id, "added", incident))),
                    );
                    lines.extend(
                        data.updated
                            .iter()
                            .map(|incident| info(incident_line(server_id, "updated", incident))),
                    );
                    lines.extend(data.removed.iter().map(|incident| {
                        info(format!(
                            "server {server_id} resolved incident {}",
                            incident.key
                        ))
                    }));
                }
                lines
            }
            Event::Heartbeat { server_id, .. } => vec![EventLogLine {
                level: log::Level::Debug,
                message: format!("server {server_id} received heartbeat"),
            }],
            Event::ForgetStale { key } => vec![info(format!("forgot stale incident {key}"))],
            _ => Vec::new(),
        }
    }
}

fn info(message: String) -> EventLogLine {
    EventLogLine {
        level: log::Level::Info,
        message,
    }
}

/// Produces success logs only after the proposed snooze map is durable.
fn snooze_success_lines(action: &SnoozeAction) -> Vec<EventLogLine> {
    match action {
        SnoozeAction::Set { key, until } => vec![info(format!(
            "snoozed incident {key} until {}",
            until.unix_timestamp()
        ))],
        SnoozeAction::Remove { key } => vec![info(format!("unsnoozed incident {key}"))],
        SnoozeAction::Expire { keys } => keys
            .iter()
            .map(|key| info(format!("snooze expired for incident {key}")))
            .collect(),
    }
}

/// Builds user feedback for failed explicit actions without notifying on
/// automatic expiration retries.
fn snooze_failure_notification(action: &SnoozeAction, details: &str) -> Option<(String, String)> {
    let title = match action {
        SnoozeAction::Set { key, .. } => format!("Failed to snooze incident {key}"),
        SnoozeAction::Remove { key } => format!("Failed to unsnooze incident {key}"),
        SnoozeAction::Expire { .. } => return None,
    };
    Some((
        title,
        format!(
            r#"The snooze state was not changed.

{details}"#
        ),
    ))
}

/// Builds one reminder for each still-ongoing incident whose snooze expired.
///
/// This is evaluated only after the expiration map is durable. Explicit
/// unsnoozes therefore stay silent, and incidents that resolved while hidden
/// are absent from the snapshot and do not produce a notification.
fn snooze_expiry_notifications(
    action: &SnoozeAction,
    state: &AppState,
    now: OffsetDateTime,
) -> Vec<(String, String)> {
    let SnoozeAction::Expire { keys } = action else {
        return Vec::new();
    };
    let active = state.snapshot(now).active;
    keys.iter()
        .filter_map(|key| {
            active
                .iter()
                .find(|incident| incident.key == *key)
                .map(|incident| {
                    (
                        format!("Snooze ended: {}", incident.key),
                        incident.details.clone(),
                    )
                })
        })
        .collect()
}

/// Queues a runtime notification when desktop delivery initialized successfully.
fn enqueue_desktop_notification(
    notifications: &mut Option<NotificationDispatcher>,
    title: String,
    body: String,
) {
    if let Some(dispatcher) = notifications {
        dispatcher.enqueue(title, body);
    }
}

/// Applies the retry cooldown only to automatic expiration cleanup.
fn snooze_persistence_is_due(
    action: &SnoozeAction,
    expiry_retry_not_before: Option<Instant>,
    now: Instant,
) -> bool {
    !matches!(action, SnoozeAction::Expire { .. })
        || expiry_retry_not_before.is_none_or(|deadline| now >= deadline)
}

fn incident_line(server_id: &str, action: &str, incident: &Incident) -> String {
    let state = match incident.state {
        IncidentState::Ok => "ok",
        IncidentState::Warning => "warning",
        IncidentState::Error => "error",
    };
    let mut line = format!(
        "server {server_id} {action} incident {} ({state})",
        incident.key
    );
    if !incident.details.is_empty() {
        line.push_str(": ");
        line.push_str(&log_details(&incident.details));
    }
    line
}

/// Flattens untrusted incident details into one bounded physical log line.
///
/// Control characters and Unicode line separators are replaced to prevent one
/// remote value from forging log records or flooding a terminal/file.
fn log_details(details: &str) -> String {
    let flattened = details
        .chars()
        .map(|character| {
            if character.is_control() || matches!(character, '\u{2028}' | '\u{2029}') {
                ' '
            } else {
                character
            }
        })
        .collect::<String>();
    let mut characters = flattened.chars();
    let mut limited = characters
        .by_ref()
        .take(MAX_LOG_DETAILS_CHARS)
        .collect::<String>();
    if characters.next().is_some() {
        limited.push('…');
    }
    limited
}

/// User actions accepted from the Slint thread.
#[derive(Debug)]
pub enum Command {
    /// Relative duration is converted to an absolute deadline on the runtime thread.
    Snooze {
        key: String,
        duration: time::Duration,
    },
    Unsnooze {
        key: String,
    },
    ForgetStale {
        key: String,
    },
}

/// Owns the dedicated Tokio thread and its control channels.
///
/// Call [`RuntimeHandle::shutdown`] for deterministic teardown. `Drop` only
/// signals cancellation because joining from arbitrary drop contexts could block
/// or deadlock; normal application shutdown explicitly consumes the handle.
pub struct RuntimeHandle {
    commands: mpsc::Sender<Command>,
    /// Broadcast cancellation observed by every server and tunnel generation.
    shutdown: watch::Sender<bool>,
    /// `Option` permits `shutdown` to take and join exactly once.
    thread: Option<JoinHandle<()>>,
}

impl RuntimeHandle {
    /// Starts a current-thread Tokio runtime on a dedicated OS thread.
    ///
    /// Slint retains its required UI thread while all network state stays
    /// serialized on this thread. Bounded channels provide backpressure rather
    /// than allowing an outage or update burst to grow memory without limit.
    pub fn start(
        mut config: Config,
        snoozes: BTreeMap<String, OffsetDateTime>,
        publish: Publisher,
        persist: SnoozePersister,
    ) -> Result<Self> {
        config.resolve_tunnel_addresses()?;
        let (commands, command_rx) = mpsc::channel(32);
        let (shutdown, shutdown_rx) = watch::channel(false);
        let thread = thread::Builder::new()
            .name("montray-ui-runtime".into())
            .spawn(move || run(config, snoozes, publish, persist, command_rx, shutdown_rx))
            .context("failed to start async runtime thread")?;
        Ok(Self {
            commands,
            shutdown,
            thread: Some(thread),
        })
    }

    pub fn commands(&self) -> mpsc::Sender<Command> {
        self.commands.clone()
    }

    /// Signals every worker and blocks until tunnels, sockets, and Tokio stop.
    pub fn shutdown(mut self) {
        log::debug!("requesting async runtime shutdown");
        let _ = self.shutdown.send(true);
        if let Some(thread) = self.thread.take() {
            log::debug!("waiting for async runtime thread to stop");
            if thread.join().is_err() {
                log::error!("async runtime thread panicked during shutdown");
            }
        }
    }
}

impl Drop for RuntimeHandle {
    fn drop(&mut self) {
        let _ = self.shutdown.send(true);
    }
}

/// Builds and drives Tokio on the already-created runtime OS thread.
fn run(
    config: Config,
    snoozes: BTreeMap<String, OffsetDateTime>,
    publish: Publisher,
    persist: SnoozePersister,
    commands: mpsc::Receiver<Command>,
    shutdown: watch::Receiver<bool>,
) {
    log::debug!("async runtime thread started");
    let runtime = match tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
    {
        Ok(runtime) => runtime,
        Err(error) => {
            log::error!("failed to create async runtime: {error}");
            return;
        }
    };
    runtime.block_on(run_async(
        config, snoozes, publish, persist, commands, shutdown,
    ));
    // Network tasks have been joined by run_async. Drop Tokio before reporting
    // the runtime stopped so any remaining auxiliary work is cleaned up too.
    drop(runtime);
    log::info!("async runtime stopped");
}

/// Main event pump and sole owner of the reducer.
///
/// The one-second tick republishes even without a mutation so relative timestamps
/// advance. Snooze writes are awaited and acknowledged before reducer commit;
/// failed explicit actions notify the user, while failed expiry cleanup is
/// proposed every tick but attempted at most once per retry interval. Desktop
/// notifications pass through one bounded, nonblocking FIFO so a slow notification
/// daemon cannot stall network processing or create unbounded blocking jobs.
async fn run_async(
    config: Config,
    snoozes: BTreeMap<String, OffsetDateTime>,
    publish: Publisher,
    persist: SnoozePersister,
    mut commands: mpsc::Receiver<Command>,
    mut shutdown: watch::Receiver<bool>,
) {
    let mut notifications = match NotificationDispatcher::start() {
        Ok(dispatcher) => Some(dispatcher),
        Err(error) => {
            log::error!("desktop notifications are unavailable: {error:#}");
            None
        }
    };
    let ids = config
        .ws_client
        .servers
        .iter()
        .map(|server| server.id.clone())
        .collect();
    let mut reducer = Reducer::new(AppState::new(ids, snoozes));
    let mut event_logs = EventLogState::default();
    let mut expiry_retry_not_before = None;
    let (events_tx, mut events_rx) = mpsc::channel(64);
    let mut network_tasks = JoinSet::new();
    for server in config.ws_client.servers {
        network_tasks.spawn(network::run_client(
            server,
            events_tx.clone(),
            shutdown.clone(),
        ));
    }
    (publish)(reducer.state().snapshot(wall_clock_now()));
    let mut ticks = tokio::time::interval(std::time::Duration::from_secs(1));
    loop {
        let event = tokio::select! {
            Some(event) = events_rx.recv() => event,
            Some(command) = commands.recv() => command_event(command),
            _ = ticks.tick() => Event::Tick { at: wall_clock_now() },
            _ = shutdown.changed() => {
                log::info!("stopping network services");
                break;
            },
            else => break,
        };
        let refresh = matches!(event, Event::Tick { .. });
        for line in event_logs.lines(&event) {
            log::log!(line.level, "{}", line.message);
        }
        let transition = reducer.reduce(event);
        let mut changed = transition.changed;
        for effect in transition.effects {
            match effect {
                Effect::Notify { title, body } => {
                    enqueue_desktop_notification(&mut notifications, title, body);
                }
                Effect::PersistSnoozes { snoozes, action } => {
                    if !snooze_persistence_is_due(&action, expiry_retry_not_before, Instant::now())
                    {
                        continue;
                    }
                    let automatic_expiry = matches!(&action, SnoozeAction::Expire { .. });
                    let persist = persist.clone();
                    match tokio::task::spawn_blocking(move || persist(&snoozes).map(|()| snoozes))
                        .await
                    {
                        Ok(Ok(snoozes)) => {
                            expiry_retry_not_before = None;
                            for line in snooze_success_lines(&action) {
                                log::log!(line.level, "{}", line.message);
                            }
                            let committed = reducer.reduce(Event::SnoozesPersisted { snoozes });
                            debug_assert!(committed.effects.is_empty());
                            changed |= committed.changed;
                            for (title, body) in snooze_expiry_notifications(
                                &action,
                                reducer.state(),
                                wall_clock_now(),
                            ) {
                                enqueue_desktop_notification(&mut notifications, title, body);
                            }
                        }
                        Ok(Err(error)) => {
                            if automatic_expiry {
                                expiry_retry_not_before =
                                    Some(Instant::now() + EXPIRY_PERSISTENCE_RETRY_DELAY);
                            }
                            let details = format!("{error:#}");
                            log::error!("failed to persist snoozes: {details}");
                            if let Some((title, body)) =
                                snooze_failure_notification(&action, &details)
                            {
                                enqueue_desktop_notification(&mut notifications, title, body);
                            }
                        }
                        Err(error) => {
                            if automatic_expiry {
                                expiry_retry_not_before =
                                    Some(Instant::now() + EXPIRY_PERSISTENCE_RETRY_DELAY);
                            }
                            let details = format!("snooze persistence worker failed: {error}");
                            log::error!("{details}");
                            if let Some((title, body)) =
                                snooze_failure_notification(&action, &details)
                            {
                                enqueue_desktop_notification(&mut notifications, title, body);
                            }
                        }
                    }
                }
            }
        }
        if changed || refresh {
            (publish)(reducer.state().snapshot(wall_clock_now()));
        }
    }

    // Keep draining events while clients stop so a client already waiting on a
    // full event channel cannot prevent shutdown. Joining here lets tunnel
    // tasks kill and reap their child processes before Tokio is destroyed.
    drop(events_tx);
    while !network_tasks.is_empty() {
        tokio::select! {
            result = network_tasks.join_next() => {
                if let Some(Err(error)) = result {
                    log::error!("network service task failed during shutdown: {error}");
                }
            }
            _ = events_rx.recv() => {}
        }
    }
    log::info!("network services stopped");
    if let Some(dispatcher) = notifications {
        dispatcher.shutdown();
    }
}

/// Converts a relative UI action at the last responsible moment.
fn command_event(command: Command) -> Event {
    match command {
        Command::Snooze { key, duration } => Event::Snooze {
            key,
            until: wall_clock_now().saturating_add(duration),
        },
        Command::Unsnooze { key } => Event::Unsnooze { key },
        Command::ForgetStale { key } => Event::ForgetStale { key },
    }
}

/// Returns the current UTC wall-clock instant.
fn wall_clock_now() -> OffsetDateTime {
    OffsetDateTime::now_utc()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::domain::NotificationData;

    fn at(unix_seconds: i64) -> OffsetDateTime {
        OffsetDateTime::from_unix_timestamp(unix_seconds).unwrap()
    }

    fn incident(key: &str, state: IncidentState, details: &str) -> Incident {
        Incident {
            key: key.into(),
            state,
            details: details.into(),
            incident_started_at: "2026-09-09T00:00:00Z".into(),
            stale: false,
        }
    }

    fn notification(
        server_id: &str,
        total: Vec<Incident>,
        added: Vec<Incident>,
        updated: Vec<Incident>,
        removed: Vec<Incident>,
    ) -> Event {
        Event::Notification {
            server_id: server_id.into(),
            data: NotificationData {
                total,
                added,
                removed,
                updated,
                num_items_ok: 7,
            },
            at: at(1),
        }
    }

    #[test]
    fn initial_snapshot_logs_every_ongoing_incident() {
        let mut logs = EventLogState::default();
        logs.lines(&Event::Connected {
            server_id: "home".into(),
            at: at(1),
        });
        let lines = logs.lines(&notification(
            "home",
            vec![
                incident(
                    "disk",
                    IncidentState::Error,
                    r#"filesystem
full"#,
                ),
                incident("backup", IncidentState::Warning, "late"),
            ],
            Vec::new(),
            Vec::new(),
            Vec::new(),
        ));
        assert_eq!(
            lines,
            [
                info("server home synchronized: 2 ongoing incidents".into()),
                info("server home ongoing incident disk (error): filesystem full".into()),
                info("server home ongoing incident backup (warning): late".into()),
            ]
        );
    }

    #[test]
    fn subsequent_updates_log_every_delta_without_reprinting_total() {
        let mut logs = EventLogState::default();
        logs.lines(&notification(
            "home",
            vec![incident("existing", IncidentState::Warning, "unchanged")],
            Vec::new(),
            Vec::new(),
            Vec::new(),
        ));
        let lines = logs.lines(&notification(
            "home",
            vec![
                incident("existing", IncidentState::Warning, "unchanged"),
                incident("new", IncidentState::Error, "broken"),
            ],
            vec![incident("new", IncidentState::Error, "broken")],
            vec![incident("existing", IncidentState::Error, "worse")],
            vec![incident("fixed", IncidentState::Warning, "old details")],
        ));
        assert_eq!(
            lines,
            [
                info("server home incident update: 1 added, 1 updated, 1 resolved; 2 ongoing incidents".into()),
                info("server home added incident new (error): broken".into()),
                info("server home updated incident existing (error): worse".into()),
                info("server home resolved incident fixed".into()),
            ]
        );
        assert!(lines.iter().all(|line| !line.message.contains("unchanged")));
    }

    #[test]
    fn reconnect_logs_the_next_complete_snapshot_again() {
        let mut logs = EventLogState::default();
        let snapshot = || {
            notification(
                "home",
                vec![incident("disk", IncidentState::Error, "full")],
                Vec::new(),
                Vec::new(),
                Vec::new(),
            )
        };
        assert_eq!(logs.lines(&snapshot()).len(), 2);
        assert_eq!(logs.lines(&snapshot()).len(), 1);
        logs.lines(&Event::Connected {
            server_id: "home".into(),
            at: at(2),
        });
        let reconnected = logs.lines(&snapshot());
        assert_eq!(reconnected.len(), 2);
        assert!(reconnected[1].message.contains("ongoing incident disk"));
    }

    #[test]
    fn heartbeat_is_debug_and_long_details_are_safely_bounded() {
        let mut logs = EventLogState::default();
        assert_eq!(
            logs.lines(&Event::Heartbeat {
                server_id: "home".into(),
                at: at(1),
            }),
            [EventLogLine {
                level: log::Level::Debug,
                message: "server home received heartbeat".into(),
            }]
        );

        let details = "x".repeat(MAX_LOG_DETAILS_CHARS + 1);
        let line = incident_line(
            "home",
            "ongoing",
            &incident("huge", IncidentState::Error, &details),
        );
        assert!(line.ends_with('…'));
        assert_eq!(line.matches('x').count(), MAX_LOG_DETAILS_CHARS);
    }

    #[test]
    fn snooze_feedback_reflects_persistence_outcome() {
        let mut event_logs = EventLogState::default();
        assert!(
            event_logs
                .lines(&Event::Snooze {
                    key: "home.disk".into(),
                    until: at(100),
                })
                .is_empty(),
            "a request must not be logged as successful before persistence"
        );

        let set = SnoozeAction::Set {
            key: "home.disk".into(),
            until: at(100),
        };
        assert_eq!(
            snooze_success_lines(&set),
            [info("snoozed incident home.disk until 100".into())]
        );
        let (title, body) = snooze_failure_notification(&set, "disk full").unwrap();
        assert_eq!(title, "Failed to snooze incident home.disk");
        assert!(body.contains("not changed"));
        assert!(body.contains("disk full"));

        let remove = SnoozeAction::Remove {
            key: "home.disk".into(),
        };
        assert_eq!(
            snooze_success_lines(&remove),
            [info("unsnoozed incident home.disk".into())]
        );
        assert_eq!(
            snooze_failure_notification(&remove, "read-only").unwrap().0,
            "Failed to unsnooze incident home.disk"
        );

        let expire = SnoozeAction::Expire {
            keys: vec!["home.disk".into(), "home.backup".into()],
        };
        assert_eq!(
            snooze_success_lines(&expire),
            [
                info("snooze expired for incident home.disk".into()),
                info("snooze expired for incident home.backup".into()),
            ]
        );
        assert!(snooze_failure_notification(&expire, "read-only").is_none());
    }

    #[test]
    fn automatic_expiration_obeys_retry_cooldown_but_user_actions_do_not() {
        let now = Instant::now();
        let retry_at = now + EXPIRY_PERSISTENCE_RETRY_DELAY;
        let expire = SnoozeAction::Expire {
            keys: vec!["home.disk".into()],
        };
        let set = SnoozeAction::Set {
            key: "home.disk".into(),
            until: at(100),
        };
        let remove = SnoozeAction::Remove {
            key: "home.disk".into(),
        };

        assert!(snooze_persistence_is_due(&expire, None, now));
        assert!(!snooze_persistence_is_due(&expire, Some(retry_at), now));
        assert!(snooze_persistence_is_due(&expire, Some(retry_at), retry_at));
        assert!(snooze_persistence_is_due(&set, Some(retry_at), now));
        assert!(snooze_persistence_is_due(&remove, Some(retry_at), now));
    }

    #[test]
    fn automatic_expiration_reminds_only_for_still_ongoing_incidents() {
        let mut reducer = Reducer::new(AppState::new(vec!["home".into()], BTreeMap::new()));
        reducer.reduce(notification(
            "home",
            vec![incident("disk", IncidentState::Error, "Only 2% free")],
            Vec::new(),
            Vec::new(),
            Vec::new(),
        ));

        let expire = SnoozeAction::Expire {
            keys: vec!["home.disk".into(), "home.resolved".into()],
        };
        assert_eq!(
            snooze_expiry_notifications(&expire, reducer.state(), at(100)),
            [("Snooze ended: home.disk".into(), "Only 2% free".into())]
        );
    }

    #[test]
    fn manual_unsnooze_does_not_create_a_reminder() {
        let mut reducer = Reducer::new(AppState::new(vec!["home".into()], BTreeMap::new()));
        reducer.reduce(notification(
            "home",
            vec![incident("disk", IncidentState::Warning, "Getting full")],
            Vec::new(),
            Vec::new(),
            Vec::new(),
        ));

        let remove = SnoozeAction::Remove {
            key: "home.disk".into(),
        };
        assert!(snooze_expiry_notifications(&remove, reducer.state(), at(100)).is_empty());
    }
}
