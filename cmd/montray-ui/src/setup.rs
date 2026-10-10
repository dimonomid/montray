use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};

use anyhow::{Context, Result, bail};
use tempfile::NamedTempFile;

use crate::cli::SetupOperation;
use crate::config::Config;

const DEFAULT_CONFIG: &[u8] = include_bytes!("../assets/setup/montray-ui.yml");
const APPLICATION_ICON: &[u8] = include_bytes!("../assets/app-icon.svg");
const DESKTOP_ENTRY_TEMPLATE: &str = include_str!("../assets/setup/montray-ui.desktop.tpl");
const DESKTOP_EXEC_PLACEHOLDER: &str = "{{EXEC}}";
const DESKTOP_ENTRY_VERSION_KEY: &str = "X-Montray-Desktop-Entry-Version";

/// Creates the default user configuration when absent without replacing an
/// existing file. The result reports whether this call created it.
pub(crate) fn create_default_config(config_filename: &Path) -> Result<bool> {
    Ok(install_file(config_filename, DEFAULT_CONFIG, false)? == FileResult::Created)
}

/// Concrete XDG destinations, grouped so tests can redirect setup into a sandbox.
#[derive(Clone, Debug)]
struct InstallPaths {
    /// Login autostart entry under the XDG configuration directory.
    autostart: PathBuf,
    /// Application-menu entry under the XDG data directory.
    launcher: PathBuf,
    /// Scalable icon referenced by both desktop entries.
    icon: PathBuf,
}

/// User-facing result of one atomic file installation.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum FileResult {
    Created,
    Preserved,
    Reinstalled,
}

/// Executes Linux desktop setup using current-process and XDG locations.
///
/// `--reinstall` applies only to desktop integration. Configuration is always
/// no-clobber, even during a reinstall, because it may contain credentials and
/// hand-edited server definitions.
pub fn execute(
    output: &mut dyn Write,
    config_filename: &Path,
    operation: SetupOperation,
    reinstall: bool,
    ignore_salmon: bool,
) -> Result<()> {
    validate_platform(std::env::consts::OS)?;
    if operation == SetupOperation::CreateConfig {
        let result = install_file(config_filename, DEFAULT_CONFIG, false)?;
        report(output, "configuration", config_filename, result)?;
        return Ok(());
    }
    let needs_config_home = matches!(
        operation,
        SetupOperation::Complete | SetupOperation::InstallAutostart
    );
    let needs_data_home = matches!(
        operation,
        SetupOperation::Complete | SetupOperation::InstallLauncher
    );
    let config_home = if needs_config_home {
        dirs::config_dir().context("determine user configuration directory")?
    } else {
        PathBuf::new()
    };
    let data_home = if needs_data_home {
        dirs::data_dir().context("determine user data directory")?
    } else {
        PathBuf::new()
    };
    let paths = install_paths(&config_home, &data_home);
    if operation == SetupOperation::Complete && !ignore_salmon {
        reject_salmon_watch_installation(config_filename, &paths, &config_home, &data_home)?;
    }
    let executable = executable_path()?;
    execute_with(
        output,
        config_filename,
        operation,
        reinstall,
        &paths,
        &executable,
        &std::env::temp_dir(),
    )?;
    Ok(())
}

fn install_paths(config_home: &Path, data_home: &Path) -> InstallPaths {
    InstallPaths {
        autostart: config_home.join("autostart/montray-ui.desktop"),
        launcher: data_home.join("applications/montray-ui.desktop"),
        icon: data_home.join("icons/hicolor/scalable/apps/montray-ui.svg"),
    }
}

/// Former setup-created paths recognized as belonging to Salmon Watch.
#[derive(Clone, Debug)]
struct SalmonWatchPaths {
    /// Default configuration whose sibling token files may also need copying.
    config: PathBuf,
    /// Native and web-based legacy login entries, in that order.
    autostart: [PathBuf; 2],
    /// Native and web-based legacy application-menu entries, in that order.
    launcher: [PathBuf; 2],
    /// Icons referenced by the corresponding launcher variants.
    icons: [PathBuf; 2],
}

/// Builds both historical desktop layouts because either Salmon Watch UI may
/// have been the last one installed before the project rename.
fn salmon_watch_paths(config_home: &Path, data_home: &Path) -> SalmonWatchPaths {
    SalmonWatchPaths {
        config: config_home.join("salmon-watch/salmon-watch.yml"),
        autostart: [
            config_home.join("autostart/salmon-watch.desktop"),
            config_home.join("autostart/salmon-watch-legacy.desktop"),
        ],
        launcher: [
            data_home.join("applications/salmon-watch.desktop"),
            data_home.join("applications/salmon-watch-legacy.desktop"),
        ],
        icons: [
            data_home.join("icons/hicolor/scalable/apps/salmon-watch.svg"),
            data_home.join("icons/hicolor/scalable/apps/salmon-watch-legacy.svg"),
        ],
    }
}

/// Returns every recognized artifact without following symlinks, so dangling
/// setup-created links still trigger explicit migration rather than fresh setup.
fn existing_salmon_paths(paths: &SalmonWatchPaths) -> Result<Vec<PathBuf>> {
    let mut existing = Vec::new();
    for path in std::iter::once(&paths.config)
        .chain(paths.autostart.iter())
        .chain(paths.launcher.iter())
        .chain(paths.icons.iter())
    {
        match fs::symlink_metadata(path) {
            Ok(_) => existing.push(path.clone()),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
            Err(error) => {
                return Err(error)
                    .with_context(|| format!("inspect Salmon Watch file {}", path.display()));
            }
        }
    }
    Ok(existing)
}

/// Stops bare setup before it can create a parallel UI installation, while
/// allowing repeated setup after Montray desktop integration already exists.
fn reject_salmon_watch_installation(
    config_filename: &Path,
    montray: &InstallPaths,
    config_home: &Path,
    data_home: &Path,
) -> Result<()> {
    let salmon = salmon_watch_paths(config_home, data_home);
    let existing = existing_salmon_paths(&salmon)?;
    if existing.is_empty()
        || (config_filename.exists() && (montray.autostart.exists() || montray.launcher.exists()))
    {
        return Ok(());
    }
    let paths = existing
        .iter()
        .map(|path| format!("  {}", path.display()))
        .collect::<Vec<_>>()
        .join("\n");
    bail!(
        "a Salmon Watch installation was detected:\n{paths}\n\nMigrate it explicitly with:\n\n    montray-ui setup migrate-from-salmon\n\nTo create a separate Montray UI installation, rerun setup with --ignore-salmon"
    )
}

/// Migrates the former per-user configuration and desktop integration, keeping
/// the old configuration directory as the recovery copy.
pub fn migrate_from_salmon(
    output: &mut dyn Write,
    config_filename: &Path,
    dry_run: bool,
) -> Result<()> {
    validate_platform(std::env::consts::OS)?;
    let config_home = dirs::config_dir().context("determine user configuration directory")?;
    let data_home = dirs::data_dir().context("determine user data directory")?;
    let salmon = salmon_watch_paths(&config_home, &data_home);
    let montray = install_paths(&config_home, &data_home);
    let executable = executable_path()?;
    let context = MigrationContext {
        salmon: &salmon,
        montray: &montray,
        executable: &executable,
        backup_parent: &std::env::temp_dir(),
        migrate_state: &crate::persistence::default_state_path,
    };
    migrate_from_salmon_with(output, config_filename, dry_run, &context)
}

/// Filesystem and state-migration dependencies separated from process-global
/// discovery so the destructive workflow can be exercised in a sandbox.
struct MigrationContext<'a> {
    /// Recognized source artifacts eligible for migration and cleanup.
    salmon: &'a SalmonWatchPaths,
    /// Destination desktop files managed by current setup.
    montray: &'a InstallPaths,
    /// Current Montray executable embedded into new desktop entries.
    executable: &'a Path,
    /// Parent for recoverable backups made while replacing desktop entries.
    backup_parent: &'a Path,
    /// Injected state importer, avoiding tests touching the real home directory.
    migrate_state: &'a dyn Fn() -> Result<PathBuf>,
}

/// Performs migration in dependency order: preserve user data, install and
/// validate the replacement, then remove old launchers and their executable.
fn migrate_from_salmon_with(
    output: &mut dyn Write,
    config_filename: &Path,
    dry_run: bool,
    context: &MigrationContext<'_>,
) -> Result<()> {
    let salmon = context.salmon;
    let existing = existing_salmon_paths(salmon)?;
    let former_desktop_integration_exists = existing.iter().any(|path| path != &salmon.config);
    if existing.is_empty() {
        if config_filename.exists() {
            writeln!(
                output,
                "No Salmon Watch installation remains; Montray UI is already configured."
            )?;
            return Ok(());
        }
        bail!("no Salmon Watch installation was found");
    }
    if !former_desktop_integration_exists
        && config_filename.exists()
        && (context.montray.autostart.exists() || context.montray.launcher.exists())
    {
        writeln!(
            output,
            "No Salmon Watch desktop integration remains; Montray UI is already configured."
        )?;
        return Ok(());
    }

    if dry_run {
        writeln!(output, "Would migrate Salmon Watch to Montray UI:")?;
        for path in &existing {
            writeln!(output, "  detected: {}", path.display())?;
        }
        writeln!(
            output,
            "  copy configuration to: {}\n  install Montray UI desktop integration",
            config_filename.display()
        )?;
        writeln!(
            output,
            "  remove former Salmon Watch desktop integration and its referenced executable"
        )?;
        return Ok(());
    }

    migrate_salmon_config(&salmon.config, config_filename)?;
    Config::load(config_filename)
        .with_context(|| format!("validate migrated config at {}", config_filename.display()))?;
    let _ = (context.migrate_state)()?;
    execute_with(
        output,
        config_filename,
        SetupOperation::Complete,
        true,
        context.montray,
        context.executable,
        context.backup_parent,
    )?;

    let mut former_executables = Vec::new();
    for entry in salmon.autostart.iter().chain(salmon.launcher.iter()) {
        if let Some(executable) = generated_salmon_executable(entry)?
            && !former_executables.contains(&executable)
        {
            former_executables.push(executable);
        }
    }
    for path in salmon
        .autostart
        .iter()
        .chain(salmon.launcher.iter())
        .chain(salmon.icons.iter())
    {
        remove_file_if_present(path)?;
    }
    for old_executable in former_executables {
        if old_executable != context.executable {
            remove_former_executable(output, &old_executable, |path| fs::remove_file(path))?;
        }
    }
    writeln!(
        output,
        "Migration complete. Former Salmon Watch desktop integration was removed; its configuration directory was retained as a backup."
    )?;
    Ok(())
}

/// Copies YAML without clobbering an existing Montray config and rewrites only
/// paths rooted in the former config directory, notably generated token paths.
fn migrate_salmon_config(source: &Path, destination: &Path) -> Result<()> {
    if destination.exists() {
        return Ok(());
    }
    let source_data = fs::read(source)
        .with_context(|| format!("read Salmon Watch config at {}", source.display()))?;
    let source_text = String::from_utf8(source_data).context("Salmon Watch config is not UTF-8")?;
    let source_directory = source
        .parent()
        .context("Salmon Watch config has no parent")?;
    let destination_directory = destination
        .parent()
        .context("Montray UI config has no parent")?;
    let migrated = source_text.replace(
        &source_directory.to_string_lossy().to_string(),
        &destination_directory.to_string_lossy(),
    );
    let mut created_files = Vec::new();
    match install_file(destination, migrated.as_bytes(), false)? {
        FileResult::Created => created_files.push(destination.to_owned()),
        FileResult::Preserved => return Ok(()),
        FileResult::Reinstalled => unreachable!("migration never replaces configuration"),
    }
    if let Err(error) = copy_directory_contents_noclobber(
        source_directory,
        destination_directory,
        source,
        &mut created_files,
    ) {
        return Err(rollback_created_files(&created_files, error));
    }
    Ok(())
}

/// Recursively copies config-adjacent files without replacing destination data;
/// symlinks are rejected because reproducing their intent safely is ambiguous.
/// Destinations are journaled before copying so even a partially written file
/// can be removed if this migration attempt fails.
fn copy_directory_contents_noclobber(
    source: &Path,
    destination: &Path,
    skipped: &Path,
    created_files: &mut Vec<PathBuf>,
) -> Result<()> {
    fs::create_dir_all(destination).context("create Montray UI configuration directory")?;
    for entry in fs::read_dir(source)
        .with_context(|| format!("read Salmon Watch directory {}", source.display()))?
    {
        let entry = entry.context("read Salmon Watch directory entry")?;
        let source_path = entry.path();
        if source_path == skipped {
            continue;
        }
        let destination_path = destination.join(entry.file_name());
        let file_type = entry
            .file_type()
            .context("inspect Salmon Watch config entry")?;
        if file_type.is_dir() {
            copy_directory_contents_noclobber(
                &source_path,
                &destination_path,
                skipped,
                created_files,
            )?;
        } else if file_type.is_file() && !destination_path.exists() {
            created_files.push(destination_path.clone());
            fs::copy(&source_path, &destination_path).with_context(|| {
                format!(
                    "copy Salmon Watch file {} to {}",
                    source_path.display(),
                    destination_path.display()
                )
            })?;
        } else if file_type.is_symlink() {
            bail!(
                "refusing to migrate symbolic link in Salmon Watch configuration: {}",
                source_path.display()
            );
        }
    }
    Ok(())
}

/// Best-effort rollback preserves the migration error while reporting the first
/// file that could not be removed, if cleanup itself is incomplete.
fn rollback_created_files(
    created_files: &[PathBuf],
    migration_error: anyhow::Error,
) -> anyhow::Error {
    let mut rollback_error = None;
    for path in created_files.iter().rev() {
        if let Err(error) = fs::remove_file(path)
            && error.kind() != std::io::ErrorKind::NotFound
            && rollback_error.is_none()
        {
            rollback_error = Some(format!("remove {}: {error}", path.display()));
        }
    }
    match rollback_error {
        Some(rollback_error) => migration_error.context(format!(
            "migration rollback was incomplete: {rollback_error}"
        )),
        None => migration_error,
    }
}

/// Extracts an executable only from a version-marked Salmon-generated desktop
/// entry, preventing cleanup from deleting binaries named by arbitrary entries.
fn generated_salmon_executable(entry: &Path) -> Result<Option<PathBuf>> {
    let contents = match fs::read_to_string(entry) {
        Ok(contents) => contents,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(error) => return Err(error).with_context(|| format!("read {}", entry.display())),
    };
    if !contents.contains("X-Salmon-Watch-Desktop-Entry-Version=2") {
        return Ok(None);
    }
    let Some(value) = contents.lines().find_map(|line| line.strip_prefix("Exec=")) else {
        return Ok(None);
    };
    let executable = if let Some(quoted) = value.strip_prefix('"') {
        quoted.split('"').next().unwrap_or_default()
    } else {
        value.split_whitespace().next().unwrap_or_default()
    };
    let path = PathBuf::from(executable.replace("%%", "%"));
    let expected = matches!(
        path.file_name().and_then(|name| name.to_str()),
        Some("salmon-watch" | "salmon-watch-legacy")
    );
    Ok(expected.then_some(path))
}

fn remove_file_if_present(path: &Path) -> Result<()> {
    match fs::remove_file(path) {
        Ok(()) => Ok(()),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(error) => Err(error).with_context(|| format!("remove former file {}", path.display())),
    }
}

/// Leaves a root-owned former executable for explicit administrator cleanup
/// without turning an otherwise completed per-user migration into a failure.
fn remove_former_executable(
    output: &mut dyn Write,
    path: &Path,
    remove: impl FnOnce(&Path) -> std::io::Result<()>,
) -> Result<()> {
    match remove(path) {
        Ok(()) => Ok(()),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(error) if error.kind() == std::io::ErrorKind::PermissionDenied => {
            writeln!(
                output,
                "The former Salmon Watch executable could not be removed without elevated permissions:\n  {}\nRemove it manually with:\n  sudo rm -- {}",
                path.display(),
                shell_argument(&path.to_string_lossy())
            )?;
            Ok(())
        }
        Err(error) => {
            Err(error).with_context(|| format!("remove former executable {}", path.display()))
        }
    }
}

/// Dependency-injected setup implementation used to exercise filesystem edge cases.
///
/// All files eligible for replacement are backed up before the first mutation.
/// Installation itself is per-file atomic, but the group is not transactional:
/// an I/O failure after one successful persist can leave a partially updated setup.
fn execute_with(
    output: &mut dyn Write,
    config_filename: &Path,
    operation: SetupOperation,
    reinstall: bool,
    paths: &InstallPaths,
    executable: &Path,
    backup_parent: &Path,
) -> Result<Option<PathBuf>> {
    let create_config = matches!(
        operation,
        SetupOperation::Complete | SetupOperation::CreateConfig
    );
    let install_autostart = matches!(
        operation,
        SetupOperation::Complete | SetupOperation::InstallAutostart
    );
    let install_launcher = matches!(
        operation,
        SetupOperation::Complete | SetupOperation::InstallLauncher
    );

    if create_config {
        let result = install_file(config_filename, DEFAULT_CONFIG, false)?;
        report(output, "configuration", config_filename, result)?;
    }
    if !install_autostart && !install_launcher {
        return Ok(None);
    }

    let config_filename = std::path::absolute(config_filename).context("resolve config path")?;
    Config::load(&config_filename)
        .with_context(|| format!("validate config at {}", config_filename.display()))?;
    let executable = validate_executable_path(executable, &std::env::temp_dir())?;
    let autostart_entry = desktop_entry(&executable, &config_filename, true)?;
    let launcher_entry = desktop_entry(&executable, &config_filename, false)?;
    let legacy_installation = !reinstall
        && operation == SetupOperation::Complete
        && is_legacy_desktop_entry_file(&paths.autostart)
        && is_legacy_desktop_entry_file(&paths.launcher);

    let backup_targets = if reinstall {
        let mut targets = Vec::new();
        if install_autostart {
            targets.push((&paths.autostart, Path::new("autostart/montray-ui.desktop")));
        }
        if install_launcher {
            targets.push((
                &paths.launcher,
                Path::new("applications/montray-ui.desktop"),
            ));
            targets.push((&paths.icon, Path::new("icons/montray-ui.svg")));
        }
        targets
    } else {
        Vec::new()
    };
    let backup = backup_existing_files(&backup_targets, backup_parent)?;
    if let Some(directory) = &backup {
        writeln!(
            output,
            "Backed up files that will be replaced to {}",
            directory.display()
        )?;
    }

    if install_autostart {
        let result = install_file(&paths.autostart, autostart_entry.as_bytes(), reinstall)?;
        report(output, "desktop autostart entry", &paths.autostart, result)?;
    }
    if install_launcher {
        let icon_result = install_file(&paths.icon, APPLICATION_ICON, reinstall)?;
        report(output, "application icon", &paths.icon, icon_result)?;
        let launcher_result = install_file(&paths.launcher, launcher_entry.as_bytes(), reinstall)?;
        report(
            output,
            "application launcher",
            &paths.launcher,
            launcher_result,
        )?;
    }
    if legacy_installation {
        writeln!(
            output,
            r#"
NOTE: detected desktop integration from the older web-based Montray UI.
Run this setup command again with --reinstall to replace the old files. The configuration will not be overwritten."#
        )?;
    }
    if operation == SetupOperation::Complete {
        writeln!(
            output,
            r#"
Montray UI is configured and installed for desktop autostart and application-menu launch. To start it now, run:

    {}"#,
            start_command(&executable, config_filename.as_path())?
        )?;
    }
    Ok(backup)
}

fn validate_platform(os: &str) -> Result<()> {
    if os != "linux" {
        bail!("montray-ui setup is not implemented on this platform ({os})");
    }
    Ok(())
}

fn executable_path() -> Result<PathBuf> {
    let executable = std::env::current_exe().context("locate executable")?;
    executable.canonicalize().context("resolve executable")
}

/// Rejects installing a path beneath the system temporary directory.
///
/// Desktop entries store the absolute executable path. Accepting a test/build
/// artifact under `/tmp` would leave an autostart entry pointing at an ephemeral
/// file, so this is treated as an error rather than merely warned about.
fn validate_executable_path(executable: &Path, temporary_directory: &Path) -> Result<PathBuf> {
    let executable = std::path::absolute(executable).context("resolve executable")?;
    let temporary_directory =
        std::path::absolute(temporary_directory).context("resolve temporary directory")?;
    if executable.starts_with(&temporary_directory) {
        bail!(
            "refusing to install from temporary executable {}",
            executable.display()
        );
    }
    Ok(executable)
}

/// Builds one desktop entry using freedesktop `Exec` escaping, not shell quoting.
fn desktop_entry(executable: &Path, config_filename: &Path, start_hidden: bool) -> Result<String> {
    let executable = executable
        .to_str()
        .context("executable path must be valid UTF-8 for a desktop entry")?;
    let config_filename = config_filename
        .to_str()
        .context("config path must be valid UTF-8 for a desktop entry")?;
    let hidden = if start_hidden { " --start-hidden" } else { "" };
    let command = format!(
        "{} --config {}{}",
        desktop_exec_argument(executable),
        desktop_exec_argument(config_filename),
        hidden,
    );
    render_desktop_entry_template(DESKTOP_ENTRY_TEMPLATE, &command)
}

/// Replaces the template token only when it occurs exactly once.
///
/// Failing closed here prevents silently generating an entry with a missing or
/// duplicated command if the embedded asset is edited incorrectly.
fn render_desktop_entry_template(template: &str, command: &str) -> Result<String> {
    let mut parts = template.split(DESKTOP_EXEC_PLACEHOLDER);
    let before = parts.next().unwrap_or_default();
    let after = parts
        .next()
        .context("desktop entry template is missing {{EXEC}}")?;
    if parts.next().is_some() {
        bail!("desktop entry template contains {{EXEC}} more than once");
    }
    Ok(format!("{before}{command}{after}"))
}

fn is_legacy_desktop_entry_file(path: &Path) -> bool {
    fs::read_to_string(path).is_ok_and(|contents| is_legacy_desktop_entry(&contents))
}

/// Recognizes the old generated launcher conservatively from its complete signature.
///
/// Presence of the version marker always means "not legacy". Complete setup
/// reports migration only when both launcher files match, avoiding a loud note
/// for unrelated hand-written desktop entries.
fn is_legacy_desktop_entry(contents: &str) -> bool {
    let mut in_desktop_entry = false;
    let mut application = false;
    let mut name = false;
    let mut comment = false;
    let mut icon = false;
    let mut exec = false;

    for line in contents.lines().map(str::trim) {
        if line.starts_with('[') && line.ends_with(']') {
            in_desktop_entry = line == "[Desktop Entry]";
            continue;
        }
        if !in_desktop_entry || line.is_empty() || line.starts_with('#') {
            continue;
        }
        let Some((key, value)) = line.split_once('=') else {
            continue;
        };
        let key = key.trim();
        let value = value.trim();
        if key == DESKTOP_ENTRY_VERSION_KEY {
            return false;
        }
        match key {
            "Type" => application = value == "Application",
            "Name" => name = value == "Montray",
            "Comment" => comment = value == "Show Montray status in the desktop tray",
            "Icon" => icon = value == "montray-ui",
            "Exec" => {
                exec = value
                    .split_once(" --config ")
                    .map(|(executable, _)| executable.trim_matches('"'))
                    .and_then(|executable| executable.rsplit('/').next())
                    == Some("montray-ui");
            }
            _ => {}
        }
    }

    application && name && comment && icon && exec
}

/// Quotes one argument according to the desktop-entry `Exec` field rules.
///
/// This deliberately differs from shell quoting. Percent is doubled because it
/// introduces field codes, while backslash itself must be escaped twice inside
/// a quoted argument.
fn desktop_exec_argument(argument: &str) -> String {
    let mut output = String::with_capacity(argument.len() + 2);
    output.push('"');
    for character in argument.chars() {
        match character {
            '\\' => output.push_str("\\\\\\\\"),
            '"' | '`' => {
                output.push('\\');
                output.push(character);
            }
            '$' => output.push_str("\\\\$"),
            '%' => output.push_str("%%"),
            _ => output.push(character),
        }
    }
    output.push('"');
    output
}

/// Quotes a display-only command so it can be pasted into a POSIX shell.
pub(crate) fn shell_argument(argument: &str) -> String {
    if !argument.is_empty()
        && argument
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || b"_@%+=:,./-".contains(&byte))
    {
        return argument.to_owned();
    }
    format!("'{}'", argument.replace('\'', "'\"'\"'"))
}

/// Formats the post-setup launch command, omitting a redundant default config argument.
fn start_command(executable: &Path, config_filename: &Path) -> Result<String> {
    let executable = shell_argument(
        executable
            .to_str()
            .context("executable path must be valid UTF-8 for a shell command")?,
    );
    let config_text = config_filename
        .to_str()
        .context("config path must be valid UTF-8 for a shell command")?;
    let default_config = crate::config::default_path().ok();
    if default_config.as_deref() == Some(config_filename) {
        Ok(executable)
    } else {
        Ok(format!(
            "{executable} --config {}",
            shell_argument(config_text)
        ))
    }
}

/// Installs one public asset atomically, optionally replacing its destination.
///
/// `persist_noclobber` closes the check/create race in normal setup; the early
/// existence check is only an optimization and source of the friendly result.
fn install_file(path: &Path, contents: &[u8], replace: bool) -> Result<FileResult> {
    let parent = path
        .parent()
        .context("installation filename has no parent")?;
    fs::create_dir_all(parent).context("create parent directory")?;
    if !replace && path.exists() {
        return Ok(FileResult::Preserved);
    }

    let mut temporary = NamedTempFile::new_in(parent).context("create temporary file")?;
    set_public_file_permissions(temporary.as_file())?;
    temporary.write_all(contents).context("write file")?;
    temporary.flush().context("write file")?;
    let existed = path.exists();
    if replace {
        temporary.persist(path).context("replace file")?;
    } else {
        match temporary.persist_noclobber(path) {
            Ok(_) => {}
            Err(error) if error.error.kind() == std::io::ErrorKind::AlreadyExists => {
                return Ok(FileResult::Preserved);
            }
            Err(error) => return Err(error.error).context("create file"),
        }
    }
    Ok(if existed {
        FileResult::Reinstalled
    } else {
        FileResult::Created
    })
}

fn set_public_file_permissions(file: &fs::File) -> Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt as _;
        file.set_permissions(fs::Permissions::from_mode(0o644))
            .context("set file permissions")?;
    }
    Ok(())
}

/// Copies every existing replacement target into one private temporary tree.
///
/// The kept directory intentionally lives below the system temporary directory
/// and is not deleted on success: its printed path is the user's recovery
/// mechanism after reinstall.
fn backup_existing_files(
    targets: &[(&PathBuf, &Path)],
    backup_parent: &Path,
) -> Result<Option<PathBuf>> {
    let mut existing = Vec::new();
    for target in targets {
        match fs::symlink_metadata(target.0) {
            Ok(_) => existing.push(target),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
            Err(error) => {
                return Err(error)
                    .with_context(|| format!("inspect file to back up at {}", target.0.display()));
            }
        }
    }
    if existing.is_empty() {
        return Ok(None);
    }
    fs::create_dir_all(backup_parent).context("create backup parent directory")?;
    let temporary = tempfile::Builder::new()
        .prefix("montray-ui-reinstall-")
        .tempdir_in(backup_parent)
        .context("create reinstall backup directory")?;
    set_private_directory_permissions(temporary.path())?;
    for (source, relative) in existing {
        let destination = temporary.path().join(relative);
        fs::create_dir_all(destination.parent().expect("backup path has a parent"))
            .context("create reinstall backup subdirectory")?;
        fs::copy(source, &destination).with_context(|| {
            format!("back up {} to {}", source.display(), destination.display())
        })?;
    }
    Ok(Some(temporary.keep()))
}

fn set_private_directory_permissions(directory: &Path) -> Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt as _;
        fs::set_permissions(directory, fs::Permissions::from_mode(0o700))
            .context("set reinstall backup directory permissions")?;
    }
    Ok(())
}

fn report(
    output: &mut dyn Write,
    description: &str,
    path: &Path,
    result: FileResult,
) -> Result<()> {
    match result {
        FileResult::Created => writeln!(output, "Created {description} at {}", path.display())?,
        FileResult::Preserved => writeln!(
            output,
            "{}{} already exists at {}; leaving it unchanged",
            description[..1].to_ascii_uppercase(),
            &description[1..],
            path.display()
        )?,
        FileResult::Reinstalled => {
            writeln!(output, "Reinstalled {description} at {}", path.display())?
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    struct TestLayout {
        _root: tempfile::TempDir,
        config: PathBuf,
        paths: InstallPaths,
        executable: PathBuf,
        backups: PathBuf,
    }

    impl TestLayout {
        fn new() -> Self {
            let root = tempfile::tempdir().unwrap();
            Self {
                config: root.path().join("config/montray-ui.yml"),
                paths: InstallPaths {
                    autostart: root.path().join("config/autostart/montray-ui.desktop"),
                    launcher: root.path().join("data/applications/montray-ui.desktop"),
                    icon: root
                        .path()
                        .join("data/icons/hicolor/scalable/apps/montray-ui.svg"),
                },
                executable: PathBuf::from("/opt/Montray/montray-ui"),
                backups: root.path().join("backups"),
                _root: root,
            }
        }

        fn run(&self, operation: SetupOperation, reinstall: bool) -> (String, Option<PathBuf>) {
            let mut output = Vec::new();
            let backup = execute_with(
                &mut output,
                &self.config,
                operation,
                reinstall,
                &self.paths,
                &self.executable,
                &self.backups,
            )
            .unwrap();
            (String::from_utf8(output).unwrap(), backup)
        }
    }

    fn salmon_layout(layout: &TestLayout) -> SalmonWatchPaths {
        let root = layout._root.path();
        SalmonWatchPaths {
            config: root.join("config/salmon-watch/salmon-watch.yml"),
            autostart: [
                root.join("config/autostart/salmon-watch.desktop"),
                root.join("config/autostart/salmon-watch-legacy.desktop"),
            ],
            launcher: [
                root.join("data/applications/salmon-watch.desktop"),
                root.join("data/applications/salmon-watch-legacy.desktop"),
            ],
            icons: [
                root.join("data/icons/hicolor/scalable/apps/salmon-watch.svg"),
                root.join("data/icons/hicolor/scalable/apps/salmon-watch-legacy.svg"),
            ],
        }
    }

    #[test]
    fn bare_setup_rejects_salmon_watch_and_explains_both_choices() {
        let layout = TestLayout::new();
        let salmon = salmon_layout(&layout);
        fs::create_dir_all(salmon.config.parent().unwrap()).unwrap();
        fs::write(&salmon.config, DEFAULT_CONFIG).unwrap();
        let config_home = layout._root.path().join("config");
        let data_home = layout._root.path().join("data");
        let error = reject_salmon_watch_installation(
            &layout.config,
            &layout.paths,
            &config_home,
            &data_home,
        )
        .unwrap_err();
        let text = error.to_string();
        assert!(text.contains("setup migrate-from-salmon"));
        assert!(text.contains("--ignore-salmon"));
        assert!(text.contains(&salmon.config.display().to_string()));
    }

    #[test]
    fn migration_copies_config_tokens_and_replaces_old_desktop_integration() {
        let layout = TestLayout::new();
        let salmon = salmon_layout(&layout);
        let old_executable = layout._root.path().join("bin/salmon-watch");
        fs::create_dir_all(old_executable.parent().unwrap()).unwrap();
        fs::write(&old_executable, "old executable").unwrap();
        let old_directory = salmon.config.parent().unwrap();
        let old_token = old_directory.join("tokens/remote.token");
        fs::create_dir_all(old_token.parent().unwrap()).unwrap();
        fs::write(&old_token, "secret").unwrap();
        let config = format!(
            "wsClient:\n  servers:\n    - id: remote\n      addr: localhost:41990\n      auth:\n        bearerTokenFile: {}\n",
            old_token.display()
        );
        fs::write(&salmon.config, config).unwrap();
        let old_entry = format!(
            "[Desktop Entry]\nX-Salmon-Watch-Desktop-Entry-Version=2\nExec=\"{}\" --config \"{}\"\n",
            old_executable.display(),
            salmon.config.display()
        );
        for path in salmon.autostart.iter().chain(salmon.launcher.iter()) {
            fs::create_dir_all(path.parent().unwrap()).unwrap();
            fs::write(path, &old_entry).unwrap();
        }
        for path in &salmon.icons {
            fs::create_dir_all(path.parent().unwrap()).unwrap();
            fs::write(path, "old icon").unwrap();
        }

        let mut output = Vec::new();
        let migrate_state = || Ok(layout._root.path().join(".montray-state.json"));
        let context = MigrationContext {
            salmon: &salmon,
            montray: &layout.paths,
            executable: &layout.executable,
            backup_parent: &layout.backups,
            migrate_state: &migrate_state,
        };
        migrate_from_salmon_with(&mut output, &layout.config, false, &context).unwrap();

        let migrated = fs::read_to_string(&layout.config).unwrap();
        let new_directory = layout.config.parent().unwrap();
        assert!(migrated.contains(&new_directory.display().to_string()));
        assert!(!migrated.contains(&old_directory.display().to_string()));
        assert_eq!(
            fs::read(new_directory.join("tokens/remote.token")).unwrap(),
            b"secret"
        );
        assert!(
            salmon.config.exists(),
            "former config should remain as backup"
        );
        for path in salmon
            .autostart
            .iter()
            .chain(salmon.launcher.iter())
            .chain(salmon.icons.iter())
        {
            assert!(!path.exists(), "former file remains at {}", path.display());
        }
        assert!(!old_executable.exists());
        assert!(layout.paths.autostart.exists());
        assert!(layout.paths.launcher.exists());
        assert!(
            String::from_utf8(output)
                .unwrap()
                .contains("Migration complete")
        );
        let mut second_output = Vec::new();
        migrate_from_salmon_with(&mut second_output, &layout.config, false, &context).unwrap();
        assert!(
            String::from_utf8(second_output)
                .unwrap()
                .contains("already configured")
        );
    }

    #[test]
    fn migration_dry_run_is_non_destructive() {
        let layout = TestLayout::new();
        let salmon = salmon_layout(&layout);
        fs::create_dir_all(salmon.config.parent().unwrap()).unwrap();
        fs::write(&salmon.config, DEFAULT_CONFIG).unwrap();
        let mut output = Vec::new();
        let migrate_state = || panic!("dry run must not migrate state");
        let context = MigrationContext {
            salmon: &salmon,
            montray: &layout.paths,
            executable: &layout.executable,
            backup_parent: &layout.backups,
            migrate_state: &migrate_state,
        };
        migrate_from_salmon_with(&mut output, &layout.config, true, &context).unwrap();
        assert!(salmon.config.exists());
        assert!(!layout.config.exists());
        let output = String::from_utf8(output).unwrap();
        assert!(output.contains("Would migrate"));
    }

    #[cfg(unix)]
    #[test]
    fn failed_config_migration_rolls_back_new_files_and_can_be_retried() {
        use std::os::unix::fs::symlink;

        let layout = TestLayout::new();
        let salmon = salmon_layout(&layout);
        let source_directory = salmon.config.parent().unwrap();
        let source_token = source_directory.join("tokens/remote.token");
        fs::create_dir_all(source_token.parent().unwrap()).unwrap();
        fs::write(&source_token, "secret").unwrap();
        fs::write(&salmon.config, DEFAULT_CONFIG).unwrap();
        let unsupported_link = source_directory.join("unsupported-link");
        symlink(&source_token, &unsupported_link).unwrap();

        let error = migrate_salmon_config(&salmon.config, &layout.config).unwrap_err();
        assert!(error.to_string().contains("symbolic link"));
        assert!(!layout.config.exists());
        assert!(
            !layout
                .config
                .parent()
                .unwrap()
                .join("tokens/remote.token")
                .exists()
        );

        fs::remove_file(unsupported_link).unwrap();
        migrate_salmon_config(&salmon.config, &layout.config).unwrap();
        assert!(layout.config.exists());
        assert_eq!(
            fs::read(layout.config.parent().unwrap().join("tokens/remote.token")).unwrap(),
            b"secret"
        );
    }

    #[test]
    fn permission_denied_removing_former_executable_becomes_manual_cleanup() {
        let mut output = Vec::new();
        let executable = Path::new("/usr/local/bin/salmon-watch");
        remove_former_executable(&mut output, executable, |_| {
            Err(std::io::Error::from(std::io::ErrorKind::PermissionDenied))
        })
        .unwrap();

        let output = String::from_utf8(output).unwrap();
        assert!(output.contains("could not be removed without elevated permissions"));
        assert!(output.contains("sudo rm -- /usr/local/bin/salmon-watch"));
    }

    #[test]
    fn unexpected_error_removing_former_executable_still_fails_migration() {
        let mut output = Vec::new();
        let error = remove_former_executable(&mut output, Path::new("/opt/salmon-watch"), |_| {
            Err(std::io::Error::from(std::io::ErrorKind::ReadOnlyFilesystem))
        })
        .unwrap_err();

        assert!(error.to_string().contains("remove former executable"));
        assert!(output.is_empty());
    }

    #[test]
    fn complete_setup_creates_valid_config_and_distinct_desktop_entries() {
        let layout = TestLayout::new();
        let (output, backup) = layout.run(SetupOperation::Complete, false);
        assert!(backup.is_none());
        Config::load(&layout.config).unwrap();
        assert_eq!(fs::read(&layout.paths.icon).unwrap(), APPLICATION_ICON);

        let autostart = fs::read_to_string(&layout.paths.autostart).unwrap();
        let launcher = fs::read_to_string(&layout.paths.launcher).unwrap();
        for entry in [&autostart, &launcher] {
            assert!(entry.contains("Type=Application"));
            assert!(entry.contains("Icon=montray-ui"));
            assert!(entry.contains("X-Montray-Desktop-Entry-Version=2"));
            assert!(entry.contains("Terminal=false"));
            assert!(entry.contains("\"/opt/Montray/montray-ui\""));
            assert!(entry.contains(&desktop_exec_argument(layout.config.to_str().unwrap())));
            assert!(!entry.contains(DESKTOP_EXEC_PLACEHOLDER));
        }
        assert!(autostart.contains(" --start-hidden\n"));
        assert!(!launcher.contains("--start-hidden"));
        assert!(output.contains("Created configuration"));
        assert!(output.contains("Created desktop autostart entry"));
        assert!(output.contains("Created application icon"));
        assert!(output.contains("Created application launcher"));
        assert!(output.contains("To start it now"));

        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt as _;
            for path in [
                &layout.config,
                &layout.paths.autostart,
                &layout.paths.launcher,
                &layout.paths.icon,
            ] {
                assert_eq!(
                    fs::metadata(path).unwrap().permissions().mode() & 0o777,
                    0o644
                );
            }
        }
    }

    #[test]
    fn normal_setup_preserves_every_existing_file() {
        let layout = TestLayout::new();
        layout.run(SetupOperation::Complete, false);
        let custom_config = r#"wsClient:
  servers:
    - id: custom
      addr: localhost:1234
"#;
        fs::write(&layout.config, custom_config).unwrap();
        fs::write(&layout.paths.autostart, "custom autostart").unwrap();
        fs::write(&layout.paths.launcher, "custom launcher").unwrap();
        fs::write(&layout.paths.icon, "custom icon").unwrap();

        let (output, backup) = layout.run(SetupOperation::Complete, false);
        assert!(backup.is_none());
        assert_eq!(fs::read_to_string(&layout.config).unwrap(), custom_config);
        assert_eq!(
            fs::read_to_string(&layout.paths.autostart).unwrap(),
            "custom autostart"
        );
        assert_eq!(
            fs::read_to_string(&layout.paths.launcher).unwrap(),
            "custom launcher"
        );
        assert_eq!(
            fs::read_to_string(&layout.paths.icon).unwrap(),
            "custom icon"
        );
        assert!(output.contains("Configuration already exists"));
        assert!(output.contains("Desktop autostart entry already exists"));
        assert!(output.contains("Application icon already exists"));
        assert!(output.contains("Application launcher already exists"));
        assert!(!output.contains("older web-based Montray UI"));
    }

    #[test]
    fn detects_legacy_desktop_files_and_prints_one_migration_note() {
        let layout = TestLayout::new();
        layout.run(SetupOperation::CreateConfig, false);
        let version_line = format!("{DESKTOP_ENTRY_VERSION_KEY}=2\n");
        let legacy_entry = desktop_entry(&layout.executable, &layout.config, false)
            .unwrap()
            .replace(&version_line, "");
        assert!(is_legacy_desktop_entry(&legacy_entry));
        assert!(!is_legacy_desktop_entry(
            &desktop_entry(&layout.executable, &layout.config, false).unwrap()
        ));

        fs::create_dir_all(layout.paths.autostart.parent().unwrap()).unwrap();
        fs::create_dir_all(layout.paths.launcher.parent().unwrap()).unwrap();
        fs::write(&layout.paths.autostart, &legacy_entry).unwrap();
        fs::write(&layout.paths.launcher, &legacy_entry).unwrap();

        let (output, backup) = layout.run(SetupOperation::Complete, false);
        assert!(backup.is_none());
        assert_eq!(
            output.matches("NOTE: detected desktop integration").count(),
            1
        );
        assert!(output.contains("older web-based Montray UI"));
        assert!(output.contains("--reinstall"));
        assert!(output.contains("configuration will not be overwritten"));
        assert_eq!(
            fs::read_to_string(&layout.paths.autostart).unwrap(),
            legacy_entry
        );
    }

    #[test]
    fn one_unversioned_desktop_file_is_not_enough_for_legacy_detection() {
        let layout = TestLayout::new();
        layout.run(SetupOperation::CreateConfig, false);
        fs::create_dir_all(layout.paths.autostart.parent().unwrap()).unwrap();
        let version_line = format!("{DESKTOP_ENTRY_VERSION_KEY}=2\n");
        let legacy_entry = desktop_entry(&layout.executable, &layout.config, false)
            .unwrap()
            .replace(&version_line, "");
        fs::write(&layout.paths.autostart, legacy_entry).unwrap();

        let (output, _) = layout.run(SetupOperation::Complete, false);
        assert!(!output.contains("older web-based Montray UI"));
    }

    #[test]
    fn reinstall_does_not_print_a_redundant_legacy_note() {
        let layout = TestLayout::new();
        layout.run(SetupOperation::CreateConfig, false);
        fs::create_dir_all(layout.paths.autostart.parent().unwrap()).unwrap();
        fs::create_dir_all(layout.paths.launcher.parent().unwrap()).unwrap();
        let version_line = format!("{DESKTOP_ENTRY_VERSION_KEY}=2\n");
        let legacy_entry = desktop_entry(&layout.executable, &layout.config, false)
            .unwrap()
            .replace(&version_line, "");
        fs::write(&layout.paths.autostart, &legacy_entry).unwrap();
        fs::write(&layout.paths.launcher, legacy_entry).unwrap();

        let (output, _) = layout.run(SetupOperation::Complete, true);
        assert!(!output.contains("older web-based Montray UI"));
    }

    #[test]
    fn reinstall_backs_up_and_replaces_desktop_files_but_not_config() {
        let layout = TestLayout::new();
        layout.run(SetupOperation::Complete, false);
        let custom_config = r#"wsClient:
  servers:
    - id: custom
      addr: localhost:1234
"#;
        fs::write(&layout.config, custom_config).unwrap();
        fs::write(&layout.paths.autostart, "old autostart").unwrap();
        fs::write(&layout.paths.launcher, "old launcher").unwrap();
        fs::write(&layout.paths.icon, "old icon").unwrap();

        let (output, backup) = layout.run(SetupOperation::Complete, true);
        let backup = backup.expect("reinstall did not create a backup");
        assert_eq!(
            fs::read_to_string(backup.join("autostart/montray-ui.desktop")).unwrap(),
            "old autostart"
        );
        assert_eq!(
            fs::read_to_string(backup.join("applications/montray-ui.desktop")).unwrap(),
            "old launcher"
        );
        assert_eq!(
            fs::read_to_string(backup.join("icons/montray-ui.svg")).unwrap(),
            "old icon"
        );
        assert_eq!(fs::read_to_string(&layout.config).unwrap(), custom_config);
        assert!(
            fs::read_to_string(&layout.paths.autostart)
                .unwrap()
                .contains("--start-hidden")
        );
        assert!(
            !fs::read_to_string(&layout.paths.launcher)
                .unwrap()
                .contains("--start-hidden")
        );
        assert_eq!(fs::read(&layout.paths.icon).unwrap(), APPLICATION_ICON);
        assert!(output.contains(&format!(
            "Backed up files that will be replaced to {}",
            backup.display()
        )));
        assert!(output.contains("Configuration already exists"));
        assert!(output.contains("Reinstalled desktop autostart entry"));
        assert!(output.contains("Reinstalled application icon"));
        assert!(output.contains("Reinstalled application launcher"));

        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt as _;
            assert_eq!(
                fs::metadata(backup).unwrap().permissions().mode() & 0o777,
                0o700
            );
        }
    }

    #[test]
    fn a_backup_failure_prevents_all_replacements() {
        let layout = TestLayout::new();
        fs::create_dir_all(layout.paths.autostart.parent().unwrap()).unwrap();
        fs::create_dir_all(&layout.paths.launcher).unwrap();
        fs::create_dir_all(layout.paths.icon.parent().unwrap()).unwrap();
        fs::write(&layout.paths.autostart, "old autostart").unwrap();
        fs::write(&layout.paths.icon, "old icon").unwrap();
        fs::create_dir_all(layout.config.parent().unwrap()).unwrap();
        fs::write(&layout.config, DEFAULT_CONFIG).unwrap();

        let error = execute_with(
            &mut Vec::new(),
            &layout.config,
            SetupOperation::Complete,
            true,
            &layout.paths,
            &layout.executable,
            &layout.backups,
        )
        .unwrap_err();
        assert!(error.to_string().contains("back up"), "{error:#}");
        assert_eq!(
            fs::read_to_string(&layout.paths.autostart).unwrap(),
            "old autostart"
        );
        assert_eq!(fs::read_to_string(&layout.paths.icon).unwrap(), "old icon");
        assert!(layout.paths.launcher.is_dir());
    }

    #[test]
    fn individual_operations_touch_only_their_own_files() {
        let layout = TestLayout::new();
        let (output, _) = layout.run(SetupOperation::CreateConfig, true);
        assert!(output.contains("Created configuration"));
        assert!(!layout.paths.autostart.exists());
        assert!(!layout.paths.launcher.exists());

        layout.run(SetupOperation::InstallAutostart, false);
        assert!(layout.paths.autostart.exists());
        assert!(!layout.paths.launcher.exists());
        assert!(!layout.paths.icon.exists());

        layout.run(SetupOperation::InstallLauncher, false);
        assert!(layout.paths.launcher.exists());
        assert!(layout.paths.icon.exists());
    }

    #[test]
    fn desktop_and_shell_arguments_are_escaped_like_the_old_client() {
        assert_eq!(
            desktop_exec_argument("a b%\"\\$`"),
            "\"a b%%\\\"\\\\\\\\\\\\$\\`\""
        );
        assert_eq!(shell_argument("/tmp/simple.yml"), "/tmp/simple.yml");
        assert_eq!(
            shell_argument("/tmp/custom config's.yml"),
            "'/tmp/custom config'\"'\"'s.yml'"
        );
    }

    #[test]
    fn desktop_template_requires_exactly_one_exec_placeholder() {
        assert_eq!(
            render_desktop_entry_template("before {{EXEC}} after", "command").unwrap(),
            "before command after"
        );
        assert!(render_desktop_entry_template("no placeholder", "command").is_err());
        assert!(render_desktop_entry_template("{{EXEC}} and {{EXEC}}", "command").is_err());
    }

    #[test]
    fn validates_platform_and_rejects_temporary_executables() {
        validate_platform("linux").unwrap();
        assert!(
            validate_platform("macos")
                .unwrap_err()
                .to_string()
                .contains("macos")
        );
        assert!(
            validate_platform("windows")
                .unwrap_err()
                .to_string()
                .contains("windows")
        );
        assert!(
            validate_executable_path(Path::new("/tmp/build/montray"), Path::new("/tmp"))
                .unwrap_err()
                .to_string()
                .contains("temporary executable")
        );
        assert_eq!(
            validate_executable_path(Path::new("/opt/montray-ui"), Path::new("/tmp")).unwrap(),
            Path::new("/opt/montray-ui")
        );
    }
}
