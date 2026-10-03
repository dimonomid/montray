use std::collections::BTreeMap;
use std::fs;
use std::io::{BufWriter, Write};
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};

use anyhow::{Context, Result};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use time::{OffsetDateTime, UtcOffset};

const STATE_FILENAME: &str = ".montray-state.json";
const FORMER_STATE_FILENAME: &str = ".salmon-watch-state.json";
const LEGACY_STATE_FILENAME: &str = ".salmon-watch-legacy-state.json";

/// Cloneable, process-local serialized access to the state file.
///
/// Updates always reload under the mutex so independent UI, geometry, and
/// runtime writers merge through the latest on-disk representation instead of
/// overwriting one another with stale snapshots. The mutex is not an
/// inter-process lock: running two watcher instances against the same state
/// file can still race, although each individual replacement remains atomic.
#[derive(Clone)]
pub struct Store {
    path: Arc<PathBuf>,
    /// Shared by all clones; poisoned locks are recovered because disk remains authoritative.
    lock: Arc<Mutex<()>>,
}

impl Store {
    pub fn new(path: PathBuf) -> Self {
        Self {
            path: Arc::new(path),
            lock: Arc::new(Mutex::new(())),
        }
    }

    pub fn load(&self) -> Result<StateFile> {
        let _guard = self
            .lock
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        load(&self.path)
    }

    /// Atomically applies a read-modify-write transaction within this process.
    pub fn update(&self, change: impl FnOnce(&mut StateFile) -> Result<()>) -> Result<()> {
        let _guard = self
            .lock
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        let mut state = load(&self.path)?;
        change(&mut state)?;
        save(&self.path, &state)
    }
}

/// Versioned, forward-compatible persisted application state.
#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct StateFile {
    #[serde(default = "schema_version")]
    pub schema_version: u32,
    #[serde(default)]
    pub snoozed: BTreeMap<String, SnoozeEntry>,
    #[serde(default)]
    pub preferences: Preferences,
    /// Unknown top-level fields preserved across writes for forward compatibility.
    #[serde(flatten)]
    extra: BTreeMap<String, Value>,
}

impl Default for StateFile {
    fn default() -> Self {
        Self {
            schema_version: schema_version(),
            snoozed: BTreeMap::new(),
            preferences: Preferences::default(),
            extra: BTreeMap::new(),
        }
    }
}

impl StateFile {
    /// Decodes persisted RFC 3339 deadlines into UTC instants.
    pub fn decoded_snoozes(&self) -> Result<BTreeMap<String, OffsetDateTime>> {
        self.snoozed
            .iter()
            .map(|(key, entry)| {
                let parsed = OffsetDateTime::parse(
                    &entry.snoozed_until,
                    &time::format_description::well_known::Rfc3339,
                )
                .with_context(|| format!("invalid snooze deadline for {key:?}"))?;
                Ok((key.clone(), parsed.to_offset(UtcOffset::UTC)))
            })
            .collect()
    }

    /// Reconciles snoozes while retaining unknown fields on entries that survive.
    pub fn replace_snoozes(&mut self, snoozes: &BTreeMap<String, OffsetDateTime>) -> Result<()> {
        self.snoozed.retain(|key, _| snoozes.contains_key(key));
        for (key, until) in snoozes {
            let formatted = until
                .format(&time::format_description::well_known::Rfc3339)
                .context("failed to format snooze deadline")?;
            match self.snoozed.get_mut(key) {
                Some(entry) => entry.snoozed_until = formatted,
                None => {
                    self.snoozed.insert(
                        key.clone(),
                        SnoozeEntry {
                            snoozed_until: formatted,
                            extra: BTreeMap::new(),
                        },
                    );
                }
            }
        }
        Ok(())
    }
}

/// Persisted snooze record with room for fields introduced by newer versions.
#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct SnoozeEntry {
    /// RFC 3339 deadline, chosen for compatibility and human inspection.
    pub snoozed_until: String,
    #[serde(flatten)]
    extra: BTreeMap<String, Value>,
}

/// User-controlled presentation state.
#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct Preferences {
    #[serde(default)]
    pub theme: Theme,
    #[serde(default)]
    pub sections: SectionPreferences,
    /// Last known normal placement plus the display state to restore.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub window_geometry: Option<WindowGeometry>,
    #[serde(flatten)]
    extra: BTreeMap<String, Value>,
}

impl Default for Preferences {
    fn default() -> Self {
        Self {
            theme: Theme::Dark,
            sections: SectionPreferences::default(),
            window_geometry: None,
            extra: BTreeMap::new(),
        }
    }
}

/// Native outer-window placement in physical pixels.
///
/// Width and height always describe the most recent normal (not maximized or
/// fullscreen) bounds. `maximized` is restored as a separate display state.
#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct WindowGeometry {
    pub x: i32,
    pub y: i32,
    pub width: u32,
    pub height: u32,
    #[serde(default)]
    pub maximized: bool,
}

impl WindowGeometry {
    /// Returns normal bounds without carrying a stale maximized flag.
    pub(crate) fn as_normal(mut self) -> Self {
        self.maximized = false;
        self
    }
}

/// Explicit color scheme rather than following system changes after startup.
#[derive(Clone, Copy, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum Theme {
    #[default]
    Dark,
    Light,
}

/// Expansion state for independently collapsible UI sections.
#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct SectionPreferences {
    #[serde(default = "default_expanded")]
    pub servers_expanded: bool,
    #[serde(default = "default_expanded")]
    pub active_incidents_expanded: bool,
    #[serde(default)]
    pub snoozed_incidents_expanded: bool,
    #[serde(flatten)]
    extra: BTreeMap<String, Value>,
}

impl Default for SectionPreferences {
    fn default() -> Self {
        Self {
            servers_expanded: true,
            active_incidents_expanded: true,
            snoozed_incidents_expanded: false,
            extra: BTreeMap::new(),
        }
    }
}

fn schema_version() -> u32 {
    1
}

fn default_expanded() -> bool {
    true
}

/// Returns the renamed state path after safely importing existing Salmon Watch
/// state and preserving pre-v1 data for the legacy application.
pub fn default_state_path() -> Result<PathBuf> {
    let home = dirs::home_dir().context("could not determine the user home directory")?;
    let state_path = home.join(STATE_FILENAME);
    let former_path = home.join(FORMER_STATE_FILENAME);
    let legacy_path = home.join(LEGACY_STATE_FILENAME);
    if state_path.exists() {
        return Ok(state_path);
    }
    if preserve_legacy_state(&former_path, &legacy_path)? {
        log::info!(
            "copied legacy state from {} to {}",
            former_path.display(),
            legacy_path.display()
        );
    }
    if copy_renamed_state(&former_path, &state_path)? {
        log::info!(
            "imported Salmon Watch state from {} to {}",
            former_path.display(),
            state_path.display()
        );
    }
    Ok(state_path)
}

/// Copies state from the former application name without modifying the source
/// or replacing state already written by Montray UI.
fn copy_renamed_state(source: &Path, destination: &Path) -> Result<bool> {
    if destination.exists() {
        return Ok(false);
    }
    let data = match fs::read(source) {
        Ok(data) => data,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(false),
        Err(error) => {
            return Err(error)
                .with_context(|| format!("failed to read former state file {}", source.display()));
        }
    };
    persist_copy_noclobber(&data, destination)
}

/// Atomically copies an unversioned (or pre-v1) canonical file to the legacy
/// path without ever replacing a copy created by another process.
fn preserve_legacy_state(source: &Path, legacy_path: &Path) -> Result<bool> {
    if legacy_path.exists() {
        return Ok(false);
    }

    let data = match fs::read(source) {
        Ok(data) => data,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(false),
        Err(error) => {
            return Err(error).with_context(|| {
                format!(
                    "failed to read state file for migration {}",
                    source.display()
                )
            });
        }
    };
    let value: Value = serde_json::from_slice(&data).with_context(|| {
        format!(
            "failed to inspect state file for migration {}",
            source.display()
        )
    })?;
    let Some(fields) = value.as_object() else {
        anyhow::bail!(
            "state file for migration is not a JSON object: {}",
            source.display()
        );
    };
    let is_legacy = match fields.get("schema_version") {
        None => true,
        Some(version) => {
            version
                .as_u64()
                .context("state schema_version is not a non-negative integer")?
                < 1
        }
    };
    if !is_legacy {
        return Ok(false);
    }

    persist_copy_noclobber(&data, legacy_path)
}

fn persist_copy_noclobber(data: &[u8], destination: &Path) -> Result<bool> {
    let parent = destination
        .parent()
        .context("state filename has no parent directory")?;
    let mut temporary = tempfile::NamedTempFile::new_in(parent).with_context(|| {
        format!(
            "failed to create temporary state file in {}",
            parent.display()
        )
    })?;
    set_owner_only_permissions(temporary.as_file())?;
    temporary
        .write_all(&data)
        .context("failed to copy legacy state into temporary file")?;
    temporary
        .flush()
        .context("failed to flush temporary legacy state file")?;
    temporary
        .as_file()
        .sync_all()
        .context("failed to sync temporary legacy state file")?;
    match temporary.persist_noclobber(destination) {
        Ok(_) => Ok(true),
        Err(error) if error.error.kind() == std::io::ErrorKind::AlreadyExists => Ok(false),
        Err(error) => Err(error.error)
            .with_context(|| format!("failed to preserve state at {}", destination.display())),
    }
}

/// Loads state, treating absence as first run but surfacing malformed contents.
pub fn load(path: &Path) -> Result<StateFile> {
    match fs::read(path) {
        Ok(data) => serde_json::from_slice(&data)
            .with_context(|| format!("failed to parse state file {}", path.display())),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(StateFile::default()),
        Err(error) => {
            Err(error).with_context(|| format!("failed to read state file {}", path.display()))
        }
    }
}

/// Atomically replaces the state file with an owner-only, flushed temporary file.
///
/// The temporary file is created in the destination directory so the final
/// persist operation stays on one filesystem and can use an atomic rename.
pub fn save(path: &Path, state: &StateFile) -> Result<()> {
    let parent = path
        .parent()
        .context("state filename has no parent directory")?;
    let mut temporary = tempfile::NamedTempFile::new_in(parent).with_context(|| {
        format!(
            "failed to create temporary state file in {}",
            parent.display()
        )
    })?;

    set_owner_only_permissions(temporary.as_file())?;
    {
        let mut writer = BufWriter::new(temporary.as_file_mut());
        serde_json::to_writer_pretty(&mut writer, state).context("failed to encode state file")?;
        writer
            .write_all(b"\n")
            .context("failed to finish state file")?;
        writer.flush().context("failed to flush state file")?;
    }
    temporary
        .as_file()
        .sync_all()
        .context("failed to sync state file")?;
    temporary
        .persist(path)
        .map_err(|error| error.error)
        .with_context(|| format!("failed to replace state file {}", path.display()))?;
    Ok(())
}

#[cfg(unix)]
fn set_owner_only_permissions(file: &fs::File) -> Result<()> {
    use std::os::unix::fs::PermissionsExt;

    file.set_permissions(fs::Permissions::from_mode(0o600))
        .context("failed to set state-file permissions")
}

#[cfg(not(unix))]
fn set_owner_only_permissions(_file: &fs::File) -> Result<()> {
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_state_uses_ui_defaults() {
        let directory = tempfile::tempdir().unwrap();
        let state = load(&directory.path().join("missing.json")).unwrap();

        assert_eq!(state.schema_version, 1);
        assert_eq!(state.preferences.theme, Theme::Dark);
        assert!(state.preferences.sections.servers_expanded);
        assert!(state.preferences.sections.active_incidents_expanded);
        assert!(!state.preferences.sections.snoozed_incidents_expanded);
        assert_eq!(state.preferences.window_geometry, None);
    }

    #[test]
    fn former_state_is_imported_without_removing_or_overwriting_files() {
        let directory = tempfile::tempdir().unwrap();
        let former = directory.path().join(FORMER_STATE_FILENAME);
        let renamed = directory.path().join(STATE_FILENAME);
        let former_contents = br#"{"schema_version":1,"snoozed":{}}"#;
        fs::write(&former, former_contents).unwrap();

        assert!(copy_renamed_state(&former, &renamed).unwrap());
        assert_eq!(fs::read(&former).unwrap(), former_contents);
        assert_eq!(fs::read(&renamed).unwrap(), former_contents);

        let renamed_contents = br#"{"schema_version":1,"preferences":{}}"#;
        fs::write(&renamed, renamed_contents).unwrap();
        assert!(!copy_renamed_state(&former, &renamed).unwrap());
        assert_eq!(fs::read(&renamed).unwrap(), renamed_contents);
    }

    #[test]
    fn unversioned_canonical_state_is_copied_for_legacy_without_removing_source() {
        let directory = tempfile::tempdir().unwrap();
        let source = directory.path().join(STATE_FILENAME);
        let legacy = directory.path().join(LEGACY_STATE_FILENAME);
        let contents = br#"{"snoozed":{"local.disk":{"snoozed_until":"2026-09-06T12:00:00Z"}}}"#;
        fs::write(&source, contents).unwrap();

        assert!(preserve_legacy_state(&source, &legacy).unwrap());

        assert_eq!(fs::read(&source).unwrap(), contents);
        assert_eq!(fs::read(&legacy).unwrap(), contents);
    }

    #[test]
    fn schema_version_zero_is_copied_for_legacy() {
        let directory = tempfile::tempdir().unwrap();
        let source = directory.path().join(STATE_FILENAME);
        let legacy = directory.path().join(LEGACY_STATE_FILENAME);
        let contents = br#"{"schema_version":0,"snoozed":{}}"#;
        fs::write(&source, contents).unwrap();

        assert!(preserve_legacy_state(&source, &legacy).unwrap());

        assert_eq!(fs::read(&legacy).unwrap(), contents);
    }

    #[test]
    fn versioned_canonical_state_is_not_copied_for_legacy() {
        let directory = tempfile::tempdir().unwrap();
        let source = directory.path().join(STATE_FILENAME);
        let legacy = directory.path().join(LEGACY_STATE_FILENAME);
        fs::write(&source, br#"{"schema_version":1,"snoozed":{}}"#).unwrap();

        assert!(!preserve_legacy_state(&source, &legacy).unwrap());

        assert!(!legacy.exists());
    }

    #[test]
    fn existing_legacy_state_is_never_overwritten() {
        let directory = tempfile::tempdir().unwrap();
        let source = directory.path().join(STATE_FILENAME);
        let legacy = directory.path().join(LEGACY_STATE_FILENAME);
        fs::write(&source, br#"{"snoozed":{"source":{}}}"#).unwrap();
        let existing = br#"{"snoozed":{"destination":{}}}"#;
        fs::write(&legacy, existing).unwrap();

        assert!(!preserve_legacy_state(&source, &legacy).unwrap());

        assert_eq!(fs::read(&legacy).unwrap(), existing);
    }

    #[test]
    fn unversioned_snooze_only_shape_loads_with_defaults() {
        let state: StateFile = serde_json::from_str(
            r#"{"snoozed":{"local.disk":{"snoozed_until":"2026-09-06T12:00:00Z"}}}"#,
        )
        .unwrap();

        assert_eq!(state.schema_version, 1);
        assert_eq!(state.snoozed.len(), 1);
        assert_eq!(state.preferences.theme, Theme::Dark);
        assert_eq!(state.preferences.window_geometry, None);
    }

    #[test]
    fn snooze_deadlines_round_trip_as_offset_datetimes() {
        let deadline = OffsetDateTime::from_unix_timestamp(1_800_000_000)
            .unwrap()
            .replace_nanosecond(123_456_789)
            .unwrap();
        let expected = BTreeMap::from([("local.disk".to_owned(), deadline)]);
        let mut state = StateFile::default();

        state.replace_snoozes(&expected).unwrap();

        assert_eq!(state.decoded_snoozes().unwrap(), expected);
        assert_eq!(
            state.snoozed["local.disk"].snoozed_until,
            "2027-01-15T08:00:00.123456789Z"
        );
    }

    #[test]
    fn window_geometry_round_trips() {
        let geometry = WindowGeometry {
            x: -120,
            y: 48,
            width: 940,
            height: 720,
            maximized: true,
        };
        let mut state = StateFile::default();
        state.preferences.window_geometry = Some(geometry);

        let encoded = serde_json::to_vec(&state).unwrap();
        let decoded: StateFile = serde_json::from_slice(&encoded).unwrap();

        assert_eq!(decoded.preferences.window_geometry, Some(geometry));
    }

    #[test]
    fn old_window_geometry_defaults_to_not_maximized() {
        let state: StateFile = serde_json::from_str(
            r#"{"preferences":{"window_geometry":{"x":10,"y":20,"width":800,"height":600}}}"#,
        )
        .unwrap();

        assert!(!state.preferences.window_geometry.unwrap().maximized);
    }

    #[test]
    fn save_is_round_trippable_and_preserves_unknown_fields() {
        let directory = tempfile::tempdir().unwrap();
        let path = directory.path().join("state.json");
        let mut state: StateFile = serde_json::from_str(
            r#"{
                "schema_version": 1,
                "snoozed": {},
                "preferences": {
                    "theme": "light",
                    "sections": {
                        "servers_expanded": false,
                        "active_incidents_expanded": true,
                        "snoozed_incidents_expanded": true,
                        "future_section_setting": 42
                    },
                    "future_preference": "kept"
                },
                "future_top_level": {"also": "kept"}
            }"#,
        )
        .unwrap();
        state.preferences.sections.active_incidents_expanded = false;

        save(&path, &state).unwrap();
        let encoded: Value = serde_json::from_slice(&fs::read(&path).unwrap()).unwrap();

        assert_eq!(encoded["future_top_level"]["also"], "kept");
        assert_eq!(encoded["preferences"]["future_preference"], "kept");
        assert_eq!(
            encoded["preferences"]["sections"]["future_section_setting"],
            42
        );
        assert_eq!(
            encoded["preferences"]["sections"]["active_incidents_expanded"],
            false
        );
    }

    #[cfg(unix)]
    #[test]
    fn saved_state_is_owner_only() {
        use std::os::unix::fs::PermissionsExt;

        let directory = tempfile::tempdir().unwrap();
        let path = directory.path().join("state.json");
        save(&path, &StateFile::default()).unwrap();

        assert_eq!(
            fs::metadata(path).unwrap().permissions().mode() & 0o777,
            0o600
        );
    }
}
