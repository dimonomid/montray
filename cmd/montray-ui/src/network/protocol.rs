use anyhow::{Context, Result, bail};
use serde::Deserialize;

use crate::domain::{Incident, NotificationData};

/// Minimal wire envelope; unknown event payloads remain unparsed by design.
#[derive(Debug, Deserialize)]
struct Envelope {
    event: String,
    /// Defaults only to let unknown events omit data; known events reject null below.
    #[serde(default)]
    data: serde_json::Value,
}

/// Payload shared by snapshot and incremental notification events.
#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct Notification {
    /// Validated when present but not used for ordering; local receipt time drives UI state.
    #[serde(default)]
    time: Option<String>,
    ongoing_incidents: OngoingIncidents,
}

/// Go-compatible incident collection, including nullable slices encoded from nil.
#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct OngoingIncidents {
    #[serde(default, deserialize_with = "null_as_default")]
    total: Vec<Incident>,
    #[serde(default, deserialize_with = "null_as_default")]
    added: Vec<Incident>,
    #[serde(default, deserialize_with = "null_as_default")]
    removed: Vec<Incident>,
    #[serde(default, deserialize_with = "null_as_default")]
    updated: Vec<Incident>,
    #[serde(default)]
    #[serde(rename = "numItemsOK")]
    num_items_ok: i64,
}

/// Treats explicit JSON `null` like an omitted collection.
///
/// This preserves compatibility with Go's nil slices while keeping malformed
/// non-null values strict.
fn null_as_default<'de, D, T>(deserializer: D) -> std::result::Result<T, D::Error>
where
    D: serde::Deserializer<'de>,
    T: Deserialize<'de> + Default,
{
    Ok(Option::<T>::deserialize(deserializer)?.unwrap_or_default())
}

/// Decodes a supported server event or returns `None` for forward-compatible unknown events.
///
/// Known events are validated eagerly so malformed state disconnects the client
/// instead of partially corrupting the reducer's authoritative snapshot.
pub fn decode_text_message(text: &str) -> Result<Option<NotificationData>> {
    let envelope: Envelope = serde_json::from_str(text).context("decoding server message")?;
    if !matches!(
        envelope.event.as_str(),
        "OngoingIncidentsSnapshot" | "OngoingIncidentsUpdate"
    ) {
        return Ok(None);
    }
    if envelope.data.is_null() {
        bail!("decoding {} data: notification is null", envelope.event);
    }
    let notification: Notification = serde_json::from_value(envelope.data)
        .with_context(|| format!("decoding {} data", envelope.event))?;
    if let Some(timestamp) = &notification.time {
        validate_timestamp(timestamp).context("notification time is invalid")?;
    }
    let ongoing = notification.ongoing_incidents;
    if ongoing.num_items_ok < 0 {
        bail!("ongoingIncidents.numItemsOK is negative");
    }
    for (name, items) in [
        ("total", &ongoing.total),
        ("added", &ongoing.added),
        ("removed", &ongoing.removed),
        ("updated", &ongoing.updated),
    ] {
        for (index, item) in items.iter().enumerate() {
            if item.key.is_empty() {
                bail!("ongoingIncidents.{name}[{index}].key is empty");
            }
            validate_timestamp(&item.incident_started_at).with_context(|| {
                format!("ongoingIncidents.{name}[{index}].incidentStartedAt is invalid")
            })?;
        }
    }
    Ok(Some(NotificationData {
        total: ongoing.total,
        added: ongoing.added,
        removed: ongoing.removed,
        updated: ongoing.updated,
        num_items_ok: ongoing.num_items_ok as usize,
    }))
}

fn validate_timestamp(timestamp: &str) -> Result<()> {
    time::OffsetDateTime::parse(timestamp, &time::format_description::well_known::Rfc3339)?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn decodes_snapshot_and_all_delta_lists() {
        let data = decode_text_message(r#"{"event":"OngoingIncidentsSnapshot","data":{"time":"2026-01-01T00:00:00Z","ongoingIncidents":{"total":[{"key":"disk","state":"error","details":"full","incidentStartedAt":"2026-01-01T00:00:00Z"}],"added":[],"removed":[],"updated":[],"numItemsOK":2}}}"#).unwrap().unwrap();
        assert_eq!(data.total[0].key, "disk");
        assert_eq!(data.num_items_ok, 2);
    }

    #[test]
    fn accepts_go_nil_slices_as_empty_lists() {
        let data = decode_text_message(
            r#"{"event":"OngoingIncidentsSnapshot","data":{"time":"2026-01-01T00:00:00Z","ongoingIncidents":{"total":null,"added":null,"removed":null,"updated":null,"numItemsOK":0}}}"#,
        )
        .unwrap()
        .unwrap();

        assert!(data.total.is_empty());
        assert!(data.added.is_empty());
        assert!(data.removed.is_empty());
        assert!(data.updated.is_empty());
    }

    #[test]
    fn ignores_unknown_events() {
        assert!(
            decode_text_message(r#"{"event":"FutureEvent","data":null}"#)
                .unwrap()
                .is_none()
        );
    }

    #[test]
    fn rejects_null_malformed_and_invalid_known_events() {
        for message in [
            r#"{"event":"OngoingIncidentsUpdate","data":null}"#,
            r#"{"event":"OngoingIncidentsUpdate","data":{}}"#,
            r#"{"event":"OngoingIncidentsUpdate","data":{"ongoingIncidents":{"numItemsOK":-1}}}"#,
            r#"{"event":"OngoingIncidentsUpdate","data":{"ongoingIncidents":{"total":[{"key":"","state":"error","details":"","incidentStartedAt":"x"}]}}}"#,
            r#"{"event":"OngoingIncidentsUpdate","data":{"ongoingIncidents":{"total":[{"key":"x","state":"broken","details":"","incidentStartedAt":"x"}]}}}"#,
        ] {
            assert!(decode_text_message(message).is_err(), "accepted {message}");
        }
    }
}
