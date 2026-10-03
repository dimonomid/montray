use std::collections::BTreeMap;

use time::OffsetDateTime;

use super::NotificationData;

/// Every input accepted by the domain reducer.
///
/// Network producers and UI commands share this stream, so the reducer is the
/// single authority for state transitions and their side effects. Producers
/// supply UTC wall-clock instants without discarding sub-second precision.
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum Event {
    Connected {
        server_id: String,
        at: OffsetDateTime,
    },
    Disconnected {
        server_id: String,
        at: OffsetDateTime,
        /// Empty when the owning tunnel process already reported the root cause.
        error: String,
    },
    TunnelReady {
        server_id: String,
        at: OffsetDateTime,
    },
    TunnelFailed {
        server_id: String,
        at: OffsetDateTime,
        error: String,
    },
    Heartbeat {
        server_id: String,
        at: OffsetDateTime,
    },
    Notification {
        server_id: String,
        data: NotificationData,
        /// Local receipt time used to evaluate snooze deadlines.
        at: OffsetDateTime,
    },
    Snooze {
        key: String,
        until: OffsetDateTime,
    },
    Unsnooze {
        key: String,
    },
    /// Internal acknowledgement applied only after the exact map was written.
    SnoozesPersisted {
        snoozes: BTreeMap<String, OffsetDateTime>,
    },
    ForgetStale {
        /// Fully qualified key; forgetting never implies remote recovery.
        key: String,
    },
    Tick {
        at: OffsetDateTime,
    },
}
