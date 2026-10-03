use super::{AppState, Effect, Event, Incident, IncidentState, SnoozeAction, Transition};
use time::OffsetDateTime;

/// Serial state machine for network events and user incident actions.
///
/// A reducer instance must be driven by one task: ordering events here avoids
/// locks in the domain model. Snooze requests deliberately produce persistence
/// proposals without mutation; only the serialized acknowledgement commits one.
pub struct Reducer {
    state: AppState,
}

impl Reducer {
    pub fn new(state: AppState) -> Self {
        Self { state }
    }

    pub fn state(&self) -> &AppState {
        &self.state
    }

    /// Applies one event, returning publication and side-effect instructions.
    ///
    /// Events for unknown server IDs are ignored defensively. Notification
    /// `total` lists replace per-server state, while only `added` and `removed`
    /// lists create desktop notifications. Snooze persistence uses a two-phase
    /// proposal/acknowledgement so failed writes cannot alter live behavior.
    pub fn reduce(&mut self, event: Event) -> Transition {
        match event {
            Event::Connected { server_id, at } => self.connected(&server_id, at),
            Event::Disconnected {
                server_id,
                at,
                error,
            } => self.disconnected(&server_id, at, error),
            Event::TunnelReady { server_id, at } => self.tunnel_ready(&server_id, at),
            Event::TunnelFailed {
                server_id,
                at,
                error,
            } => self.tunnel_failed(&server_id, at, error),
            Event::Heartbeat { server_id, at } => self.heartbeat(&server_id, at),
            Event::Notification {
                server_id,
                data,
                at,
            } => {
                if !self.state.servers.contains_key(&server_id) {
                    return Transition::default();
                }
                let mut effects = Vec::new();
                for item in &data.added {
                    let item = prefixed(item, &server_id);
                    if !self.is_snoozed(&item.key, at) {
                        effects.push(Effect::Notify {
                            title: format!("{}: {}", state_name(item.state), item.key),
                            body: item.details,
                        });
                    }
                }
                for item in &data.removed {
                    let item = prefixed(item, &server_id);
                    if !self.is_snoozed(&item.key, at) {
                        effects.push(Effect::Notify {
                            title: format!("OK: {}", item.key),
                            body: String::new(),
                        });
                    }
                }
                self.state.incidents.insert(
                    server_id.clone(),
                    data.total
                        .iter()
                        .map(|item| prefixed(item, &server_id))
                        .collect(),
                );
                Transition {
                    changed: true,
                    effects,
                }
            }
            Event::Snooze { key, until } => {
                if self.state.snoozed.get(&key) == Some(&until) {
                    return Transition::default();
                }
                let mut snoozes = self.state.snoozed.clone();
                snoozes.insert(key.clone(), until);
                Transition {
                    changed: false,
                    effects: vec![Effect::PersistSnoozes {
                        snoozes,
                        action: SnoozeAction::Set { key, until },
                    }],
                }
            }
            Event::Unsnooze { key } => {
                if !self.state.snoozed.contains_key(&key) {
                    return Transition::default();
                }
                let mut snoozes = self.state.snoozed.clone();
                snoozes.remove(&key);
                Transition {
                    changed: false,
                    effects: vec![Effect::PersistSnoozes {
                        snoozes,
                        action: SnoozeAction::Remove { key },
                    }],
                }
            }
            Event::SnoozesPersisted { snoozes } => {
                let changed = self.state.snoozed != snoozes;
                if changed {
                    self.state.snoozed = snoozes;
                }
                Transition {
                    changed,
                    effects: Vec::new(),
                }
            }
            Event::ForgetStale { key } => {
                // This is a local dismissal, not a synthetic recovery. A later
                // authoritative snapshot may therefore restore the incident.
                let mut changed = false;
                for incidents in self.state.incidents.values_mut() {
                    let before = incidents.len();
                    incidents.retain(|item| item.key != key || !item.stale);
                    changed |= incidents.len() != before;
                }
                Transition {
                    changed,
                    effects: Vec::new(),
                }
            }
            Event::Tick { at } => {
                let keys = self
                    .state
                    .snoozed
                    .iter()
                    .filter(|(_, until)| **until <= at)
                    .map(|(key, _)| key.clone())
                    .collect::<Vec<_>>();
                if keys.is_empty() {
                    return Transition::default();
                }
                let mut snoozes = self.state.snoozed.clone();
                snoozes.retain(|_, until| *until > at);
                Transition {
                    changed: false,
                    effects: vec![Effect::PersistSnoozes {
                        snoozes,
                        action: SnoozeAction::Expire { keys },
                    }],
                }
            }
        }
    }

    /// Starts a fresh socket generation and resolves its connection incident.
    /// A previous heartbeat is cleared because it cannot prove the new socket
    /// has delivered any data.
    fn connected(&mut self, id: &str, at: OffsetDateTime) -> Transition {
        let incident_key = format!("internal.connection.{id}");
        let incident_is_snoozed = self.is_snoozed(&incident_key, at);
        let Some(server) = self.state.servers.get_mut(id) else {
            return Transition::default();
        };
        let changed = !server.initialized || !server.connected;
        server.initialized = true;
        server.connected = true;
        server.connection_changed_at = changed.then_some(at).or(server.connection_changed_at);
        if changed {
            // A heartbeat from the previous socket says nothing about this one.
            server.last_heartbeat_at = None;
        }
        let removed = self
            .state
            .internal_incidents
            .remove(&incident_key)
            .is_some();
        let effects = (removed && !incident_is_snoozed)
            .then(|| Effect::Notify {
                title: format!("OK: {incident_key}"),
                body: String::new(),
            })
            .into_iter()
            .collect();
        Transition {
            changed: changed || removed,
            effects,
        }
    }

    /// Marks cached server incidents stale while retaining them for context.
    /// Repeated failures update details but notify only on incident creation.
    fn disconnected(&mut self, id: &str, at: OffsetDateTime, error: String) -> Transition {
        let key = format!("internal.connection.{id}");
        let incident_is_snoozed = self.is_snoozed(&key, at);
        let Some(server) = self.state.servers.get_mut(id) else {
            return Transition::default();
        };
        let status_changed = !server.initialized || server.connected;
        server.initialized = true;
        server.connected = false;
        server.connection_changed_at = status_changed
            .then_some(at)
            .or(server.connection_changed_at);
        let mut stale_changed = false;
        if let Some(incidents) = self.state.incidents.get_mut(id) {
            for incident in incidents {
                if !incident.stale {
                    incident.stale = true;
                    stale_changed = true;
                }
            }
        }
        let mut effects = Vec::new();
        let internal_changed = if error.is_empty() {
            // Tunnel failures deliberately send an empty socket error so the
            // root-cause tunnel incident is the only visible internal failure.
            self.state.internal_incidents.remove(&key).is_some()
        } else {
            match self.state.internal_incidents.get_mut(&key) {
                Some(incident) if incident.details == error => false,
                Some(incident) => {
                    // Update the card but do not emit another notification for
                    // every retry of the same logical connection incident.
                    incident.details = error;
                    true
                }
                None => {
                    if !incident_is_snoozed {
                        effects.push(Effect::Notify {
                            title: format!("error: {key}"),
                            body: error.clone(),
                        });
                    }
                    self.state.internal_incidents.insert(
                        key.clone(),
                        Incident {
                            key,
                            state: IncidentState::Error,
                            details: error,
                            incident_started_at: format_incident_time(at),
                            stale: false,
                        },
                    );
                    true
                }
            }
        };
        Transition {
            changed: status_changed || stale_changed || internal_changed,
            effects,
        }
    }

    /// Resolves the tunnel incident only after SSH emitted its readiness marker.
    fn tunnel_ready(&mut self, id: &str, at: OffsetDateTime) -> Transition {
        if !self.state.servers.contains_key(id) {
            return Transition::default();
        }
        let key = format!("internal.tunnel.{id}");
        let incident_is_snoozed = self.is_snoozed(&key, at);
        let removed = self.state.internal_incidents.remove(&key).is_some();
        Transition {
            changed: removed,
            effects: (removed && !incident_is_snoozed)
                .then(|| Effect::Notify {
                    title: format!("OK: {key}"),
                    body: String::new(),
                })
                .into_iter()
                .collect(),
        }
    }

    /// Makes the tunnel the sole root-cause incident and removes a concurrent,
    /// derivative WebSocket connection failure for the same server.
    fn tunnel_failed(&mut self, id: &str, at: OffsetDateTime, error: String) -> Transition {
        let key = format!("internal.tunnel.{id}");
        let incident_is_snoozed = self.is_snoozed(&key, at);
        let Some(server) = self.state.servers.get_mut(id) else {
            return Transition::default();
        };
        let status_changed = !server.initialized || server.connected;
        server.initialized = true;
        server.connected = false;
        server.connection_changed_at = status_changed
            .then_some(at)
            .or(server.connection_changed_at);

        let mut stale_changed = false;
        if let Some(incidents) = self.state.incidents.get_mut(id) {
            for incident in incidents {
                if !incident.stale {
                    incident.stale = true;
                    stale_changed = true;
                }
            }
        }

        // A tunnel failure is the root cause. Do not leave a redundant socket
        // failure beside it if both sides notice the same break concurrently.
        let connection_removed = self
            .state
            .internal_incidents
            .remove(&format!("internal.connection.{id}"))
            .is_some();
        let mut effects = Vec::new();
        let internal_changed = match self.state.internal_incidents.get_mut(&key) {
            Some(incident) if incident.details == error => false,
            Some(incident) => {
                incident.details = error;
                true
            }
            None => {
                if !incident_is_snoozed {
                    effects.push(Effect::Notify {
                        title: format!("error: {key}"),
                        body: error.clone(),
                    });
                }
                self.state.internal_incidents.insert(
                    key.clone(),
                    Incident {
                        key,
                        state: IncidentState::Error,
                        details: error,
                        incident_started_at: format_incident_time(at),
                        stale: false,
                    },
                );
                true
            }
        };
        Transition {
            changed: status_changed || stale_changed || connection_removed || internal_changed,
            effects,
        }
    }

    fn heartbeat(&mut self, id: &str, at: OffsetDateTime) -> Transition {
        let Some(server) = self.state.servers.get_mut(id) else {
            return Transition::default();
        };
        let changed = server.last_heartbeat_at != Some(at);
        server.last_heartbeat_at = Some(at);
        Transition {
            changed,
            effects: Vec::new(),
        }
    }

    /// Tests the deadline directly; expired entries may remain stored until the
    /// periodic expiry event removes and persists them.
    fn is_snoozed(&self, key: &str, now: OffsetDateTime) -> bool {
        self.state
            .snoozed
            .get(key)
            .is_some_and(|until| *until > now)
    }
}

/// Qualifies a server-local wire key for collision-free aggregation.
fn prefixed(item: &Incident, id: &str) -> Incident {
    let mut item = item.clone();
    item.key = format!("{id}.{}", item.key);
    item
}

/// Encodes locally generated incident times in the same RFC 3339 shape as wire incidents.
fn format_incident_time(at: OffsetDateTime) -> String {
    at.format(&time::format_description::well_known::Rfc3339)
        .expect("a current UTC timestamp is representable as RFC 3339")
}

fn state_name(state: IncidentState) -> &'static str {
    match state {
        IncidentState::Ok => "ok",
        IncidentState::Warning => "warning",
        IncidentState::Error => "error",
    }
}

#[cfg(test)]
#[path = "reducer_tests.rs"]
mod tests;
