use std::collections::BTreeMap;

use time::OffsetDateTime;

use super::*;
use crate::domain::{NotificationData, OverallState};

fn at(unix_seconds: i64) -> OffsetDateTime {
    OffsetDateTime::from_unix_timestamp(unix_seconds).unwrap()
}

fn reducer(ids: &[&str]) -> Reducer {
    Reducer::new(AppState::new(
        ids.iter().map(|id| (*id).to_owned()).collect(),
        BTreeMap::new(),
    ))
}

fn incident(key: &str, state: IncidentState) -> Incident {
    Incident {
        key: key.into(),
        state,
        details: format!("details for {key}"),
        incident_started_at: "2026-09-07T10:00:00Z".into(),
        stale: false,
    }
}

fn notification(total: Vec<Incident>) -> NotificationData {
    NotificationData {
        total,
        added: Vec::new(),
        removed: Vec::new(),
        updated: Vec::new(),
        num_items_ok: 0,
    }
}

fn notify(r: &mut Reducer, server: &str, data: NotificationData, at: i64) -> Transition {
    r.reduce(Event::Notification {
        server_id: server.into(),
        data,
        at: self::at(at),
    })
}

fn commit_proposed_snoozes(r: &mut Reducer, transition: Transition) -> SnoozeAction {
    assert!(!transition.changed);
    let [Effect::PersistSnoozes { snoozes, action }] = transition.effects.as_slice() else {
        panic!("expected exactly one snooze persistence effect");
    };
    assert!(
        r.reduce(Event::SnoozesPersisted {
            snoozes: snoozes.clone(),
        })
        .changed
    );
    action.clone()
}

#[test]
fn starts_with_configured_servers_in_unknown_state() {
    let r = reducer(&["second", "first"]);
    let snapshot = r.state().snapshot(at(0));
    assert_eq!(
        snapshot
            .servers
            .iter()
            .map(|s| s.id.as_str())
            .collect::<Vec<_>>(),
        ["second", "first"]
    );
    assert_eq!(snapshot.unknown_server_count, 2);
    assert_eq!(snapshot.alerting_state, OverallState::Unknown);
}

#[test]
fn snapshot_prefixes_keys_and_replaces_only_its_server() {
    let mut r = reducer(&["first", "second"]);
    notify(
        &mut r,
        "first",
        notification(vec![incident("disk", IncidentState::Error)]),
        1,
    );
    notify(
        &mut r,
        "second",
        notification(vec![incident("cpu", IncidentState::Warning)]),
        2,
    );
    notify(
        &mut r,
        "first",
        notification(vec![incident("memory", IncidentState::Warning)]),
        3,
    );
    let keys = r
        .state()
        .snapshot(at(3))
        .active
        .into_iter()
        .map(|i| i.key)
        .collect::<Vec<_>>();
    assert_eq!(keys, ["first.memory", "second.cpu"]);
}

#[test]
fn aggregation_order_is_configuration_order_not_arrival_order() {
    let mut r = reducer(&["zebra", "alpha"]);
    notify(
        &mut r,
        "alpha",
        notification(vec![incident("a", IncidentState::Error)]),
        1,
    );
    notify(
        &mut r,
        "zebra",
        notification(vec![incident("z", IncidentState::Error)]),
        2,
    );
    let keys = r
        .state()
        .snapshot(at(2))
        .active
        .into_iter()
        .map(|i| i.key)
        .collect::<Vec<_>>();
    assert_eq!(keys, ["zebra.z", "alpha.a"]);
}

#[test]
fn deltas_drive_notifications_but_total_drives_state() {
    let mut r = reducer(&["local"]);
    let mut data = notification(vec![incident("current", IncidentState::Warning)]);
    data.added.push(incident("added", IncidentState::Error));
    data.removed
        .push(incident("removed", IncidentState::Warning));
    data.updated
        .push(incident("ignored-update", IncidentState::Error));
    let transition = notify(&mut r, "local", data, 10);
    assert_eq!(r.state().snapshot(at(10)).active[0].key, "local.current");
    assert_eq!(transition.effects.len(), 2);
    assert!(
        matches!(&transition.effects[0], Effect::Notify { title, .. } if title == "error: local.added")
    );
    assert!(
        matches!(&transition.effects[1], Effect::Notify { title, .. } if title == "OK: local.removed")
    );
}

#[test]
fn snoozed_incident_appearance_resolution_and_reappearance_do_not_notify() {
    let mut r = reducer(&["local"]);
    let transition = r.reduce(Event::Snooze {
        key: "local.disk".into(),
        until: at(100),
    });
    commit_proposed_snoozes(&mut r, transition);

    let mut data = notification(vec![incident("disk", IncidentState::Error)]);
    data.added.push(incident("disk", IncidentState::Error));
    assert!(notify(&mut r, "local", data, 10).effects.is_empty());

    let mut data = notification(Vec::new());
    data.removed.push(incident("disk", IncidentState::Error));
    assert!(notify(&mut r, "local", data, 20).effects.is_empty());

    let mut data = notification(vec![incident("disk", IncidentState::Error)]);
    data.added.push(incident("disk", IncidentState::Error));
    assert!(notify(&mut r, "local", data, 30).effects.is_empty());

    let snapshot = r.state().snapshot(at(30));
    assert!(snapshot.active.is_empty());
    assert_eq!(snapshot.snoozed[0].incident.key, "local.disk");
}

#[test]
fn snoozed_connection_incident_lifecycle_does_not_notify() {
    let mut r = reducer(&["local"]);
    let transition = r.reduce(Event::Snooze {
        key: "internal.connection.local".into(),
        until: at(100),
    });
    commit_proposed_snoozes(&mut r, transition);

    assert!(
        r.reduce(Event::Disconnected {
            server_id: "local".into(),
            at: at(10),
            error: "connection refused".into(),
        })
        .effects
        .is_empty()
    );
    assert!(
        r.reduce(Event::Connected {
            server_id: "local".into(),
            at: at(20),
        })
        .effects
        .is_empty()
    );
    assert!(
        r.reduce(Event::Disconnected {
            server_id: "local".into(),
            at: at(30),
            error: "connection refused again".into(),
        })
        .effects
        .is_empty()
    );

    let snapshot = r.state().snapshot(at(30));
    assert!(snapshot.active.is_empty());
    assert_eq!(
        snapshot.snoozed[0].incident.key,
        "internal.connection.local"
    );
}

#[test]
fn snoozed_tunnel_incident_lifecycle_does_not_notify() {
    let mut r = reducer(&["remote"]);
    let transition = r.reduce(Event::Snooze {
        key: "internal.tunnel.remote".into(),
        until: at(100),
    });
    commit_proposed_snoozes(&mut r, transition);

    assert!(
        r.reduce(Event::TunnelFailed {
            server_id: "remote".into(),
            at: at(10),
            error: "ssh exited".into(),
        })
        .effects
        .is_empty()
    );
    assert!(
        r.reduce(Event::TunnelReady {
            server_id: "remote".into(),
            at: at(20),
        })
        .effects
        .is_empty()
    );
    assert!(
        r.reduce(Event::TunnelFailed {
            server_id: "remote".into(),
            at: at(30),
            error: "ssh exited again".into(),
        })
        .effects
        .is_empty()
    );

    let snapshot = r.state().snapshot(at(30));
    assert!(snapshot.active.is_empty());
    assert_eq!(snapshot.snoozed[0].incident.key, "internal.tunnel.remote");
}

#[test]
fn connection_lifecycle_and_heartbeat_are_recorded() {
    let mut r = reducer(&["local"]);
    assert!(
        r.reduce(Event::Connected {
            server_id: "local".into(),
            at: at(10)
        })
        .changed
    );
    r.reduce(Event::Heartbeat {
        server_id: "local".into(),
        at: at(12),
    });
    let server = &r.state().snapshot(at(12)).servers[0];
    assert!(server.connected && server.initialized);
    assert_eq!(server.connection_changed_at, Some(at(10)));
    assert_eq!(server.last_heartbeat_at, Some(at(12)));
}

#[test]
fn repeated_connection_state_is_idempotent() {
    let mut r = reducer(&["local"]);
    r.reduce(Event::Connected {
        server_id: "local".into(),
        at: at(10),
    });
    assert!(
        !r.reduce(Event::Connected {
            server_id: "local".into(),
            at: at(20)
        })
        .changed
    );
    assert_eq!(
        r.state().snapshot(at(20)).servers[0].connection_changed_at,
        Some(at(10))
    );
}

#[test]
fn disconnect_marks_only_source_incidents_stale() {
    let mut r = reducer(&["first", "second"]);
    notify(
        &mut r,
        "first",
        notification(vec![incident("disk", IncidentState::Error)]),
        1,
    );
    notify(
        &mut r,
        "second",
        notification(vec![incident("cpu", IncidentState::Error)]),
        1,
    );
    r.reduce(Event::Disconnected {
        server_id: "first".into(),
        at: at(2),
        error: String::new(),
    });
    let snapshot = r.state().snapshot(at(2));
    assert!(
        snapshot
            .active
            .iter()
            .find(|i| i.key == "first.disk")
            .unwrap()
            .stale
    );
    assert!(
        !snapshot
            .active
            .iter()
            .find(|i| i.key == "second.cpu")
            .unwrap()
            .stale
    );
}

#[test]
fn repeated_disconnect_is_idempotent_when_error_is_unchanged() {
    let mut r = reducer(&["local"]);
    notify(
        &mut r,
        "local",
        notification(vec![incident("disk", IncidentState::Error)]),
        1,
    );
    r.reduce(Event::Disconnected {
        server_id: "local".into(),
        at: at(2),
        error: "down".into(),
    });
    assert!(
        !r.reduce(Event::Disconnected {
            server_id: "local".into(),
            at: at(3),
            error: "down".into()
        })
        .changed
    );
}

#[test]
fn reconnect_resolves_internal_error_but_does_not_freshen_data() {
    let mut r = reducer(&["local"]);
    notify(
        &mut r,
        "local",
        notification(vec![incident("disk", IncidentState::Error)]),
        1,
    );
    r.reduce(Event::Disconnected {
        server_id: "local".into(),
        at: at(2),
        error: "refused".into(),
    });
    assert_eq!(
        r.state().snapshot(at(2)).alerting_state,
        OverallState::Error
    );
    r.reduce(Event::Connected {
        server_id: "local".into(),
        at: at(3),
    });
    let snapshot = r.state().snapshot(at(3));
    assert_eq!(snapshot.active.len(), 1);
    assert!(snapshot.active[0].stale);
}

#[test]
fn connection_error_is_an_internal_incident() {
    let mut r = reducer(&["local"]);
    let occurred_at = at(2)
        .replace_nanosecond(123_456_789)
        .expect("valid test nanosecond");
    let transition = r.reduce(Event::Disconnected {
        server_id: "local".into(),
        at: occurred_at,
        error: "connection refused".into(),
    });
    let snapshot = r.state().snapshot(at(3));
    assert_eq!(snapshot.alerting_state, OverallState::InternalError);
    assert_eq!(snapshot.active[0].key, "internal.connection.local");
    assert_eq!(snapshot.active[0].details, "connection refused");
    assert_eq!(
        snapshot.active[0].incident_started_at,
        "1970-01-01T00:00:02.123456789Z"
    );
    assert_eq!(transition.effects.len(), 1);
}

#[test]
fn reconnect_notifies_once_that_connection_incident_resolved() {
    let mut r = reducer(&["local"]);
    r.reduce(Event::Disconnected {
        server_id: "local".into(),
        at: at(2),
        error: "down".into(),
    });
    let transition = r.reduce(Event::Connected {
        server_id: "local".into(),
        at: at(3),
    });
    assert!(
        matches!(&transition.effects[..], [Effect::Notify { title, .. }] if title == "OK: internal.connection.local")
    );
    assert!(
        r.reduce(Event::Connected {
            server_id: "local".into(),
            at: at(4)
        })
        .effects
        .is_empty()
    );
}

#[test]
fn tunnel_failure_is_distinct_and_marks_server_data_stale() {
    let mut r = reducer(&["remote"]);
    r.reduce(Event::Connected {
        server_id: "remote".into(),
        at: at(1),
    });
    notify(
        &mut r,
        "remote",
        notification(vec![incident("disk", IncidentState::Error)]),
        1,
    );
    r.reduce(Event::Disconnected {
        server_id: "remote".into(),
        at: at(2),
        error: "socket closed".into(),
    });

    let transition = r.reduce(Event::TunnelFailed {
        server_id: "remote".into(),
        at: at(2),
        error: "ssh exited".into(),
    });
    let snapshot = r.state().snapshot(at(2));
    assert!(!snapshot.servers[0].connected);
    assert!(
        snapshot
            .active
            .iter()
            .any(|item| { item.key == "internal.tunnel.remote" && item.details == "ssh exited" })
    );
    assert!(
        !snapshot
            .active
            .iter()
            .any(|item| item.key == "internal.connection.remote")
    );
    assert!(
        snapshot
            .active
            .iter()
            .find(|item| item.key == "remote.disk")
            .unwrap()
            .stale
    );
    assert!(matches!(
        &transition.effects[..],
        [Effect::Notify { title, body }]
            if title == "error: internal.tunnel.remote" && body == "ssh exited"
    ));
}

#[test]
fn tunnel_readiness_resolves_only_the_tunnel_incident() {
    let mut r = reducer(&["remote"]);
    r.reduce(Event::TunnelFailed {
        server_id: "remote".into(),
        at: at(1),
        error: "ssh exited".into(),
    });
    let transition = r.reduce(Event::TunnelReady {
        server_id: "remote".into(),
        at: at(2),
    });
    assert!(r.state().snapshot(at(2)).active.is_empty());
    assert!(matches!(
        &transition.effects[..],
        [Effect::Notify { title, body }]
            if title == "OK: internal.tunnel.remote" && body.is_empty()
    ));
    assert!(
        !r.reduce(Event::TunnelReady {
            server_id: "remote".into(),
            at: at(3),
        })
        .changed
    );
}

#[test]
fn fresh_snapshot_replaces_stale_data() {
    let mut r = reducer(&["local"]);
    notify(
        &mut r,
        "local",
        notification(vec![incident("disk", IncidentState::Error)]),
        1,
    );
    r.reduce(Event::Disconnected {
        server_id: "local".into(),
        at: at(2),
        error: String::new(),
    });
    notify(
        &mut r,
        "local",
        notification(vec![incident("disk", IncidentState::Error)]),
        3,
    );
    assert!(!r.state().snapshot(at(3)).active[0].stale);
}

#[test]
fn snooze_classifies_and_unsnooze_restores_incident() {
    let mut r = reducer(&["local"]);
    notify(
        &mut r,
        "local",
        notification(vec![incident("disk", IncidentState::Error)]),
        1,
    );
    let transition = r.reduce(Event::Snooze {
        key: "local.disk".into(),
        until: at(100),
    });
    assert_eq!(r.state().snapshot(at(50)).active.len(), 1);
    assert!(r.state().snapshot(at(50)).snoozed.is_empty());
    assert_eq!(
        commit_proposed_snoozes(&mut r, transition),
        SnoozeAction::Set {
            key: "local.disk".into(),
            until: at(100),
        }
    );
    assert_eq!(r.state().snapshot(at(50)).snoozed.len(), 1);
    assert!(r.state().snapshot(at(50)).active.is_empty());
    let transition = r.reduce(Event::Unsnooze {
        key: "local.disk".into(),
    });
    assert!(r.state().snapshot(at(50)).active.is_empty());
    assert_eq!(
        commit_proposed_snoozes(&mut r, transition),
        SnoozeAction::Remove {
            key: "local.disk".into(),
        }
    );
    assert_eq!(r.state().snapshot(at(50)).active.len(), 1);
}

#[test]
fn snooze_expires_at_exact_boundary_and_requests_persistence() {
    let mut r = reducer(&["local"]);
    let transition = r.reduce(Event::Snooze {
        key: "local.disk".into(),
        until: at(100),
    });
    commit_proposed_snoozes(&mut r, transition);
    assert_eq!(r.reduce(Event::Tick { at: at(99) }), Transition::default());

    let first_attempt = r.reduce(Event::Tick { at: at(100) });
    assert!(!first_attempt.changed);
    assert_eq!(
        first_attempt.effects,
        [Effect::PersistSnoozes {
            snoozes: BTreeMap::new(),
            action: SnoozeAction::Expire {
                keys: vec!["local.disk".into()],
            },
        }]
    );
    assert!(r.state().snoozes().contains_key("local.disk"));

    // A failed write means no commit event, so the next tick proposes the same
    // cleanup again without any special retry state.
    let retry = r.reduce(Event::Tick { at: at(101) });
    assert_eq!(retry.effects, first_attempt.effects);
    commit_proposed_snoozes(&mut r, retry);
    assert!(r.state().snoozes().is_empty());
}

#[test]
fn snooze_survives_updates_and_disconnects() {
    let mut r = reducer(&["local"]);
    let transition = r.reduce(Event::Snooze {
        key: "local.disk".into(),
        until: at(100),
    });
    commit_proposed_snoozes(&mut r, transition);
    notify(
        &mut r,
        "local",
        notification(vec![incident("disk", IncidentState::Warning)]),
        1,
    );
    r.reduce(Event::Disconnected {
        server_id: "local".into(),
        at: at(2),
        error: String::new(),
    });
    let snapshot = r.state().snapshot(at(50));
    assert_eq!(snapshot.snoozed.len(), 1);
    assert!(snapshot.snoozed[0].incident.stale);
}

#[test]
fn forget_removes_only_matching_stale_incident() {
    let mut r = reducer(&["first", "second"]);
    notify(
        &mut r,
        "first",
        notification(vec![incident("disk", IncidentState::Error)]),
        1,
    );
    notify(
        &mut r,
        "second",
        notification(vec![incident("disk", IncidentState::Error)]),
        1,
    );
    r.reduce(Event::Disconnected {
        server_id: "first".into(),
        at: at(2),
        error: String::new(),
    });
    assert!(
        r.reduce(Event::ForgetStale {
            key: "first.disk".into()
        })
        .changed
    );
    let keys = r
        .state()
        .snapshot(at(2))
        .active
        .into_iter()
        .map(|i| i.key)
        .collect::<Vec<_>>();
    assert_eq!(keys, ["second.disk"]);
}

#[test]
fn forget_refuses_fresh_incident_and_forgotten_data_can_reappear() {
    let mut r = reducer(&["local"]);
    notify(
        &mut r,
        "local",
        notification(vec![incident("disk", IncidentState::Error)]),
        1,
    );
    assert!(
        !r.reduce(Event::ForgetStale {
            key: "local.disk".into()
        })
        .changed
    );
    r.reduce(Event::Disconnected {
        server_id: "local".into(),
        at: at(2),
        error: String::new(),
    });
    r.reduce(Event::ForgetStale {
        key: "local.disk".into(),
    });
    notify(
        &mut r,
        "local",
        notification(vec![incident("disk", IncidentState::Error)]),
        3,
    );
    assert_eq!(r.state().snapshot(at(3)).active.len(), 1);
}

#[test]
fn severity_precedence_and_snoozed_state_are_independent() {
    let mut r = reducer(&["local"]);
    notify(
        &mut r,
        "local",
        notification(vec![
            incident("warn", IncidentState::Warning),
            incident("err", IncidentState::Error),
        ]),
        1,
    );
    let transition = r.reduce(Event::Snooze {
        key: "local.err".into(),
        until: at(100),
    });
    commit_proposed_snoozes(&mut r, transition);
    let snapshot = r.state().snapshot(at(50));
    assert_eq!(snapshot.alerting_state, OverallState::Warning);
    assert_eq!(snapshot.snoozed_state, Some(OverallState::Error));
}

#[test]
fn unknown_events_for_unconfigured_servers_do_not_mutate_state() {
    let mut r = reducer(&["local"]);
    assert!(
        !r.reduce(Event::Connected {
            server_id: "other".into(),
            at: at(1)
        })
        .changed
    );
    assert!(
        !r.reduce(Event::Heartbeat {
            server_id: "other".into(),
            at: at(2)
        })
        .changed
    );
    assert!(
        !r.reduce(Event::Disconnected {
            server_id: "other".into(),
            at: at(3),
            error: "x".into()
        })
        .changed
    );
    assert!(
        !r.reduce(Event::TunnelReady {
            server_id: "other".into(),
            at: at(4),
        })
        .changed
    );
    assert!(
        !r.reduce(Event::TunnelFailed {
            server_id: "other".into(),
            at: at(5),
            error: "x".into(),
        })
        .changed
    );
    assert_eq!(r.state().snapshot(at(3)).unknown_server_count, 1);
}
