use std::collections::BTreeMap;

use time::OffsetDateTime;

/// Reason for a proposed snooze-map write.
///
/// Runtime uses this metadata for success logging and to distinguish explicit
/// user failures from automatic expiration cleanup.
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum SnoozeAction {
    /// Add or replace one deadline.
    Set { key: String, until: OffsetDateTime },
    /// Remove one deadline at the user's request.
    Remove { key: String },
    /// Remove deadlines that have passed; keys are retained for useful logs.
    Expire { keys: Vec<String> },
}

/// Side effects requested by a pure domain transition and executed by runtime code.
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum Effect {
    /// Delivers a desktop notification after the corresponding state mutation.
    Notify {
        /// Short summary suitable for the notification heading.
        title: String,
        /// Optional incident details; empty for resolution notifications.
        body: String,
    },
    /// Writes a proposed complete map before committing it to live state.
    PersistSnoozes {
        /// Exact replacement map to persist and, on success, commit.
        snoozes: BTreeMap<String, OffsetDateTime>,
        /// Operation metadata not encoded by the replacement map itself.
        action: SnoozeAction,
    },
}

/// Outcome of reducing one event.
#[derive(Clone, Debug, Default, Eq, PartialEq)]
pub struct Transition {
    /// Whether consumers need a fresh [`super::UiSnapshot`].
    pub changed: bool,
    /// Ordered effects to perform according to each variant's commit contract.
    pub effects: Vec<Effect>,
}
