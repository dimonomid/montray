use std::rc::Rc;

use chrono::{Datelike, Local, NaiveDateTime, TimeZone, Timelike, Utc};
use slint::{Model, ModelRc, VecModel};
use time::OffsetDateTime;

use super::{IncidentView, MainWindow, MontrayTray, ServerView};
use crate::domain::{Incident, IncidentState, OverallState as DomainOverallState, UiSnapshot};
use crate::tray::{FlashCycle, OverallState, TrayFlashController, TrayIcons, TrayState};

/// Projects one immutable domain snapshot into stable Slint models and tray state.
///
/// Returns a flash token only when the rendered icon genuinely changed and the
/// new state flashes. The caller owns timer scheduling because it has the weak
/// component handles required to update native UI safely.
pub fn apply_snapshot(
    window: &MainWindow,
    tray: &MontrayTray,
    icons: &TrayIcons,
    flash: &TrayFlashController,
    snapshot: UiSnapshot,
) -> Option<FlashCycle> {
    let now_millis = Utc::now().timestamp_millis();
    let online = snapshot
        .servers
        .iter()
        .filter(|server| server.connected)
        .count();
    window.set_server_summary(
        format!("{online}/{} servers are online", snapshot.servers.len()).into(),
    );
    window.set_online_server_count(online as i32);
    let servers: Vec<_> = snapshot
        .servers
        .iter()
        .map(|server| {
            let heartbeat_status = heartbeat_status(
                server.last_heartbeat_at.and_then(offset_datetime_millis),
                now_millis,
            );
            ServerView {
                id: server.id.clone().into(),
                connection: if server.connected {
                    "online"
                } else if server.initialized {
                    "offline"
                } else {
                    "connecting"
                }
                .into(),
                last_heartbeat: format_domain_timestamp(server.last_heartbeat_at, now_millis)
                    .into(),
                connected: server.connected,
                heartbeat_warning: heartbeat_status == HeartbeatStatus::Warning,
                heartbeat_overdue: heartbeat_status == HeartbeatStatus::Overdue,
            }
        })
        .collect();
    let current_servers = window.get_servers();
    if !update_rows_in_place(&current_servers, &servers, |old, new| old.id == new.id) {
        window.set_servers(ModelRc::from(Rc::new(VecModel::from(servers))));
    }
    let active: Vec<_> = snapshot
        .active
        .iter()
        .map(|incident| incident_view(incident, None, now_millis))
        .collect();
    let current_active = window.get_active_incidents();
    if !update_rows_in_place(&current_active, &active, |old, new| old.key == new.key) {
        window.set_active_incidents(ModelRc::from(Rc::new(VecModel::from(active))));
    }
    let snoozed: Vec<_> = snapshot
        .snoozed
        .iter()
        .map(|item| incident_view(&item.incident, Some(item.until), now_millis))
        .collect();
    let current_snoozed = window.get_snoozed_incidents();
    if !update_rows_in_place(&current_snoozed, &snoozed, |old, new| old.key == new.key) {
        window.set_snoozed_incidents(ModelRc::from(Rc::new(VecModel::from(snoozed))));
    }

    let tray_state = TrayState {
        alerting: overall(snapshot.alerting_state),
        unknown_server_count: snapshot.unknown_server_count,
        server_count: snapshot.servers.len(),
        alerting_count: snapshot.active.len(),
        snoozed: snapshot.snoozed_state.map(overall),
        snoozed_count: snapshot.snoozed.len(),
    };
    let flash_cycle = if let Ok(icon) = icons.icon(tray_state) {
        window.set_current_tray_icon(icon.clone());
        flash.apply(tray_state).and_then(|cycle| {
            tray.set_tray_icon(icon);
            flash.is_flashing(cycle).then_some(cycle)
        })
    } else {
        None
    };
    tray.set_status_title(tray_state.status_title().into());
    flash_cycle
}

/// Updates rows without replacing a model when length and stable identity match.
///
/// This is a behavioral requirement, not only an optimization: replacing models
/// every one-second timestamp refresh closes snooze popups and loses text
/// selection. A structural change still replaces the model in one operation.
fn update_rows_in_place<T>(
    model: &ModelRc<T>,
    rows: &[T],
    same_identity: impl Fn(&T, &T) -> bool,
) -> bool
where
    T: Clone + PartialEq + 'static,
{
    if model.row_count() != rows.len() {
        return false;
    }
    let Some(current): Option<Vec<_>> = (0..rows.len()).map(|row| model.row_data(row)).collect()
    else {
        return false;
    };
    if current
        .iter()
        .zip(rows)
        .any(|(old, new)| !same_identity(old, new))
    {
        return false;
    }
    for (row, (old, new)) in current.into_iter().zip(rows).enumerate() {
        if old != *new {
            model.set_row_data(row, new.clone());
        }
    }
    true
}

/// Converts a domain incident to the integer severity contract used by Slint.
fn incident_view(
    incident: &Incident,
    snoozed_until: Option<OffsetDateTime>,
    now_millis: i64,
) -> IncidentView {
    let state = match incident.state {
        IncidentState::Ok => "ok",
        IncidentState::Warning => "warning",
        IncidentState::Error => "error",
    };
    let suffix = if incident.stale {
        " (STALE)"
    } else if snoozed_until.is_some() {
        " (SNOOZED)"
    } else {
        ""
    };
    IncidentView {
        key: incident.key.clone().into(),
        state: format!("{state}{suffix}").into(),
        details: incident.details.clone().into(),
        started_at: display_wire_time(&incident.incident_started_at, now_millis).into(),
        snoozed_until: snoozed_until
            .map(|until| format_domain_timestamp(Some(until), now_millis))
            .unwrap_or_default()
            .into(),
        severity: if incident.state != IncidentState::Ok && incident.key.starts_with("internal.") {
            4
        } else {
            match incident.state {
                IncidentState::Ok => 1,
                IncidentState::Warning => 2,
                IncidentState::Error => 3,
            }
        },
        stale: incident.stale,
        snoozed: snoozed_until.is_some(),
    }
}

fn format_domain_timestamp(value: Option<OffsetDateTime>, now_millis: i64) -> String {
    let Some(value_millis) = value.and_then(offset_datetime_millis) else {
        return "never".into();
    };
    format_timestamp_millis(value_millis, now_millis)
}

fn offset_datetime_millis(timestamp: OffsetDateTime) -> Option<i64> {
    i64::try_from(timestamp.unix_timestamp_nanos().div_euclid(1_000_000)).ok()
}

/// Accepts both RFC 3339 server timestamps and decimal Unix seconds from internal incidents.
///
/// Invalid values are shown verbatim so protocol/debug information is not hidden
/// behind a generic placeholder.
fn display_wire_time(value: &str, now_millis: i64) -> String {
    if value.is_empty() {
        return "never".into();
    }
    if let Ok(timestamp) =
        time::OffsetDateTime::parse(value, &time::format_description::well_known::Rfc3339)
    {
        offset_datetime_millis(timestamp)
            .map(|millis| format_timestamp_millis(millis, now_millis))
            .unwrap_or_else(|| value.to_owned())
    } else if let Ok(timestamp) = value.parse::<i64>() {
        format_unix_timestamp(Some(timestamp), now_millis)
    } else {
        value.to_owned()
    }
}

fn format_unix_timestamp(value: Option<i64>, now_millis: i64) -> String {
    let Some(value) = value else {
        return "never".into();
    };
    format_timestamp_millis(value.saturating_mul(1_000), now_millis)
}

/// Heartbeat freshness buckets matching the original web UI thresholds.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum HeartbeatStatus {
    Normal,
    Warning,
    Overdue,
}

/// Classifies absence as neutral until a heartbeat has actually been observed.
fn heartbeat_status(value_millis: Option<i64>, now_millis: i64) -> HeartbeatStatus {
    let Some(value_millis) = value_millis else {
        return HeartbeatStatus::Normal;
    };
    let age = i128::from(now_millis) - i128::from(value_millis);
    if age < 15_000 {
        HeartbeatStatus::Normal
    } else if age < 30_000 {
        HeartbeatStatus::Warning
    } else {
        HeartbeatStatus::Overdue
    }
}

/// Formats valid local time as compact absolute text plus a bucketed relative age.
fn format_timestamp_millis(value_millis: i64, now_millis: i64) -> String {
    let Some(timestamp) = Local.timestamp_millis_opt(value_millis).single() else {
        return value_millis.to_string();
    };
    let Some(current) = Local.timestamp_millis_opt(now_millis).single() else {
        return value_millis.to_string();
    };
    format!(
        "{} ({})",
        format_absolute(timestamp.naive_local(), current.naive_local()),
        format_relative(value_millis, now_millis)
    )
}

/// Omits date and year portions when they are redundant in local time.
fn format_absolute(value: NaiveDateTime, now: NaiveDateTime) -> String {
    let time = format!(
        "{:02}:{:02}:{:02}",
        value.hour(),
        value.minute(),
        value.second()
    );
    if value.date() == now.date() {
        return time;
    }
    let date = format!("{} {}", month_name(value.month()), value.day());
    if value.year() == now.year() {
        format!("{date}, {time}")
    } else {
        format!("{date}, {}, {time}", value.year())
    }
}

fn month_name(month: u32) -> &'static str {
    const MONTHS: [&str; 12] = [
        "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec",
    ];
    MONTHS[(month.saturating_sub(1) as usize).min(MONTHS.len() - 1)]
}

/// Produces past/future wording while avoiding premature future-time buckets.
///
/// Past ages floor partial seconds; future ages ceil them. This mirrors the web
/// client around zero instead of claiming a future event already occurred.
fn format_relative(value_millis: i64, now_millis: i64) -> String {
    let elapsed_millis = i128::from(now_millis) - i128::from(value_millis);
    let future = elapsed_millis < 0;
    let magnitude = elapsed_millis.unsigned_abs();
    let seconds = if future {
        magnitude.div_ceil(1_000)
    } else {
        magnitude / 1_000
    } as i64;
    let duration = duration(seconds);
    if future {
        format!("in {duration}")
    } else {
        format!("{duration} ago")
    }
}

/// Buckets short ages at 15-second boundaries, then uses minute/hour/day precision.
fn duration(seconds: i64) -> String {
    if seconds < 15 {
        "<15s".into()
    } else if seconds < 60 {
        format!("{}s", seconds / 15 * 15)
    } else if seconds < 3600 {
        format!("{}m", seconds / 60)
    } else if seconds < 86400 {
        let total_minutes = seconds / 60;
        let hours = total_minutes / 60;
        let minutes = total_minutes % 60;
        if minutes == 0 {
            format!("{hours}h")
        } else {
            format!("{hours}h {minutes}m")
        }
    } else {
        let total_hours = seconds / 3600;
        let days = total_hours / 24;
        let hours = total_hours % 24;
        if hours == 0 {
            format!("{days}d")
        } else {
            format!("{days}d {hours}h")
        }
    }
}

fn overall(state: DomainOverallState) -> OverallState {
    match state {
        DomainOverallState::Unknown => OverallState::Unknown,
        DomainOverallState::Ok => OverallState::Ok,
        DomainOverallState::InternalError => OverallState::InternalError,
        DomainOverallState::Warning => OverallState::Warning,
        DomainOverallState::Error => OverallState::Error,
    }
}

#[cfg(test)]
mod tests {
    use std::cell::{Cell, RefCell};

    use chrono::NaiveDate;

    use super::*;

    fn date(year: i32, month: u32, day: u32, hour: u32, minute: u32, second: u32) -> NaiveDateTime {
        NaiveDate::from_ymd_opt(year, month, day)
            .unwrap()
            .and_hms_opt(hour, minute, second)
            .unwrap()
    }

    #[test]
    fn absolute_time_is_compact_and_adds_only_the_needed_date_parts() {
        let now = date(2026, 8, 26, 16, 16, 43);
        assert_eq!(format_absolute(date(2026, 8, 26, 1, 2, 3), now), "01:02:03");
        assert_eq!(
            format_absolute(date(2026, 8, 25, 1, 2, 3), now),
            "Aug 25, 01:02:03"
        );
        assert_eq!(
            format_absolute(date(2025, 8, 25, 1, 2, 3), now),
            "Aug 25, 2025, 01:02:03"
        );
    }

    #[test]
    fn relative_time_uses_the_same_buckets_as_the_web_client() {
        const NOW: i64 = 2_000_000_000;
        let cases = [
            (0, "<15s ago"),
            (14_999, "<15s ago"),
            (15_000, "15s ago"),
            (29_999, "15s ago"),
            (30_000, "30s ago"),
            (44_999, "30s ago"),
            (45_000, "45s ago"),
            (59_999, "45s ago"),
            (60_000, "1m ago"),
            (73 * 60_000, "1h 13m ago"),
            ((23 * 60 + 59) * 60_000, "23h 59m ago"),
            ((24 * 60 + 59) * 60_000, "1d ago"),
            (25 * 60 * 60_000, "1d 1h ago"),
        ];
        for (age, expected) in cases {
            assert_eq!(format_relative(NOW - age, NOW), expected);
        }
        assert_eq!(format_relative(NOW + 73 * 60_000, NOW), "in 1h 13m");
        assert_eq!(format_relative(NOW + 14_001, NOW), "in 15s");
    }

    #[test]
    fn heartbeat_highlighting_uses_the_same_boundaries_as_the_web_client() {
        const NOW: i64 = 2_000_000_000;
        for value in [None, Some(NOW + 1), Some(NOW - 14_999)] {
            assert_eq!(heartbeat_status(value, NOW), HeartbeatStatus::Normal);
        }
        for value in [NOW - 15_000, NOW - 29_999] {
            assert_eq!(heartbeat_status(Some(value), NOW), HeartbeatStatus::Warning);
        }
        assert_eq!(
            heartbeat_status(Some(NOW - 30_000), NOW),
            HeartbeatStatus::Overdue
        );
        assert_eq!(
            heartbeat_status(Some(NOW - 3 * 60 * 60 * 1_000), NOW),
            HeartbeatStatus::Overdue
        );
    }

    #[test]
    fn domain_timestamp_conversion_preserves_milliseconds() {
        let timestamp = OffsetDateTime::from_unix_timestamp(1_800_000_000)
            .unwrap()
            .replace_nanosecond(123_456_789)
            .unwrap();

        assert_eq!(offset_datetime_millis(timestamp), Some(1_800_000_000_123));
    }

    #[test]
    fn missing_and_invalid_wire_timestamps_match_web_fallbacks() {
        assert_eq!(format_unix_timestamp(None, 1_000_000), "never");
        assert_eq!(display_wire_time("", 1_000_000), "never");
        assert_eq!(display_wire_time("not-a-time", 1_000_000), "not-a-time");
    }

    #[test]
    fn stable_models_skip_unchanged_rows_and_update_changed_rows_in_place() {
        let backing = Rc::new(CountingModel::new(vec![("disk", "15s"), ("cpu", "30s")]));
        let model = ModelRc::from(backing.clone());

        assert!(update_rows_in_place(
            &model,
            &[("disk", "15s"), ("cpu", "30s")],
            |old, new| old.0 == new.0
        ));
        assert_eq!(backing.writes.get(), 0);

        assert!(update_rows_in_place(
            &model,
            &[("disk", "30s"), ("cpu", "30s")],
            |old, new| old.0 == new.0
        ));
        assert_eq!(backing.writes.get(), 1);
        assert_eq!(model.row_data(0), Some(("disk", "30s")));

        assert!(!update_rows_in_place(
            &model,
            &[("cpu", "30s"), ("disk", "30s")],
            |old, new| old.0 == new.0
        ));
        assert!(!update_rows_in_place(
            &model,
            &[("disk", "30s")],
            |old, new| { old.0 == new.0 }
        ));
        assert_eq!(backing.writes.get(), 1);
    }

    #[test]
    fn internal_errors_project_to_the_distinct_magenta_severity() {
        let view = incident_view(
            &Incident {
                key: "internal.connection.local".into(),
                state: IncidentState::Error,
                details: "connection refused".into(),
                incident_started_at: "0".into(),
                stale: false,
            },
            None,
            1_000_000,
        );
        assert_eq!(view.severity, 4);

        let ordinary = incident_view(
            &Incident {
                key: "disk".into(),
                state: IncidentState::Error,
                details: "full".into(),
                incident_started_at: "0".into(),
                stale: false,
            },
            None,
            1_000_000,
        );
        assert_eq!(ordinary.severity, 3);
    }

    struct CountingModel<T> {
        rows: RefCell<Vec<T>>,
        writes: Cell<usize>,
    }

    impl<T> CountingModel<T> {
        fn new(rows: Vec<T>) -> Self {
            Self {
                rows: RefCell::new(rows),
                writes: Cell::new(0),
            }
        }
    }

    impl<T: Clone + 'static> Model for CountingModel<T> {
        type Data = T;

        fn row_count(&self) -> usize {
            self.rows.borrow().len()
        }

        fn row_data(&self, row: usize) -> Option<Self::Data> {
            self.rows.borrow().get(row).cloned()
        }

        fn set_row_data(&self, row: usize, data: Self::Data) {
            self.rows.borrow_mut()[row] = data;
            self.writes.set(self.writes.get() + 1);
        }

        fn model_tracker(&self) -> &dyn slint::ModelTracker {
            &()
        }
    }
}
