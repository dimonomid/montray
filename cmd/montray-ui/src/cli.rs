use std::ffi::OsString;
use std::path::PathBuf;

use clap::{Args, Parser, Subcommand};

use crate::logging::LogLevel;

/// Parsed process options shared by normal execution and maintenance commands.
#[derive(Debug, PartialEq)]
pub struct Options {
    pub start_hidden: bool,
    /// Explicit Slint scale override; `None` preserves backend DPI detection.
    pub scale: Option<f32>,
    pub log_level: LogLevel,
    /// Config override; resolved to the XDG default by the command dispatcher.
    pub config: Option<PathBuf>,
    pub command: Command,
    pub version: bool,
}

/// Mutually exclusive top-level mode selected from argv.
#[derive(Debug, Default, PartialEq)]
pub enum Command {
    #[default]
    Run,
    Setup {
        operation: SetupOperation,
        reinstall: bool,
        ignore_salmon: bool,
    },
    MigrateFromSalmon {
        dry_run: bool,
    },
    GenerateBearerToken {
        server_id: String,
        output: Option<PathBuf>,
    },
}

/// Scope of Linux desktop setup requested by the user.
#[derive(Clone, Copy, Debug, Default, Eq, PartialEq)]
pub enum SetupOperation {
    #[default]
    Complete,
    CreateConfig,
    InstallAutostart,
    InstallLauncher,
}

/// Clap's declarative grammar, kept private so the rest of the application is
/// independent of parser-specific subcommand wrappers.
#[derive(Debug, Parser)]
#[command(
    name = "montray-ui",
    about = "Show Montray status in the desktop tray",
    disable_version_flag = true
)]
struct Cli {
    /// Configuration file.
    #[arg(long, global = true, value_name = "FILE")]
    config: Option<PathBuf>,

    /// Start with the status window hidden.
    #[arg(long)]
    start_hidden: bool,

    /// Set the UI scale factor instead of using automatic DPI detection.
    #[arg(long, value_name = "FACTOR", value_parser = parse_scale)]
    scale: Option<f32>,

    /// Set logging verbosity.
    #[arg(
        long,
        value_enum,
        ignore_case = true,
        default_value = "info",
        value_name = "LEVEL"
    )]
    log_level: LogLevel,

    /// Print version and build information.
    #[arg(short = 'V', long, global = true)]
    version: bool,

    #[command(subcommand)]
    command: Option<CliCommand>,
}

/// Maintenance commands represented in the shape Clap expects.
#[derive(Debug, Subcommand)]
enum CliCommand {
    /// Create configuration and install the desktop integration.
    Setup(SetupArgs),
    /// Generate a bearer token for one Montray Server.
    GenerateBearerToken(GenerateBearerTokenArgs),
}

/// Arguments shared by complete and targeted setup runs.
#[derive(Debug, Args)]
struct SetupArgs {
    /// Privately back up and replace existing desktop integration files.
    #[arg(long, global = true)]
    reinstall: bool,

    /// Create a separate installation even when Salmon Watch is installed.
    #[arg(long)]
    ignore_salmon: bool,

    #[command(subcommand)]
    operation: Option<SetupCommand>,
}

/// Optional restriction to one part of setup.
#[derive(Debug, Subcommand)]
enum SetupCommand {
    /// Create the default configuration if it does not exist.
    CreateConfig,
    /// Install the desktop autostart entry.
    InstallAutostart,
    /// Install the desktop application launcher.
    InstallLauncher,
    /// Migrate configuration and desktop integration from Salmon Watch.
    MigrateFromSalmon(MigrationArgs),
}

/// Non-mutating controls specific to the one-way Salmon migration workflow.
#[derive(Debug, Args)]
struct MigrationArgs {
    /// Print the migration plan without changing anything.
    #[arg(long)]
    dry_run: bool,
}

/// Arguments for secure bearer-token generation.
#[derive(Debug, Args)]
struct GenerateBearerTokenArgs {
    /// ID of the server that will use this token.
    #[arg(value_name = "SERVER_ID")]
    server_id: String,

    /// Token filename; defaults next to the configuration under `tokens/`.
    #[arg(long, value_name = "FILE")]
    output: Option<PathBuf>,
}

impl From<Cli> for Options {
    fn from(cli: Cli) -> Self {
        let command = match cli.command {
            None => Command::Run,
            Some(CliCommand::Setup(setup)) => match setup.operation {
                Some(SetupCommand::MigrateFromSalmon(migration)) => Command::MigrateFromSalmon {
                    dry_run: migration.dry_run,
                },
                operation => Command::Setup {
                    operation: match operation {
                        None => SetupOperation::Complete,
                        Some(SetupCommand::CreateConfig) => SetupOperation::CreateConfig,
                        Some(SetupCommand::InstallAutostart) => SetupOperation::InstallAutostart,
                        Some(SetupCommand::InstallLauncher) => SetupOperation::InstallLauncher,
                        Some(SetupCommand::MigrateFromSalmon(_)) => unreachable!(),
                    },
                    reinstall: setup.reinstall,
                    ignore_salmon: setup.ignore_salmon,
                },
            },
            Some(CliCommand::GenerateBearerToken(token)) => Command::GenerateBearerToken {
                server_id: token.server_id,
                output: token.output,
            },
        };

        Self {
            start_hidden: cli.start_hidden,
            scale: cli.scale,
            log_level: cli.log_level,
            config: cli.config,
            command,
            version: cli.version,
        }
    }
}

/// Parses an explicit argv tail without printing or exiting.
///
/// This entry point is used by tests; normal startup uses [`parse_env`], which
/// lets Clap render help and argument errors using its standard CLI behavior.
pub fn parse(args: impl IntoIterator<Item = OsString>) -> Result<Options, clap::Error> {
    Cli::try_parse_from(std::iter::once(OsString::from("montray-ui")).chain(args))
        .map(Options::from)
}

/// Parses the process arguments, printing generated help or errors and exiting
/// when Clap encounters a terminal CLI action.
pub fn parse_env() -> Options {
    Cli::parse().into()
}

fn parse_scale(value: &str) -> Result<f32, String> {
    let scale: f32 = value
        .parse()
        .map_err(|_| format!("invalid scale factor {value:?}"))?;
    if scale.is_finite() && scale > 0.0 {
        Ok(scale)
    } else {
        Err("scale factor must be greater than zero".into())
    }
}

#[cfg(test)]
mod tests {
    use clap::error::ErrorKind;

    use super::*;

    #[test]
    fn defaults_select_run_mode_and_info_logging() {
        let options = parse([]).unwrap();
        assert_eq!(options.command, Command::Run);
        assert_eq!(options.log_level, LogLevel::Info);
        assert!(!options.start_hidden);
        assert_eq!(options.scale, None);
        assert_eq!(options.config, None);
        assert!(!options.version);
    }

    #[test]
    fn parses_config_start_hidden_scale_and_log_level() {
        let options = parse([
            "--config=config.yml".into(),
            "--start-hidden".into(),
            "--scale=1.25".into(),
            "--log-level=DEBUG".into(),
        ])
        .unwrap();
        assert_eq!(options.config, Some(PathBuf::from("config.yml")));
        assert!(options.start_hidden);
        assert_eq!(options.scale, Some(1.25));
        assert_eq!(options.log_level, LogLevel::Debug);
    }

    #[test]
    fn parses_short_and_long_version_flags() {
        assert!(parse(["--version".into()]).unwrap().version);
        assert!(parse(["-V".into()]).unwrap().version);
    }

    #[test]
    fn rejects_invalid_and_missing_values() {
        for value in ["0", "-1", "NaN", "inf", "wat"] {
            assert!(parse(["--scale".into(), value.into()]).is_err());
        }
        assert!(parse(["--scale".into()]).is_err());
        assert!(parse(["--config".into()]).is_err());
        assert!(parse(["--log-level".into()]).is_err());
        assert!(parse(["--log-level=verbose".into()]).is_err());
        assert!(parse(["--wat".into()]).is_err());
    }

    #[test]
    fn parses_generate_bearer_token_command() {
        let options = parse([
            "--config".into(),
            "relative.yml".into(),
            "generate-bearer-token".into(),
            "--output=secret.token".into(),
            "remote".into(),
        ])
        .unwrap();
        assert_eq!(options.config, Some(PathBuf::from("relative.yml")));
        assert_eq!(
            options.command,
            Command::GenerateBearerToken {
                server_id: "remote".into(),
                output: Some(PathBuf::from("secret.token")),
            }
        );

        assert!(parse(["generate-bearer-token".into()]).is_err());
        assert!(parse(["generate-bearer-token".into(), "one".into(), "two".into()]).is_err());
        assert!(parse(["generate-bearer-token".into(), "--output".into()]).is_err());
    }

    #[test]
    fn parses_setup_commands_and_reinstall_in_any_position() {
        assert_eq!(
            parse(["setup".into()]).unwrap().command,
            Command::Setup {
                operation: SetupOperation::Complete,
                reinstall: false,
                ignore_salmon: false,
            }
        );
        let options = parse([
            "setup".into(),
            "--reinstall".into(),
            "install-autostart".into(),
            "--config=custom.yml".into(),
        ])
        .unwrap();
        assert_eq!(options.config, Some(PathBuf::from("custom.yml")));
        assert_eq!(
            options.command,
            Command::Setup {
                operation: SetupOperation::InstallAutostart,
                reinstall: true,
                ignore_salmon: false,
            }
        );
        assert_eq!(
            parse([
                "setup".into(),
                "install-launcher".into(),
                "--reinstall".into(),
            ])
            .unwrap()
            .command,
            Command::Setup {
                operation: SetupOperation::InstallLauncher,
                reinstall: true,
                ignore_salmon: false,
            }
        );
        assert_eq!(
            parse(["setup".into(), "--ignore-salmon".into()])
                .unwrap()
                .command,
            Command::Setup {
                operation: SetupOperation::Complete,
                reinstall: false,
                ignore_salmon: true,
            }
        );
        assert_eq!(
            parse([
                "setup".into(),
                "migrate-from-salmon".into(),
                "--dry-run".into(),
            ])
            .unwrap()
            .command,
            Command::MigrateFromSalmon { dry_run: true }
        );
        assert!(
            parse([
                "setup".into(),
                "migrate-from-salmon".into(),
                "--keep-old".into(),
            ])
            .is_err()
        );
    }

    #[test]
    fn rejects_unknown_or_multiple_setup_operations() {
        assert!(parse(["setup".into(), "unexpected".into()]).is_err());
        assert!(
            parse([
                "setup".into(),
                "create-config".into(),
                "install-launcher".into(),
            ])
            .is_err()
        );
    }

    #[test]
    fn clap_generates_root_and_subcommand_help() {
        let root = parse(["--help".into()]).unwrap_err();
        assert_eq!(root.kind(), ErrorKind::DisplayHelp);
        let root = root.to_string();
        assert!(root.contains("Show Montray status in the desktop tray"));
        assert!(root.contains("generate-bearer-token"));
        assert!(root.contains("--log-level <LEVEL>"));
        assert!(root.contains("[default: info]"));

        let setup = parse(["setup".into(), "--help".into()]).unwrap_err();
        assert_eq!(setup.kind(), ErrorKind::DisplayHelp);
        let setup = setup.to_string();
        assert!(setup.contains("create-config"));
        assert!(setup.contains("install-autostart"));
        assert!(setup.contains("install-launcher"));
        assert!(setup.contains("migrate-from-salmon"));
        assert!(setup.contains("--reinstall"));
    }
}
