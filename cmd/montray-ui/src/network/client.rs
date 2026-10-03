use std::fs;
use std::io::BufReader;
use std::sync::Arc;
use std::time::Duration;

use anyhow::{Context, Result, bail};
use futures_util::StreamExt;
use rustls::{ClientConfig, RootCertStore};
use time::OffsetDateTime;
use tokio::net::TcpStream;
use tokio::sync::{mpsc, watch};
use tokio::time::{sleep, timeout};
use tokio_tungstenite::{
    Connector, client_async_tls_with_config,
    tungstenite::{
        Error as WebSocketError, Message,
        client::IntoClientRequest,
        handshake::client::Request,
        http::{HeaderValue, header},
        protocol::WebSocketConfig,
    },
};

use crate::config::{AuthConfig, ServerConfig, TlsConfig};
use crate::domain::Event;

const MAX_MESSAGE_BYTES: usize = 1 << 20;
const CONNECT_TIMEOUT: Duration = Duration::from_secs(30);
const READ_TIMEOUT: Duration = Duration::from_secs(30);
const RECONNECT_STEP: Duration = Duration::from_secs(1);
const MAX_RECONNECT_DELAY: Duration = Duration::from_secs(10);

/// Tunable transport policy kept as a value so tests can exercise timeouts quickly.
#[derive(Clone, Copy)]
struct ClientOptions {
    /// Hard cap for a single decoded WebSocket message and its underlying frame.
    max_message_bytes: usize,
    /// Covers TCP establishment plus TLS and WebSocket handshakes as one attempt.
    connect_timeout: Duration,
    /// Maximum silence after the WebSocket handshake; any received frame resets it.
    read_timeout: Duration,
    /// Linear backoff increment after each failed or lost connection.
    reconnect_step: Duration,
    max_reconnect_delay: Duration,
}

/// Immutable transport material prepared once before reconnection begins.
///
/// Bearer files and CA bundles are intentionally not hot-reloaded. Restart the
/// application after rotating either one.
#[derive(Clone)]
struct ConnectionSetup {
    scheme: &'static str,
    connector: Connector,
    /// Validated complete `Authorization` header value.
    bearer: Option<HeaderValue>,
    /// Host used for URI/SNI/certificate verification while `addr` remains the TCP target.
    tls_server_name: Option<String>,
}

impl ConnectionSetup {
    fn for_server(server: &ServerConfig) -> Result<Self> {
        Ok(Self {
            scheme: if server.tls.is_some() { "wss" } else { "ws" },
            connector: match &server.tls {
                Some(tls) => Connector::Rustls(build_tls_config(tls)?),
                None => Connector::Plain,
            },
            bearer: server.auth.as_ref().map(load_bearer_token).transpose()?,
            tls_server_name: server
                .tls
                .as_ref()
                .and_then(|tls| (!tls.server_name.is_empty()).then(|| tls.server_name.clone())),
        })
    }

    /// Builds the handshake request while separating verification identity from routing.
    ///
    /// With `tls.serverName`, the URI authority drives TLS SNI and certificate
    /// verification, but the HTTP `Host` header remains `server.addr` for parity
    /// with the Go client and deployments that route on the configured endpoint.
    fn request(&self, server: &ServerConfig) -> Result<Request> {
        let authority = match &self.tls_server_name {
            Some(server_name) => {
                let (_, port) = split_host_port(&server.addr)?;
                format!("{}:{port}", uri_host(server_name))
            }
            None => server.addr.clone(),
        };
        let url = format!("{}://{authority}/api/v1/wsconnect", self.scheme);
        let mut request = url
            .as_str()
            .into_client_request()
            .with_context(|| format!("build WebSocket request for {url}"))?;
        request.headers_mut().insert(
            header::HOST,
            HeaderValue::from_str(&server.addr).context("invalid WebSocket Host header")?,
        );
        if let Some(bearer) = &self.bearer {
            request
                .headers_mut()
                .insert(header::AUTHORIZATION, bearer.clone());
        }
        Ok(request)
    }
}

impl Default for ClientOptions {
    fn default() -> Self {
        Self {
            max_message_bytes: MAX_MESSAGE_BYTES,
            connect_timeout: CONNECT_TIMEOUT,
            read_timeout: READ_TIMEOUT,
            reconnect_step: RECONNECT_STEP,
            max_reconnect_delay: MAX_RECONNECT_DELAY,
        }
    }
}

/// Runs the complete transport supervisor for one configured server.
///
/// Tunneled servers delegate lifecycle ownership to the process supervisor,
/// which starts this module's connection loop only after optional readiness.
pub async fn run_client(
    server: ServerConfig,
    events: mpsc::Sender<Event>,
    shutdown: watch::Receiver<bool>,
) {
    if server.tunnel.is_some() {
        super::tunnel::run(server, events, shutdown).await;
    } else {
        run_connection_loop(server, events, shutdown, false).await;
    }
}

/// Reconnects one WebSocket until shutdown or until its owning tunnel disappears.
pub(super) async fn run_connection_loop(
    server: ServerConfig,
    events: mpsc::Sender<Event>,
    shutdown: watch::Receiver<bool>,
    tunneled: bool,
) {
    let server_id = server.id.clone();
    run_connection_loop_with_options(server, events, shutdown, tunneled, ClientOptions::default())
        .await;
    log::info!("server {server_id} WebSocket client stopped");
}

/// Injectable form of the reconnect loop; production uses the conservative
/// defaults while tests shorten deadlines without changing transport logic.
async fn run_connection_loop_with_options(
    server: ServerConfig,
    events: mpsc::Sender<Event>,
    mut shutdown: watch::Receiver<bool>,
    tunneled: bool,
    options: ClientOptions,
) {
    let setup = match ConnectionSetup::for_server(&server) {
        Ok(setup) => setup,
        Err(error) => {
            let error = format!("preparing connection: {error:#}");
            log::error!("server {} {error}", server.id);
            let _ = events
                .send(Event::Disconnected {
                    server_id: server.id,
                    at: wall_clock_now(),
                    error,
                })
                .await;
            return;
        }
    };
    let mut reconnect_delay = Duration::ZERO;
    loop {
        if !reconnect_delay.is_zero() {
            tokio::select! {
                _ = sleep(reconnect_delay) => {}
                _ = shutdown.changed() => return,
            }
        }
        let request = match setup.request(&server) {
            Ok(request) => request,
            Err(error) => {
                let error = format!("preparing connection: {error:#}");
                log::error!("server {} {error}", server.id);
                let _ = events
                    .send(Event::Disconnected {
                        server_id: server.id.clone(),
                        at: wall_clock_now(),
                        error,
                    })
                    .await;
                return;
            }
        };
        log::info!("server {} connecting to {}", server.id, request.uri());
        let connected = tokio::select! {
            result = timeout(
                options.connect_timeout,
                connect(&server, &setup, request, options),
            ) => match result {
                Ok(Ok(connected)) => Ok(connected),
                Ok(Err(error)) => Err(websocket_connection_error(
                    &error,
                    setup.bearer.is_some(),
                )),
                Err(_) => Err(format!(
                    "connection attempt timed out after {} seconds",
                    options.connect_timeout.as_secs(),
                )),
            },
            _ = shutdown.changed() => return,
        };
        let mut socket = match connected {
            Ok((socket, _)) => socket,
            Err(error) => {
                if tunneled && tunnel_stopped(&mut shutdown).await {
                    return;
                }
                log::warn!("server {} connection failed: {error}", server.id);
                let _ = events
                    .send(Event::Disconnected {
                        server_id: server.id.clone(),
                        at: wall_clock_now(),
                        error,
                    })
                    .await;
                reconnect_delay =
                    (reconnect_delay + options.reconnect_step).min(options.max_reconnect_delay);
                continue;
            }
        };
        reconnect_delay = Duration::ZERO;
        log::info!("server {} connected", server.id);
        if events
            .send(Event::Connected {
                server_id: server.id.clone(),
                at: wall_clock_now(),
            })
            .await
            .is_err()
        {
            return;
        }

        // Recreating `timeout` around each `next` means every frame—not only a
        // Montray Server heartbeat—proves liveness and restarts the silence deadline.
        let disconnect_error = loop {
            let next = tokio::select! {
                result = timeout(options.read_timeout, socket.next()) => result,
                _ = shutdown.changed() => return,
            };
            let message = match next {
                Err(_) => {
                    break format!(
                        "no server data received for {} seconds; reconnecting",
                        options.read_timeout.as_secs()
                    );
                }
                Ok(None) => break "WebSocket connection closed".to_owned(),
                Ok(Some(Err(error))) => break error.to_string(),
                Ok(Some(Ok(message))) => message,
            };
            match message {
                Message::Binary(bytes) if bytes.as_ref() == [0] => {
                    if events
                        .send(Event::Heartbeat {
                            server_id: server.id.clone(),
                            at: wall_clock_now(),
                        })
                        .await
                        .is_err()
                    {
                        return;
                    }
                }
                Message::Text(text) => {
                    if text.len() > options.max_message_bytes {
                        break "server message exceeds 1 MiB".to_owned();
                    }
                    match super::decode_text_message(text.as_ref()) {
                        Ok(Some(data)) => {
                            if events
                                .send(Event::Notification {
                                    server_id: server.id.clone(),
                                    data,
                                    at: wall_clock_now(),
                                })
                                .await
                                .is_err()
                            {
                                return;
                            }
                        }
                        Ok(None) => {}
                        Err(error) => break format!("{error:#}"),
                    }
                }
                Message::Binary(bytes) => {
                    let text = match std::str::from_utf8(&bytes) {
                        Ok(text) => text,
                        Err(error) => break format!("decoding server message: {error}"),
                    };
                    match super::decode_text_message(text) {
                        Ok(Some(data)) => {
                            if events
                                .send(Event::Notification {
                                    server_id: server.id.clone(),
                                    data,
                                    at: wall_clock_now(),
                                })
                                .await
                                .is_err()
                            {
                                return;
                            }
                        }
                        Ok(None) => {}
                        Err(error) => break format!("{error:#}"),
                    }
                }
                Message::Close(_) => break "WebSocket connection closed".to_owned(),
                _ => {}
            }
        };
        log::warn!("server {} connection lost: {disconnect_error}", server.id);
        if tunneled && tunnel_stopped(&mut shutdown).await {
            return;
        }
        let _ = events
            .send(Event::Disconnected {
                server_id: server.id.clone(),
                at: wall_clock_now(),
                error: disconnect_error,
            })
            .await;
        reconnect_delay =
            (reconnect_delay + options.reconnect_step).min(options.max_reconnect_delay);
    }
}

/// Establishes TCP and performs the optional TLS plus WebSocket handshake.
///
/// The caller owns the deadline so it covers all phases as a single operation.
async fn connect(
    server: &ServerConfig,
    setup: &ConnectionSetup,
    request: Request,
    options: ClientOptions,
) -> std::result::Result<
    (
        tokio_tungstenite::WebSocketStream<
            tokio_tungstenite::MaybeTlsStream<tokio::net::TcpStream>,
        >,
        tokio_tungstenite::tungstenite::handshake::client::Response,
    ),
    WebSocketError,
> {
    let stream = TcpStream::connect(&server.addr)
        .await
        .map_err(WebSocketError::Io)?;
    client_async_tls_with_config(
        request,
        stream,
        Some(
            WebSocketConfig::default()
                .read_buffer_size(4 * 1024)
                .max_message_size(Some(options.max_message_bytes))
                .max_frame_size(Some(options.max_message_bytes)),
        ),
        Some(setup.connector.clone()),
    )
    .await
}

/// Loads and validates a bearer secret without exposing it in errors or logs.
fn load_bearer_token(auth: &AuthConfig) -> Result<HeaderValue> {
    let contents = fs::read_to_string(&auth.bearer_token_file)
        .with_context(|| format!("read bearer token file {:?}", auth.bearer_token_file))?;
    let token = contents.trim();
    if token.is_empty() {
        bail!("bearer token file {:?} is empty", auth.bearer_token_file);
    }
    if token
        .chars()
        .any(|character| character.is_ascii_whitespace())
    {
        bail!(
            "bearer token file {:?} contains whitespace inside the token",
            auth.bearer_token_file
        );
    }
    HeaderValue::from_str(&format!("Bearer {token}")).with_context(|| {
        format!(
            "bearer token file {:?} is not a valid HTTP token",
            auth.bearer_token_file
        )
    })
}

/// Builds a TLS client using system roots augmented by the optional custom bundle.
///
/// A bad certificate among native roots is warned about individually; an
/// explicitly configured CA file must contribute at least one valid certificate.
fn build_tls_config(tls: &TlsConfig) -> Result<Arc<ClientConfig>> {
    let native = rustls_native_certs::load_native_certs();
    for error in &native.errors {
        log::warn!("failed to load one native root CA certificate: {error}");
    }
    let mut roots = RootCertStore::empty();
    roots.add_parsable_certificates(native.certs);

    if !tls.ca_file.is_empty() {
        let file = fs::File::open(&tls.ca_file)
            .with_context(|| format!("read TLS CA file {:?}", tls.ca_file))?;
        let certificates = rustls_pemfile::certs(&mut BufReader::new(file))
            .collect::<std::io::Result<Vec<_>>>()
            .with_context(|| format!("read TLS CA file {:?}", tls.ca_file))?;
        let (added, _) = roots.add_parsable_certificates(certificates);
        if added == 0 {
            bail!(
                "TLS CA file {:?} contains no valid certificates",
                tls.ca_file
            );
        }
    }
    if roots.is_empty() {
        bail!("load system CA certificates: no trusted certificates were found");
    }

    let provider = Arc::new(rustls::crypto::ring::default_provider());
    let config = ClientConfig::builder_with_provider(provider)
        .with_protocol_versions(&[&rustls::version::TLS13, &rustls::version::TLS12])
        .context("configure TLS 1.2 and TLS 1.3")?
        .with_root_certificates(roots)
        .with_no_client_auth();
    Ok(Arc::new(config))
}

/// Splits the already validated configured address for connection setup.
/// Bracketed IPv6 is supported without carrying brackets into DNS lookup.
fn split_host_port(address: &str) -> Result<(&str, u16)> {
    let (host, port) = if let Some(rest) = address.strip_prefix('[') {
        rest.split_once("]:").context("address must be host:port")?
    } else {
        address
            .rsplit_once(':')
            .context("address must be host:port")?
    };
    let port = port.parse().context("address port is invalid")?;
    Ok((host, port))
}

/// Adds URI brackets to a bare IPv6 host while leaving DNS names unchanged.
fn uri_host(host: &str) -> String {
    if host.contains(':') && !host.starts_with('[') {
        format!("[{host}]")
    } else {
        host.to_owned()
    }
}

/// Turns handshake failures into actionable text without including the bearer
/// value or arbitrary response bodies that might contain sensitive data.
fn websocket_connection_error(error: &WebSocketError, bearer_provided: bool) -> String {
    if let WebSocketError::Http(response) = error {
        if response.status() == tokio_tungstenite::tungstenite::http::StatusCode::UNAUTHORIZED {
            return if bearer_provided {
                "server rejected the configured bearer token".into()
            } else {
                "server requires authentication, but no bearer token is configured".into()
            };
        }
        return format!("WebSocket handshake failed: {}", response.status());
    }
    error.to_string()
}

/// Gives a failing tunneled socket a brief chance to be attributed to SSH.
///
/// The tunnel supervisor sets its private shutdown watch when the SSH process
/// exits. Without this settle window, scheduling order could briefly create both
/// `internal.connection.*` and `internal.tunnel.*` for the same failure.
async fn tunnel_stopped(shutdown: &mut watch::Receiver<bool>) -> bool {
    tokio::select! {
        _ = sleep(Duration::from_millis(50)) => *shutdown.borrow(),
        _ = shutdown.changed() => true,
    }
}

fn wall_clock_now() -> OffsetDateTime {
    OffsetDateTime::now_utc()
}

#[cfg(test)]
mod tests {
    use std::io::Write as _;

    use futures_util::SinkExt;
    use tokio::net::TcpListener;
    use tokio::time::timeout;
    use tokio_tungstenite::accept_async;

    use super::*;

    fn server(address: String) -> ServerConfig {
        ServerConfig {
            id: "test".into(),
            addr: address,
            tls: None,
            auth: None,
            tunnel: None,
        }
    }

    fn fast_options() -> ClientOptions {
        ClientOptions {
            max_message_bytes: MAX_MESSAGE_BYTES,
            connect_timeout: Duration::from_secs(2),
            read_timeout: Duration::from_secs(2),
            reconnect_step: Duration::from_millis(10),
            max_reconnect_delay: Duration::from_millis(20),
        }
    }

    #[test]
    fn bearer_token_is_trimmed_and_added_to_the_request() {
        let mut token_file = tempfile::NamedTempFile::new().unwrap();
        write!(token_file, " \nvalid-token\r\n").unwrap();
        let server = ServerConfig {
            auth: Some(AuthConfig {
                bearer_token_file: token_file.path().to_string_lossy().into_owned(),
            }),
            ..server("127.0.0.1:41990".into())
        };
        let setup = ConnectionSetup::for_server(&server).unwrap();
        let request = setup.request(&server).unwrap();
        assert_eq!(request.uri(), "ws://127.0.0.1:41990/api/v1/wsconnect");
        assert_eq!(request.headers()[header::HOST], "127.0.0.1:41990");
        assert_eq!(
            request.headers()[header::AUTHORIZATION],
            "Bearer valid-token"
        );
    }

    #[test]
    fn bearer_token_rejects_missing_empty_and_internal_whitespace() {
        let directory = tempfile::tempdir().unwrap();
        let missing = AuthConfig {
            bearer_token_file: directory
                .path()
                .join("missing")
                .to_string_lossy()
                .into_owned(),
        };
        assert!(
            load_bearer_token(&missing)
                .unwrap_err()
                .to_string()
                .contains("read bearer token file")
        );

        for (name, contents, expected) in [
            ("empty", " \n\t", "is empty"),
            ("space", "two tokens", "contains whitespace inside"),
            (
                "newline",
                r#"two
tokens"#,
                "contains whitespace inside",
            ),
        ] {
            let filename = directory.path().join(name);
            fs::write(&filename, contents).unwrap();
            let error = load_bearer_token(&AuthConfig {
                bearer_token_file: filename.to_string_lossy().into_owned(),
            })
            .unwrap_err();
            assert!(error.to_string().contains(expected), "{error:#}");
        }
    }

    #[test]
    fn tls_server_name_controls_verification_uri_not_http_host() {
        let setup = ConnectionSetup {
            scheme: "wss",
            connector: Connector::Plain,
            bearer: None,
            tls_server_name: Some("montray.test".into()),
        };
        let server = server("127.0.0.1:41990".into());
        let request = setup.request(&server).unwrap();
        assert_eq!(request.uri(), "wss://montray.test:41990/api/v1/wsconnect");
        assert_eq!(request.headers()[header::HOST], "127.0.0.1:41990");
    }

    #[test]
    fn formats_authentication_handshake_errors_usefully() {
        let unauthorized = WebSocketError::Http(Box::new(
            tokio_tungstenite::tungstenite::http::Response::builder()
                .status(401)
                .body(None)
                .unwrap(),
        ));
        assert_eq!(
            websocket_connection_error(&unauthorized, false),
            "server requires authentication, but no bearer token is configured"
        );
        assert_eq!(
            websocket_connection_error(&unauthorized, true),
            "server rejected the configured bearer token"
        );

        let forbidden = WebSocketError::Http(Box::new(
            tokio_tungstenite::tungstenite::http::Response::builder()
                .status(403)
                .body(None)
                .unwrap(),
        ));
        assert_eq!(
            websocket_connection_error(&forbidden, true),
            "WebSocket handshake failed: 403 Forbidden"
        );
    }

    #[test]
    fn parses_ipv4_hostname_and_ipv6_addresses() {
        assert_eq!(split_host_port("host:41990").unwrap(), ("host", 41990));
        assert_eq!(split_host_port("127.0.0.1:1").unwrap(), ("127.0.0.1", 1));
        assert_eq!(split_host_port("[::1]:443").unwrap(), ("::1", 443));
        assert!(split_host_port("host").is_err());
        assert!(split_host_port("host:nope").is_err());
    }

    async fn next(events: &mut mpsc::Receiver<Event>) -> Event {
        timeout(Duration::from_secs(3), events.recv())
            .await
            .expect("timed out waiting for client event")
            .expect("client event channel closed")
    }

    #[tokio::test(flavor = "current_thread")]
    async fn reconnects_after_the_server_closes_the_first_connection() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap().to_string();
        let server_task = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.unwrap();
            let mut first = accept_async(stream).await.unwrap();
            first.close(None).await.unwrap();

            let (stream, _) = listener.accept().await.unwrap();
            let mut second = accept_async(stream).await.unwrap();
            second.send(Message::Binary(vec![0].into())).await.unwrap();
            let _ = second.next().await;
        });
        let (events_tx, mut events_rx) = mpsc::channel(16);
        let (shutdown_tx, shutdown_rx) = watch::channel(false);
        let client = tokio::spawn(run_connection_loop_with_options(
            server(address),
            events_tx,
            shutdown_rx,
            false,
            fast_options(),
        ));

        assert!(matches!(
            next(&mut events_rx).await,
            Event::Connected { .. }
        ));
        assert!(matches!(
            next(&mut events_rx).await,
            Event::Disconnected { .. }
        ));
        assert!(matches!(
            next(&mut events_rx).await,
            Event::Connected { .. }
        ));
        assert!(matches!(
            next(&mut events_rx).await,
            Event::Heartbeat { .. }
        ));

        shutdown_tx.send(true).unwrap();
        timeout(Duration::from_secs(3), client)
            .await
            .expect("client did not stop")
            .unwrap();
        timeout(Duration::from_secs(3), server_task)
            .await
            .expect("server did not observe client shutdown")
            .unwrap();
    }

    #[tokio::test(flavor = "current_thread")]
    async fn idle_connection_hits_the_read_timeout() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap().to_string();
        let server_task = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.unwrap();
            let mut socket = accept_async(stream).await.unwrap();
            let _ = socket.next().await;
        });
        let (events_tx, mut events_rx) = mpsc::channel(16);
        let (shutdown_tx, shutdown_rx) = watch::channel(false);
        let mut options = fast_options();
        options.read_timeout = Duration::from_millis(30);
        let client = tokio::spawn(run_connection_loop_with_options(
            server(address),
            events_tx,
            shutdown_rx,
            false,
            options,
        ));

        assert!(matches!(
            next(&mut events_rx).await,
            Event::Connected { .. }
        ));
        let Event::Disconnected { error, .. } = next(&mut events_rx).await else {
            panic!("expected read timeout to disconnect the client");
        };
        assert!(error.starts_with("no server data received for "));
        assert!(error.ends_with("; reconnecting"));

        shutdown_tx.send(true).unwrap();
        timeout(Duration::from_secs(3), client)
            .await
            .expect("client did not stop")
            .unwrap();
        timeout(Duration::from_secs(3), server_task)
            .await
            .expect("server did not observe timeout disconnect")
            .unwrap();
    }

    #[tokio::test(flavor = "current_thread")]
    async fn stalled_websocket_handshake_times_out_and_retries() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap().to_string();
        let server_task = tokio::spawn(async move {
            let (first, _) = listener.accept().await.unwrap();
            let (second, _) = timeout(Duration::from_secs(3), listener.accept())
                .await
                .expect("client did not retry after its connection timeout")
                .unwrap();
            drop((first, second));
        });
        let (events_tx, mut events_rx) = mpsc::channel(16);
        let (shutdown_tx, shutdown_rx) = watch::channel(false);
        let mut options = fast_options();
        options.connect_timeout = Duration::from_millis(30);
        let client = tokio::spawn(run_connection_loop_with_options(
            server(address),
            events_tx,
            shutdown_rx,
            false,
            options,
        ));

        let Event::Disconnected { error, .. } = next(&mut events_rx).await else {
            panic!("expected stalled handshake to disconnect the client");
        };
        assert!(error.starts_with("connection attempt timed out after "));
        timeout(Duration::from_secs(3), server_task)
            .await
            .expect("server did not observe reconnect attempt")
            .unwrap();

        shutdown_tx.send(true).unwrap();
        timeout(Duration::from_secs(3), client)
            .await
            .expect("client did not stop")
            .unwrap();
    }

    #[tokio::test(flavor = "current_thread")]
    async fn shutdown_while_connected_is_quiet_and_prompt() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap().to_string();
        let server_task = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.unwrap();
            let mut socket = accept_async(stream).await.unwrap();
            let _ = socket.next().await;
        });
        let (events_tx, mut events_rx) = mpsc::channel(16);
        let (shutdown_tx, shutdown_rx) = watch::channel(false);
        let client = tokio::spawn(run_connection_loop_with_options(
            server(address),
            events_tx,
            shutdown_rx,
            false,
            fast_options(),
        ));

        assert!(matches!(
            next(&mut events_rx).await,
            Event::Connected { .. }
        ));
        shutdown_tx.send(true).unwrap();
        timeout(Duration::from_millis(250), client)
            .await
            .expect("client did not stop promptly")
            .unwrap();
        assert!(
            events_rx.try_recv().is_err(),
            "normal shutdown emitted a disconnect event"
        );
        timeout(Duration::from_secs(3), server_task)
            .await
            .expect("server did not observe client shutdown")
            .unwrap();
    }

    #[tokio::test(flavor = "current_thread")]
    async fn oversized_message_disconnects_the_client() {
        const LIMIT: usize = 64;
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap().to_string();
        let server_task = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.unwrap();
            let mut socket = accept_async(stream).await.unwrap();
            socket
                .send(Message::Text("x".repeat(LIMIT + 1).into()))
                .await
                .unwrap();
        });
        let (events_tx, mut events_rx) = mpsc::channel(16);
        let (shutdown_tx, shutdown_rx) = watch::channel(false);
        let mut options = fast_options();
        options.max_message_bytes = LIMIT;
        let client = tokio::spawn(run_connection_loop_with_options(
            server(address),
            events_tx,
            shutdown_rx,
            false,
            options,
        ));

        assert!(matches!(
            next(&mut events_rx).await,
            Event::Connected { .. }
        ));
        let Event::Disconnected { error, .. } = next(&mut events_rx).await else {
            panic!("expected oversized message to disconnect the client");
        };
        let error = error.to_ascii_lowercase();
        assert!(
            error.contains("too long")
                || error.contains("capacity")
                || error.contains("size limit"),
            "unexpected oversized-message error: {error}"
        );

        shutdown_tx.send(true).unwrap();
        timeout(Duration::from_secs(3), client)
            .await
            .expect("client did not stop")
            .unwrap();
        server_task.await.unwrap();
    }
}
