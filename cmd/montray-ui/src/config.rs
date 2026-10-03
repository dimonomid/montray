use std::collections::HashSet;
use std::fs;
use std::net::{IpAddr, TcpListener};
use std::path::{Path, PathBuf};

use anyhow::{Context, Result, bail};
use serde::Deserialize;

/// Top-level YAML configuration compatible with the original Go watcher.
#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
pub struct Config {
    pub ws_client: WsClientConfig,
}

/// Collection of independently supervised Montray Server endpoints.
#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct WsClientConfig {
    pub servers: Vec<ServerConfig>,
}

/// One logical Montray Server endpoint and its optional transport layers.
#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ServerConfig {
    /// Stable namespace used to qualify incident keys and persistence entries.
    pub id: String,
    /// TCP `host:port`, without a URL scheme; loopback listener when tunneled.
    #[serde(default)]
    pub addr: String,
    #[serde(default)]
    pub tls: Option<TlsConfig>,
    #[serde(default)]
    pub auth: Option<AuthConfig>,
    #[serde(default)]
    pub tunnel: Option<TunnelConfig>,
}

/// TLS client verification settings.
#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
pub struct TlsConfig {
    /// Optional PEM CA bundle added to, rather than replacing, system roots.
    #[serde(default)]
    pub ca_file: String,
    /// Optional verification/URI hostname when `addr` is an IP or tunnel endpoint.
    #[serde(default)]
    pub server_name: String,
}

/// Bearer authentication loaded from a file to avoid secrets in YAML and argv.
#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
pub struct AuthConfig {
    pub bearer_token_file: String,
}

/// Choice of tunnel adapter; exactly one field must be present.
#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
pub struct TunnelConfig {
    /// Convenience adapter that expands structured settings into OpenSSH argv.
    #[serde(default)]
    pub ssh: Option<SshTunnelConfig>,
    /// Direct adapter for any persistent process that provides the configured endpoint.
    #[serde(default)]
    pub custom_command: Option<CustomTunnelCommandConfig>,
}

/// Arbitrary persistent tunnel process executed directly, without a shell.
#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
pub struct CustomTunnelCommandConfig {
    /// Executable followed by its arguments.
    #[serde(default)]
    pub command: Vec<String>,
    /// Optional output signal; absence means ready immediately after process start.
    #[serde(default)]
    pub readiness_probe: Option<TunnelReadinessProbeConfig>,
}

/// Output substring that marks one custom-command generation ready.
#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
pub struct TunnelReadinessProbeConfig {
    /// Exact byte-compatible text searched for across both output streams.
    pub contains_output: String,
}

/// External OpenSSH local-forward configuration.
#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
pub struct SshTunnelConfig {
    /// SSH destination host, passed after all options to avoid option injection.
    pub host: String,
    pub user: String,
    /// SSH port; zero in YAML means the conventional port 22.
    #[serde(default)]
    pub port: u16,
    /// Destination visible from the SSH server, used as the `-L` target.
    #[serde(alias = "remoteSalmonAddr")]
    pub remote_server_addr: String,
    /// Trusted, user-supplied OpenSSH arguments inserted before the destination.
    #[serde(default)]
    pub extra_ssh_args: Vec<String>,
}

impl Config {
    pub fn load(path: &Path) -> Result<Self> {
        let data =
            fs::read(path).with_context(|| format!("failed to read config {}", path.display()))?;
        let config: Self = serde_yaml::from_slice(&data)
            .with_context(|| format!("failed to parse config {}", path.display()))?;
        config.validate()?;
        Ok(config)
    }

    /// Validates cross-field invariants that Serde cannot express.
    pub fn validate(&self) -> Result<()> {
        if self.ws_client.servers.is_empty() {
            bail!("wsClient.servers must contain at least one server");
        }

        let mut ids = HashSet::new();
        for (index, server) in self.ws_client.servers.iter().enumerate() {
            validate_server_id(&server.id)
                .with_context(|| format!("wsClient.servers[{index}].id"))?;
            if !ids.insert(&server.id) {
                bail!("wsClient.servers[{index}].id {:?} is duplicated", server.id);
            }
            if server.addr.is_empty() && !has_structured_ssh_tunnel(server) {
                bail!("wsClient.servers[{index}].addr is required unless tunnel.ssh is configured");
            }
            if !server.addr.is_empty() && (server.addr.contains("//") || server.addr.contains('/'))
            {
                bail!("wsClient.servers[{index}].addr must be a host:port address, not a URL");
            }
            if !server.addr.is_empty() {
                validate_host_port(&server.addr)
                    .with_context(|| format!("wsClient.servers[{index}].addr"))?;
            }
            if server
                .auth
                .as_ref()
                .is_some_and(|auth| auth.bearer_token_file.is_empty())
            {
                bail!("wsClient.servers[{index}].auth.bearerTokenFile is required");
            }
            if let Some(tunnel) = &server.tunnel {
                validate_tunnel(server, tunnel, index)?;
            }
        }
        Ok(())
    }

    /// Replaces omitted structured-SSH endpoints with kernel-assigned loopback ports.
    pub(crate) fn resolve_tunnel_addresses(&mut self) -> Result<()> {
        for (index, server) in self.ws_client.servers.iter_mut().enumerate() {
            if !server.addr.is_empty() {
                continue;
            }
            let listener = TcpListener::bind("127.0.0.1:0").with_context(|| {
                format!("allocating local tunnel address #{index} ({})", server.id)
            })?;
            server.addr = listener
                .local_addr()
                .context("reading allocated local tunnel address")?
                .to_string();
            drop(listener);
            log::info!(
                "server {} allocated local tunnel address {}",
                server.id,
                server.addr
            );
        }
        Ok(())
    }
}

fn has_structured_ssh_tunnel(server: &ServerConfig) -> bool {
    server
        .tunnel
        .as_ref()
        .is_some_and(|tunnel| tunnel.ssh.is_some() && tunnel.custom_command.is_none())
}

/// Validates IDs used in incident namespaces, filenames, and generated commands.
///
/// `internal` is reserved because client-generated incidents use that prefix.
pub fn validate_server_id(id: &str) -> Result<()> {
    if id.is_empty() {
        bail!("is required");
    }
    if !id
        .bytes()
        .all(|byte| byte.is_ascii_alphanumeric() || byte == b'_' || byte == b'-')
    {
        bail!("{id:?} must contain only letters, digits, underscores, or hyphens");
    }
    if id == "internal" {
        bail!("{id:?} is reserved");
    }
    Ok(())
}

/// Validates the tagged tunnel choice before runtime code builds a command.
fn validate_tunnel(server: &ServerConfig, tunnel: &TunnelConfig, index: usize) -> Result<()> {
    let prefix = format!("wsClient.servers[{index}].tunnel");
    match (&tunnel.ssh, &tunnel.custom_command) {
        (Some(ssh), None) => validate_ssh_tunnel(server, ssh, index),
        (None, Some(custom)) => {
            if custom.command.first().is_none_or(String::is_empty) {
                bail!("{prefix}.customCommand.command must start with an executable");
            }
            if custom
                .readiness_probe
                .as_ref()
                .is_some_and(|probe| probe.contains_output.is_empty())
            {
                bail!("{prefix}.customCommand.readinessProbe.containsOutput must not be empty");
            }
            Ok(())
        }
        _ => bail!("{prefix} must contain exactly one of ssh or customCommand"),
    }
}

fn validate_ssh_tunnel(server: &ServerConfig, ssh: &SshTunnelConfig, index: usize) -> Result<()> {
    let prefix = format!("wsClient.servers[{index}].tunnel.ssh");
    if ssh.host.is_empty() {
        bail!("{prefix}.host is required");
    }
    if ssh.host.starts_with('-') {
        bail!("{prefix}.host must not start with a hyphen");
    }
    if ssh.user.is_empty() {
        bail!("{prefix}.user is required");
    }
    if ssh.user.starts_with('-') {
        bail!("{prefix}.user must not start with a hyphen");
    }
    validate_host_port(&ssh.remote_server_addr)
        .with_context(|| format!("{prefix}.remoteServerAddr"))?;
    if server.addr.is_empty() {
        return Ok(());
    }
    let (local_host, _) = validate_host_port(&server.addr)
        .with_context(|| format!("wsClient.servers[{index}].addr for an SSH tunnel"))?;
    // Binding the forwarded port beyond loopback would expose an otherwise
    // private Montray Server endpoint to other hosts on the local network.
    let loopback = local_host == "localhost"
        || local_host
            .parse::<IpAddr>()
            .is_ok_and(|address| address.is_loopback());
    if !loopback {
        bail!("wsClient.servers[{index}].addr must use a loopback host for an SSH tunnel");
    }
    Ok(())
}

/// Parses a host/port pair without accepting schemes, paths, or bare IPv6.
fn validate_host_port(address: &str) -> Result<(&str, u16)> {
    let (host, port) = if let Some(rest) = address.strip_prefix('[') {
        let (host, port) = rest
            .split_once("]:")
            .context("must be a valid host:port address")?;
        (host, port)
    } else {
        let (host, port) = address
            .rsplit_once(':')
            .context("must be a valid host:port address")?;
        if host.contains(':') {
            bail!("IPv6 addresses must be enclosed in brackets");
        }
        (host, port)
    };
    if host.is_empty() {
        bail!("host must not be empty");
    }
    let port = port
        .parse::<u16>()
        .ok()
        .filter(|port| *port != 0)
        .context("port must be between 1 and 65535")?;
    Ok((host, port))
}

/// Returns the XDG configuration path used when `--config` is absent.
pub fn default_path() -> Result<PathBuf> {
    let directory =
        dirs::config_dir().context("could not determine the user configuration directory")?;
    Ok(directory.join("montray-ui").join("montray-ui.yml"))
}

/// Returns the Montray UI default, falling back to the original Salmon Watch
/// path when an existing user has not created a Montray UI configuration yet.
pub fn runtime_default_path() -> Result<PathBuf> {
    let directory =
        dirs::config_dir().context("could not determine the user configuration directory")?;
    Ok(prefer_canonical_path(
        directory.join("montray-ui").join("montray-ui.yml"),
        directory.join("salmon-watch").join("salmon-watch.yml"),
    ))
}

fn prefer_canonical_path(canonical: PathBuf, former: PathBuf) -> PathBuf {
    if !canonical.exists() && former.exists() {
        former
    } else {
        canonical
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn parse(yaml: &str) -> Result<Config> {
        let config: Config = serde_yaml::from_str(yaml)?;
        config.validate()?;
        Ok(config)
    }

    #[test]
    fn accepts_plain_servers() {
        let config = parse(
            r#"wsClient:
  servers:
    - id: local
      addr: localhost:41990
"#,
        )
        .unwrap();
        assert_eq!(config.ws_client.servers[0].id, "local");
        assert!(config.ws_client.servers[0].tunnel.is_none());
        assert!(config.ws_client.servers[0].tls.is_none());
        assert!(config.ws_client.servers[0].auth.is_none());
    }

    #[test]
    fn rejects_empty_server_list() {
        let error = parse(
            r#"wsClient:
  servers: []
"#,
        )
        .unwrap_err();

        assert_eq!(
            error.to_string(),
            "wsClient.servers must contain at least one server"
        );
    }

    #[test]
    fn accepts_tls_and_bearer_auth() {
        let config = parse(
            r#"wsClient:
  servers:
    - id: remote
      addr: 127.0.0.1:41990
      tls:
        caFile: /etc/montray/ca.pem
        serverName: montray.example.com
      auth:
        bearerTokenFile: /etc/montray/remote.token
"#,
        )
        .unwrap();
        let server = &config.ws_client.servers[0];
        let tls = server.tls.as_ref().unwrap();
        assert_eq!(tls.ca_file, "/etc/montray/ca.pem");
        assert_eq!(tls.server_name, "montray.example.com");
        assert_eq!(
            server.auth.as_ref().unwrap().bearer_token_file,
            "/etc/montray/remote.token"
        );

        let empty_tls = parse(
            r#"wsClient:
  servers:
    - id: remote
      addr: montray.example.com:41990
      tls: {}
"#,
        )
        .unwrap();
        assert!(empty_tls.ws_client.servers[0].tls.is_some());
    }

    #[test]
    fn accepts_ssh_tunnel() {
        let config = parse(
            r#"wsClient:
  servers:
    - id: remote
      addr: 127.0.0.1:42990
      tunnel:
        ssh:
          host: montray.example.com
          user: monitor
          port: 2222
          remoteServerAddr: 127.0.0.1:41990
          extraSshArgs: ['-i', '/tmp/key']
"#,
        )
        .unwrap();
        let ssh = config.ws_client.servers[0]
            .tunnel
            .as_ref()
            .unwrap()
            .ssh
            .as_ref()
            .unwrap();
        assert_eq!(ssh.host, "montray.example.com");
        assert_eq!(ssh.user, "monitor");
        assert_eq!(ssh.port, 2222);
        assert_eq!(ssh.remote_server_addr, "127.0.0.1:41990");
        assert_eq!(ssh.extra_ssh_args, ["-i", "/tmp/key"]);
    }

    #[test]
    fn accepts_legacy_remote_salmon_addr_key() {
        let config = parse(
            r#"wsClient:
  servers:
    - id: remote
      addr: 127.0.0.1:42990
      tunnel:
        ssh:
          host: example.com
          user: monitor
          remoteSalmonAddr: 127.0.0.1:41990
"#,
        )
        .unwrap();

        assert_eq!(
            config.ws_client.servers[0]
                .tunnel
                .as_ref()
                .unwrap()
                .ssh
                .as_ref()
                .unwrap()
                .remote_server_addr,
            "127.0.0.1:41990"
        );
    }

    #[test]
    fn canonical_config_path_wins_over_former_path() {
        let directory = tempfile::tempdir().unwrap();
        let canonical = directory.path().join("montray-ui/montray-ui.yml");
        let former = directory.path().join("salmon-watch/salmon-watch.yml");
        fs::create_dir_all(former.parent().unwrap()).unwrap();
        fs::write(&former, "former").unwrap();
        assert_eq!(
            prefer_canonical_path(canonical.clone(), former.clone()),
            former
        );

        fs::create_dir_all(canonical.parent().unwrap()).unwrap();
        fs::write(&canonical, "canonical").unwrap();
        assert_eq!(prefer_canonical_path(canonical.clone(), former), canonical);
    }

    #[test]
    fn accepts_and_resolves_omitted_ssh_tunnel_address() {
        let mut config = parse(
            r#"wsClient:
  servers:
    - id: remote
      tunnel:
        ssh:
          host: montray.example.com
          user: monitor
          remoteServerAddr: 127.0.0.1:41990
"#,
        )
        .unwrap();
        assert!(config.ws_client.servers[0].addr.is_empty());

        config.resolve_tunnel_addresses().unwrap();

        let address: std::net::SocketAddr = config.ws_client.servers[0].addr.parse().unwrap();
        assert_eq!(address.ip(), std::net::Ipv4Addr::LOCALHOST);
        assert_ne!(address.port(), 0);
    }

    #[test]
    fn rejects_omitted_address_without_structured_ssh_tunnel() {
        for server in [
            r#"- id: direct"#,
            r#"- id: custom
      tunnel:
        customCommand:
          command: [my-tunnel]"#,
        ] {
            let yaml = format!(
                r#"wsClient:
  servers:
    {server}
"#
            );
            let error = parse(&yaml).unwrap_err();
            assert!(
                error
                    .to_string()
                    .contains("addr is required unless tunnel.ssh is configured"),
                "unexpected error: {error:#}"
            );
        }
    }

    #[test]
    fn accepts_custom_tunnel_commands_with_optional_readiness_probe() {
        let config = parse(
            r#"wsClient:
  servers:
    - id: remote
      addr: localhost:42990
      tunnel:
        customCommand:
          command: ['my-tunnel', '--listen', 'localhost:42990']
          readinessProbe:
            containsOutput: READY
    - id: immediate
      addr: localhost:42991
      tunnel:
        customCommand:
          command: ['other-tunnel']
"#,
        )
        .unwrap();
        let custom = config.ws_client.servers[0]
            .tunnel
            .as_ref()
            .unwrap()
            .custom_command
            .as_ref()
            .unwrap();
        assert_eq!(custom.command, ["my-tunnel", "--listen", "localhost:42990"]);
        assert_eq!(
            custom.readiness_probe.as_ref().unwrap().contains_output,
            "READY"
        );
        assert!(
            config.ws_client.servers[1]
                .tunnel
                .as_ref()
                .unwrap()
                .custom_command
                .as_ref()
                .unwrap()
                .readiness_probe
                .is_none()
        );
    }

    #[test]
    fn rejects_invalid_tunnel_adapter_selection() {
        for yaml in [
            r#"wsClient:
  servers:
    - id: local
      addr: localhost:41990
      tunnel: {}
"#,
            r#"wsClient:
  servers:
    - id: local
      addr: localhost:41990
      tunnel:
        unsupported: {}
"#,
            r#"wsClient:
  servers:
    - id: local
      addr: localhost:41990
      tunnel:
        ssh:
          host: host
          user: user
          remoteServerAddr: localhost:1
        customCommand:
          command: [tunnel]
"#,
        ] {
            assert!(
                parse(yaml).is_err(),
                r#"accepted invalid tunnel:
{yaml}"#
            );
        }
    }

    #[test]
    fn rejects_invalid_custom_tunnel_commands() {
        for yaml in [
            r#"wsClient:
  servers:
    - id: remote
      addr: localhost:42990
      tunnel:
        customCommand:
          command: []
"#,
            r#"wsClient:
  servers:
    - id: remote
      addr: localhost:42990
      tunnel:
        customCommand:
          command: ['']
"#,
            r#"wsClient:
  servers:
    - id: remote
      addr: localhost:42990
      tunnel:
        customCommand:
          command: [tunnel]
          readinessProbe:
            containsOutput: ''
"#,
        ] {
            assert!(
                parse(yaml).is_err(),
                r#"accepted invalid custom command:
{yaml}"#
            );
        }
    }

    #[test]
    fn rejects_missing_bearer_token_file() {
        for auth in ["{}", "{ bearerTokenFile: '' }"] {
            let yaml = format!(
                r#"wsClient:
  servers:
    - id: remote
      addr: localhost:41990
      auth: {auth}
"#
            );
            assert!(parse(&yaml).is_err(), "accepted invalid auth: {auth}");
        }
    }

    #[test]
    fn rejects_invalid_ssh_tunnels() {
        for ssh in [
            r#"host: ''
          user: user
          remoteServerAddr: localhost:41990"#,
            r#"host: -option
          user: user
          remoteServerAddr: localhost:41990"#,
            r#"host: host
          user: ''
          remoteServerAddr: localhost:41990"#,
            r#"host: host
          user: -option
          remoteServerAddr: localhost:41990"#,
            r#"host: host
          user: user
          remoteServerAddr: missing-port"#,
            r#"host: host
          user: user
          remoteServerAddr: localhost:0"#,
        ] {
            let yaml = format!(
                r#"wsClient:
  servers:
    - id: remote
      addr: localhost:42990
      tunnel:
        ssh:
          {ssh}
"#
            );
            assert!(
                parse(&yaml).is_err(),
                r#"accepted invalid SSH config:
{ssh}"#
            );
        }

        let non_loopback = r#"wsClient:
  servers:
    - id: remote
      addr: example.com:42990
      tunnel:
        ssh:
          host: host
          user: user
          remoteServerAddr: localhost:41990
"#;
        assert!(parse(non_loopback).is_err());
    }

    #[test]
    fn rejects_invalid_duplicate_and_reserved_ids() {
        for yaml in [
            r#"wsClient:
  servers:
    - { id: 'bad.id', addr: localhost:1 }
"#,
            r#"wsClient:
  servers:
    - { id: internal, addr: localhost:1 }
"#,
            r#"wsClient:
  servers:
    - { id: same, addr: localhost:1 }
    - { id: same, addr: localhost:2 }
"#,
        ] {
            assert!(parse(yaml).is_err());
        }
    }
}
