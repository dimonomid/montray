use std::io::BufReader;
use std::sync::Arc;
use std::time::Duration;

use futures_util::{SinkExt, StreamExt};
use montray_ui::config::{AuthConfig, ServerConfig, TlsConfig};
use montray_ui::domain::Event;
use montray_ui::network::run_client;
use tokio::net::TcpListener;
use tokio::sync::{mpsc, watch};
use tokio_tungstenite::tungstenite::Message;

const TEST_CERTIFICATE: &str = include_str!("fixtures/montray-test-cert.pem");
const TEST_PRIVATE_KEY: &str = include_str!("fixtures/montray-test-key.pem");

#[tokio::test(flavor = "current_thread")]
async fn plain_client_reports_connection_snapshot_and_heartbeat() {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let server = tokio::spawn(async move {
        let (stream, _) = listener.accept().await.unwrap();
        let mut socket = tokio_tungstenite::accept_async(stream).await.unwrap();
        socket.send(Message::Text(r#"{"event":"OngoingIncidentsSnapshot","data":{"time":"2026-09-07T10:00:00Z","ongoingIncidents":{"total":[{"key":"disk","state":"error","details":"full","incidentStartedAt":"2026-09-07T09:00:00Z"}],"added":[],"removed":[],"updated":[],"numItemsOK":4}}}"#.into())).await.unwrap();
        socket.send(Message::Binary(vec![0].into())).await.unwrap();
        tokio::time::sleep(Duration::from_secs(1)).await;
    });

    let (events_tx, mut events_rx) = mpsc::channel(8);
    let (shutdown_tx, shutdown_rx) = watch::channel(false);
    let client = tokio::spawn(run_client(
        ServerConfig {
            id: "local".into(),
            addr: address.to_string(),
            tls: None,
            auth: None,
            tunnel: None,
        },
        events_tx,
        shutdown_rx,
    ));

    assert!(
        matches!(next(&mut events_rx).await, Event::Connected { server_id, .. } if server_id == "local")
    );
    assert!(
        matches!(next(&mut events_rx).await, Event::Notification { server_id, data, .. } if server_id == "local" && data.total[0].key == "disk")
    );
    assert!(
        matches!(next(&mut events_rx).await, Event::Heartbeat { server_id, .. } if server_id == "local")
    );

    let _ = shutdown_tx.send(true);
    client.await.unwrap();
    server.await.unwrap();
}

#[tokio::test(flavor = "current_thread")]
async fn malformed_snapshot_reports_the_full_decode_error_chain() {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let server = tokio::spawn(async move {
        let (stream, _) = listener.accept().await.unwrap();
        let mut socket = tokio_tungstenite::accept_async(stream).await.unwrap();
        socket
            .send(Message::Text(
                r#"{"event":"OngoingIncidentsSnapshot","data":{"ongoingIncidents":{"total":"not-an-array"}}}"#
                    .into(),
            ))
            .await
            .unwrap();
    });

    let (events_tx, mut events_rx) = mpsc::channel(8);
    let (shutdown_tx, shutdown_rx) = watch::channel(false);
    let client = tokio::spawn(run_client(
        ServerConfig {
            id: "local".into(),
            addr: address.to_string(),
            tls: None,
            auth: None,
            tunnel: None,
        },
        events_tx,
        shutdown_rx,
    ));

    assert!(matches!(
        next(&mut events_rx).await,
        Event::Connected { .. }
    ));
    let Event::Disconnected { error, .. } = next(&mut events_rx).await else {
        panic!("expected the malformed snapshot to disconnect the client");
    };
    assert!(error.contains("decoding OngoingIncidentsSnapshot data"));
    assert!(error.contains("invalid type: string \"not-an-array\", expected a sequence"));

    let _ = shutdown_tx.send(true);
    client.await.unwrap();
    server.await.unwrap();
}

#[tokio::test(flavor = "current_thread")]
#[allow(clippy::result_large_err)] // Required by tungstenite's header-callback API.
async fn tls_client_uses_custom_ca_server_name_and_bearer_token() {
    let directory = tempfile::tempdir().unwrap();
    let ca_file = directory.path().join("ca.pem");
    let token_file = directory.path().join("client.token");
    std::fs::write(&ca_file, TEST_CERTIFICATE).unwrap();
    std::fs::write(&token_file, "  valid-token\n").unwrap();

    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let acceptor = tls_acceptor();
    let expected_host = address.to_string();
    let server = tokio::spawn(async move {
        let (stream, _) = listener.accept().await.unwrap();
        let stream = acceptor.accept(stream).await.unwrap();
        let mut socket = tokio_tungstenite::accept_hdr_async(
            stream,
            move |
                request: &tokio_tungstenite::tungstenite::handshake::server::Request,
                response: tokio_tungstenite::tungstenite::handshake::server::Response,
            | {
                assert_eq!(request.uri(), "/api/v1/wsconnect");
                assert_eq!(request.headers()["host"], expected_host);
                assert_eq!(request.headers()["authorization"], "Bearer valid-token");
                Ok(response)
            },
        )
        .await
        .unwrap();
        socket.send(Message::Binary(vec![0].into())).await.unwrap();
        let _ = socket.next().await;
    });

    let (events_tx, mut events_rx) = mpsc::channel(8);
    let (shutdown_tx, shutdown_rx) = watch::channel(false);
    let client = tokio::spawn(run_client(
        ServerConfig {
            id: "secure".into(),
            addr: address.to_string(),
            tls: Some(TlsConfig {
                ca_file: ca_file.to_string_lossy().into_owned(),
                server_name: "montray.test".into(),
            }),
            auth: Some(AuthConfig {
                bearer_token_file: token_file.to_string_lossy().into_owned(),
            }),
            tunnel: None,
        },
        events_tx,
        shutdown_rx,
    ));

    let connected = next(&mut events_rx).await;
    assert!(
        matches!(&connected, Event::Connected { server_id, .. } if server_id == "secure"),
        "expected a TLS connection, got {connected:?}"
    );
    assert!(matches!(
        next(&mut events_rx).await,
        Event::Heartbeat { server_id, .. } if server_id == "secure"
    ));

    let _ = shutdown_tx.send(true);
    client.await.unwrap();
    server.await.unwrap();
}

fn tls_acceptor() -> tokio_rustls::TlsAcceptor {
    let certificates = rustls_pemfile::certs(&mut BufReader::new(TEST_CERTIFICATE.as_bytes()))
        .collect::<std::io::Result<Vec<_>>>()
        .unwrap();
    let private_key = rustls_pemfile::private_key(&mut BufReader::new(TEST_PRIVATE_KEY.as_bytes()))
        .unwrap()
        .unwrap();
    let provider = Arc::new(rustls::crypto::ring::default_provider());
    let config = rustls::ServerConfig::builder_with_provider(provider)
        .with_protocol_versions(&[&rustls::version::TLS13, &rustls::version::TLS12])
        .unwrap()
        .with_no_client_auth()
        .with_single_cert(certificates, private_key)
        .unwrap();
    tokio_rustls::TlsAcceptor::from(Arc::new(config))
}

async fn next(events: &mut mpsc::Receiver<Event>) -> Event {
    tokio::time::timeout(Duration::from_secs(3), events.recv())
        .await
        .expect("timed out waiting for client event")
        .expect("client event channel closed")
}
