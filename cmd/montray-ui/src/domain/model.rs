use std::collections::{BTreeMap, HashMap};

use serde::{Deserialize, Serialize};
use time::OffsetDateTime;

/// Severity reported by Montray Server for one monitored item.
#[derive(Clone, Copy, Debug, Deserialize, Eq, Ord, PartialEq, PartialOrd, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum IncidentState {
    Ok,
    Warning,
    Error,
}

/// Normalized incident shared by wire decoding, the reducer, and UI projection.
#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Incident {
    /// Stable item identity. Server incidents are prefixed locally with the server ID.
    pub key: String,
    pub state: IncidentState,
    #[serde(default)]
    pub details: String,
    /// RFC 3339 on the wire; locally synthesized incidents use Unix seconds as text.
    pub incident_started_at: String,
    /// True when retained from the last snapshot after its server disconnected.
    #[serde(default)]
    pub stale: bool,
}

/// Authoritative incident state plus the transition deltas supplied by Montray Server.
///
/// `total` replaces the cached state. The delta lists drive logging and desktop
/// notifications; they must not be reconstructed by diffing `total` because the
/// server owns transition semantics.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct NotificationData {
    pub total: Vec<Incident>,
    pub added: Vec<Incident>,
    pub removed: Vec<Incident>,
    pub updated: Vec<Incident>,
    /// Informational server count used in synchronization logs, not UI severity.
    pub num_items_ok: usize,
}

/// Connection metadata retained independently from incident state.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ServerStatus {
    pub id: String,
    pub connected: bool,
    /// Distinguishes startup/unknown from a connection known to be offline.
    pub initialized: bool,
    /// Wall-clock instant of the last connected/disconnected transition.
    pub connection_changed_at: Option<OffsetDateTime>,
    /// Wall-clock instant when the client received the latest heartbeat frame.
    pub last_heartbeat_at: Option<OffsetDateTime>,
}

/// Aggregated state used by the window and tray.
///
/// `InternalError` is deliberately distinct (magenta) and ranks below a real
/// warning or error reported by a monitored system.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum OverallState {
    Unknown,
    Ok,
    InternalError,
    Warning,
    Error,
}

/// Incident paired with its exclusive snooze deadline.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SnoozedIncident {
    pub incident: Incident,
    pub until: OffsetDateTime,
}

/// Immutable, display-ready view of domain state published to the UI thread.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct UiSnapshot {
    pub servers: Vec<ServerStatus>,
    pub active: Vec<Incident>,
    pub snoozed: Vec<SnoozedIncident>,
    /// Severity of visible incidents, or `Unknown` while any server is uninitialized.
    pub alerting_state: OverallState,
    /// Highest hidden severity; absent when no current incident is snoozed.
    pub snoozed_state: Option<OverallState>,
    pub unknown_server_count: usize,
}

/// Reducer-owned canonical state.
///
/// Keeping this independent of Slint and Tokio makes event ordering and all
/// alerting decisions deterministic and directly testable.
#[derive(Clone, Debug)]
pub struct AppState {
    /// Configuration order, preserved because `HashMap` iteration is unstable.
    pub(crate) server_order: Vec<String>,
    pub(crate) servers: HashMap<String, ServerStatus>,
    /// Last authoritative snapshot per configured server.
    pub(crate) incidents: HashMap<String, Vec<Incident>>,
    /// Client-generated connection and tunnel failures keyed by `internal.*`.
    pub(crate) internal_incidents: BTreeMap<String, Incident>,
    /// Incident key to exclusive deadline, including currently absent items.
    pub(crate) snoozed: BTreeMap<String, OffsetDateTime>,
}

impl AppState {
    pub fn new(server_ids: Vec<String>, snoozed: BTreeMap<String, OffsetDateTime>) -> Self {
        let servers = server_ids
            .iter()
            .map(|id| {
                (
                    id.clone(),
                    ServerStatus {
                        id: id.clone(),
                        connected: false,
                        initialized: false,
                        connection_changed_at: None,
                        last_heartbeat_at: None,
                    },
                )
            })
            .collect();
        Self {
            server_order: server_ids,
            servers,
            incidents: HashMap::new(),
            internal_incidents: BTreeMap::new(),
            snoozed,
        }
    }

    pub fn snoozes(&self) -> &BTreeMap<String, OffsetDateTime> {
        &self.snoozed
    }

    /// Projects canonical state while classifying snoozes at `now`.
    ///
    /// Expired entries are treated as active here. A tick proposes their
    /// removal, but live state changes only after persistence acknowledges the
    /// replacement map, keeping projection side-effect free.
    pub fn snapshot(&self, now: OffsetDateTime) -> UiSnapshot {
        let mut incidents: Vec<_> = self.internal_incidents.values().cloned().collect();
        for id in &self.server_order {
            if let Some(server_incidents) = self.incidents.get(id) {
                incidents.extend(server_incidents.iter().cloned());
            }
        }

        let mut active = Vec::new();
        let mut snoozed = Vec::new();
        for incident in incidents {
            if let Some(until) = self
                .snoozed
                .get(&incident.key)
                .copied()
                .filter(|until| *until > now)
            {
                snoozed.push(SnoozedIncident { incident, until });
            } else {
                active.push(incident);
            }
        }
        let servers: Vec<_> = self
            .server_order
            .iter()
            .filter_map(|id| self.servers.get(id).cloned())
            .collect();
        let unknown_server_count = servers.iter().filter(|server| !server.initialized).count();
        let mut alerting_state = overall_state(&active);
        if alerting_state == OverallState::Ok && unknown_server_count > 0 {
            alerting_state = OverallState::Unknown;
        }
        let snoozed_state = (!snoozed.is_empty()).then(|| {
            overall_state(
                &snoozed
                    .iter()
                    .map(|item| item.incident.clone())
                    .collect::<Vec<_>>(),
            )
        });
        UiSnapshot {
            servers,
            active,
            snoozed,
            alerting_state,
            snoozed_state,
            unknown_server_count,
        }
    }
}

/// Reduces incident severity using the tray/UI precedence rather than enum
/// declaration order. Internal failures deliberately retain their own visual
/// state unless a real warning or error is present.
fn overall_state(items: &[Incident]) -> OverallState {
    items.iter().fold(OverallState::Ok, |overall, item| {
        let current = if item.key.starts_with("internal.") && item.state != IncidentState::Ok {
            OverallState::InternalError
        } else {
            match item.state {
                IncidentState::Ok => OverallState::Ok,
                IncidentState::Warning => OverallState::Warning,
                IncidentState::Error => OverallState::Error,
            }
        };
        if rank(current) > rank(overall) {
            current
        } else {
            overall
        }
    })
}

fn rank(state: OverallState) -> u8 {
    match state {
        OverallState::Unknown => 0,
        OverallState::Ok => 1,
        OverallState::InternalError => 2,
        OverallState::Warning => 3,
        OverallState::Error => 4,
    }
}
