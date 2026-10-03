package wsclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/dimonomid/montray"
	"github.com/dimonomid/montray/logs"

	"github.com/gorilla/websocket"
)

type wsMsgServer struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data,omitempty"`
}

const (
	// maxServerMessageBytes bounds memory used to receive a single message from
	// a Montray server. Heartbeats and incident notifications are expected to be
	// much smaller than this.
	maxServerMessageBytes = 1 << 20

	// heartbeatPeriod is how often the server is expected to send heartbeats
	heartbeatPeriod = 10 * time.Second

	// maxNumHeartbeatPeriodsUntilDisconnect is how many heartbeat periods may
	// pass before a silent Montray connection, or its SSH tunnel, is considered
	// dead.
	maxNumHeartbeatPeriodsUntilDisconnect = 3

	// readTimeout specifies how long to wait for any data received from the
	// server before disconnecting.
	readTimeout = heartbeatPeriod * maxNumHeartbeatPeriodsUntilDisconnect

	// tunnelFailureSettleDelay gives the tunnel supervisor time to mark its
	// process unavailable when a tunnel failure and the resulting WebSocket
	// error are observed in the opposite order. Without this delay, the
	// WebSocket error could briefly be reported as a separate connection
	// incident. We cannot wait indefinitely for the tunnel to fail because the
	// same WebSocket error can be caused by Montray becoming unavailable while
	// the tunnel process remains healthy.
	tunnelFailureSettleDelay = 50 * time.Millisecond

	// reconnectDelayStep is added after each consecutive failed connection
	// attempt.
	reconnectDelayStep = time.Second

	// maxReconnectDelay caps the linear connection retry backoff.
	maxReconnectDelay = 10 * time.Second
)

type WSClient struct {
	params Params
	// dialer contains the TLS trust configuration for this server.
	dialer *websocket.Dialer
	// scheme is ws for plain connections and wss when TLS is enabled.
	scheme string
	// requestHeader carries optional authentication for the WebSocket upgrade.
	requestHeader http.Header
	// interrupt is closed to stop dialing, reconnect delays, and the active
	// connection.
	interrupt chan struct{}
	// cancel interrupts an in-progress WebSocket dial.
	cancel context.CancelFunc
	// done is closed after the connection worker exits.
	done chan struct{}
	// closeOnce makes Close safe to call from multiple cleanup paths.
	closeOnce sync.Once
}

// websocketDialerConfig contains the dialer and URL scheme selected for one
// server's plain or TLS WebSocket connection.
type websocketDialerConfig struct {
	// dialer carries the server's TLS trust and hostname-verification settings.
	dialer *websocket.Dialer

	// scheme is "ws" for a plain connection or "wss" for a TLS connection.
	scheme string
}

type Params struct {
	Config ConfigServer
	Logger *logs.Logger

	// EventCh receives every event for this server in observation order.
	EventCh chan<- ServerEvent
	// ReconnectDelay overrides each production reconnect delay when non-zero.
	ReconnectDelay time.Duration
	// Tunnel is optional; if provided, it delays connection attempts until the
	// server's tunnel is ready.
	Tunnel *TunnelSupervisor
}

// ServerEventKind identifies the payload carried by a ServerEvent.
type ServerEventKind string

const (
	// ServerEventKindOngoingIncidents carries an incident notification.
	ServerEventKindOngoingIncidents ServerEventKind = "ongoing incidents"
	// ServerEventKindConnectionError carries the current connection error.
	ServerEventKindConnectionError ServerEventKind = "connection error"
	// ServerEventKindConnection carries a connection transition or heartbeat.
	ServerEventKindConnection ServerEventKind = "connection"
	// ServerEventKindTunnel carries a tunnel lifecycle event.
	ServerEventKindTunnel ServerEventKind = "tunnel"
)

// ServerEvent is one event in the ordered lifecycle of a configured server.
// Exactly one payload field is meaningful according to Kind.
type ServerEvent struct {
	// Kind identifies the event payload.
	Kind ServerEventKind
	// OngoingIncidents contains the notification for an incident event.
	OngoingIncidents *montray.Notification
	// ConnectionError contains the current error for a connection-error event;
	// an empty value resolves the corresponding internal incident.
	ConnectionError string
	// Connection contains the transition or heartbeat for a connection event.
	Connection ConnectionEvent
	// Tunnel contains the lifecycle change for a tunnel event.
	Tunnel TunnelEvent
}

// ConnectionEventKind identifies the kind of connection event.
type ConnectionEventKind string

const (
	EventKindConnected    ConnectionEventKind = "connected"
	EventKindDisconnected ConnectionEventKind = "disconnected"
	EventKindHeartbeat    ConnectionEventKind = "heartbeat"
)

// ConnectionEvent describes a Montray connection transition or heartbeat.
type ConnectionEvent struct {
	EventKind ConnectionEventKind
	Time      time.Time
}

func New(params Params) (*WSClient, error) {
	if params.Logger == nil {
		panic("Logger is required")
	}
	if params.EventCh == nil {
		panic("EventCh is required")
	}
	params.Logger = params.Logger.WithNamespaceAppended("WSClient")
	dialerConfig, err := websocketDialer(params.Config)
	if err != nil {
		return nil, err
	}
	requestHeader, err := websocketRequestHeader(params.Config)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &WSClient{
		params:        params,
		dialer:        dialerConfig.dialer,
		scheme:        dialerConfig.scheme,
		requestHeader: requestHeader,
		interrupt:     make(chan struct{}),
		cancel:        cancel,
		done:          make(chan struct{}),
	}

	go c.eventLoop(ctx)

	return c, nil
}

// websocketRequestHeader loads the optional bearer token before the client's
// connection worker starts.
func websocketRequestHeader(config ConfigServer) (http.Header, error) {
	if config.Auth == nil {
		return nil, nil
	}
	if config.Auth.BearerTokenFile == "" {
		return nil, fmt.Errorf("auth.bearerTokenFile is required")
	}
	contents, err := os.ReadFile(config.Auth.BearerTokenFile)
	if err != nil {
		return nil, fmt.Errorf("read bearer token file %q: %w", config.Auth.BearerTokenFile, err)
	}
	token := strings.TrimSpace(string(contents))
	if token == "" {
		return nil, fmt.Errorf("bearer token file %q is empty", config.Auth.BearerTokenFile)
	}
	if strings.ContainsAny(token, " \t\r\n") {
		return nil, fmt.Errorf("bearer token file %q contains whitespace inside the token", config.Auth.BearerTokenFile)
	}
	return http.Header{"Authorization": []string{"Bearer " + token}}, nil
}

// websocketDialer creates an independent dialer and loads any additional CA
// certificates before the client's connection worker starts.
func websocketDialer(config ConfigServer) (websocketDialerConfig, error) {
	dialer := *websocket.DefaultDialer
	if config.TLS == nil {
		return websocketDialerConfig{dialer: &dialer, scheme: "ws"}, nil
	}

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: config.TLS.ServerName,
	}
	if config.TLS.CAFile != "" {
		rootCAs, err := x509.SystemCertPool()
		if err != nil {
			return websocketDialerConfig{}, fmt.Errorf("load system CA certificates: %w", err)
		}
		if rootCAs == nil {
			rootCAs = x509.NewCertPool()
		}
		caPEM, err := os.ReadFile(config.TLS.CAFile)
		if err != nil {
			return websocketDialerConfig{}, fmt.Errorf("read TLS CA file %q: %w", config.TLS.CAFile, err)
		}
		if !rootCAs.AppendCertsFromPEM(caPEM) {
			return websocketDialerConfig{}, fmt.Errorf("TLS CA file %q contains no valid certificates", config.TLS.CAFile)
		}
		tlsConfig.RootCAs = rootCAs
	}
	dialer.TLSClientConfig = tlsConfig
	return websocketDialerConfig{dialer: &dialer, scheme: "wss"}, nil
}

// Close stops the client and waits for its connection worker to exit.
func (c *WSClient) Close() {
	c.closeOnce.Do(func() {
		c.params.Logger.Log(logs.Info, "Shutting down")
		close(c.interrupt)
		c.cancel()
		<-c.done
		c.params.Logger.Log(logs.Info, "Shutdown complete")
	})
}

func (c *WSClient) eventLoop(ctx context.Context) {
	defer close(c.done)
	connError := ""
	reconnectDelay := time.Duration(0)

mainLoop:
	for {
		tunnelWasReady := true
		if c.params.Tunnel != nil {
			tunnelWasReady = c.params.Tunnel.IsReady()
			if !c.params.Tunnel.WaitReady(c.interrupt) {
				return
			}
			if !tunnelWasReady {
				reconnectDelay = 0
			}
		}
		if !c.sendConnectionError(connError) {
			return
		}

		if reconnectDelay > 0 && tunnelWasReady {
			delay := reconnectDelay
			if c.params.ReconnectDelay > 0 {
				delay = c.params.ReconnectDelay
			}
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-c.interrupt:
				if !timer.Stop() {
					<-timer.C
				}
				return
			}
		}

		u := url.URL{
			Scheme: c.scheme,
			Host:   c.params.Config.Addr,
			Path:   "/api/v1/wsconnect",
		}

		ustr := u.String()

		c.params.Logger.Log(logs.Info, "Connecting to %s", ustr)
		conn, response, err := c.dialer.DialContext(ctx, ustr, c.requestHeader)
		if err != nil {
			connError = websocketConnectionError(err, response, c.requestHeader.Get("Authorization") != "")
			if response != nil && response.Body != nil {
				_ = response.Body.Close()
			}
			if c.tunnelUnavailable() {
				connError = ""
				reconnectDelay = 0
				c.params.Logger.Log(logs.Debug, "Connection is waiting for its tunnel")
				continue mainLoop
			}
			reconnectDelay = nextReconnectDelay(reconnectDelay)
			if !c.sendConnectionEvent(ConnectionEvent{EventKind: EventKindDisconnected, Time: time.Now()}) {
				return
			}
			c.params.Logger.Log(logs.Warning, "Failed to connect to %s: %s", ustr, connError)
			continue mainLoop
		}
		conn.SetReadLimit(maxServerMessageBytes)
		reconnectDelay = 0

		c.params.Logger.Log(logs.Info, "Connected to %s", ustr)
		if !c.sendConnectionEvent(ConnectionEvent{EventKind: EventKindConnected, Time: time.Now()}) {
			_ = conn.Close()
			return
		}

		connError = ""
		if !c.sendConnectionError(connError) {
			_ = conn.Close()
			return
		}

		disconnected := make(chan struct{})

		readTimer := time.AfterFunc(readTimeout, func() {
			// We haven't heard anything from the server for too long, so,
			// disconnect.
			//
			// NOTE that we don't use the c.Close() method: it's quite likely that
			// there is some problem with the network, and c.Close() first tries to
			// send the websocket close message, which would take quite some time
			// before timing out. Instead we just close the ws connection
			// forcefully, thus immediately breaking out of recvLoop,
			// so the clients will notice the state change right away.
			conn.Close()
		})

		go func() {
			defer func() {
				close(disconnected)
			}()
			disconnectWithError := func(err error) {
				connError = err.Error()
				c.sendConnectionEvent(ConnectionEvent{EventKind: EventKindDisconnected, Time: time.Now()})
				c.params.Logger.Log(logs.Error, "Invalid message: %s", err)
				_ = conn.Close()
			}
			for {
				_, message, err := conn.ReadMessage()
				if err != nil {
					if c.tunnelUnavailable() {
						connError = ""
						c.params.Logger.Log(logs.Debug, "Connection closed with its tunnel")
						return
					}
					connError = err.Error()
					c.sendConnectionEvent(ConnectionEvent{EventKind: EventKindDisconnected, Time: time.Now()})
					select {
					case <-c.interrupt:
						c.params.Logger.Log(logs.Debug, "Connection closed")
					default:
						c.params.Logger.Log(logs.Warning, "Connection was lost: %s", err)
					}
					return
				}

				// Just received something from the server: reset the read timeout. We
				// don't bother to check whether the timer has already fired: if so,
				// we'll reconnect anyway.
				readTimer.Reset(readTimeout)

				if len(message) == 1 && message[0] == 0x00 {
					// Heartbeat
					if !c.sendConnectionEvent(ConnectionEvent{EventKind: EventKindHeartbeat, Time: time.Now()}) {
						return
					}
					c.params.Logger.Log(logs.Debug, "Received heartbeat")
					continue
				}

				var msgServer wsMsgServer
				if err := json.Unmarshal(message, &msgServer); err != nil {
					disconnectWithError(fmt.Errorf("decoding server message: %w", err))
					return
				}

				switch msgServer.Event {
				case "OngoingIncidentsSnapshot", "OngoingIncidentsUpdate":
					var notif *montray.Notification

					if err := json.Unmarshal(msgServer.Data, &notif); err != nil {
						disconnectWithError(fmt.Errorf("decoding %s data: %w", msgServer.Event, err))
						return
					}
					if notif == nil {
						disconnectWithError(fmt.Errorf("decoding %s data: notification is null", msgServer.Event))
						return
					}
					if err := validateNotification(notif); err != nil {
						disconnectWithError(fmt.Errorf("validating %s data: %w", msgServer.Event, err))
						return
					}
					updateKind := "update"
					if msgServer.Event == "OngoingIncidentsSnapshot" {
						updateKind = "snapshot"
					}
					total, err := json.Marshal(notif.OngoingIncidents.Total)
					if err != nil {
						c.params.Logger.Log(logs.Error, "Failed to format incident %s for logging: %s",
							updateKind, err)
					} else {
						c.params.Logger.Log(logs.Info, "Received incident %s; ongoing incidents: %s",
							updateKind, total)
					}

					if !c.sendOngoingIncidents(notif) {
						return
					}
				default:
					c.params.Logger.Log(logs.Warning, "Ignoring unsupported event %q", msgServer.Event)
				}
			}
		}()

		for {
			select {

			case <-disconnected:
				readTimer.Stop()
				_ = conn.Close()
				reconnectDelay = nextReconnectDelay(reconnectDelay)
				continue mainLoop

			case <-c.interrupt:
				c.params.Logger.Log(logs.Debug, "Closing connection")
				readTimer.Stop()
				_ = conn.Close()
				<-disconnected
				return
			}
		}
	}
}

// nextReconnectDelay advances the linear retry backoff by one step without
// exceeding its maximum.
func nextReconnectDelay(current time.Duration) time.Duration {
	if current >= maxReconnectDelay-reconnectDelayStep {
		return maxReconnectDelay
	}
	return current + reconnectDelayStep
}

// websocketConnectionError describes HTTP handshake failures more usefully
// than the WebSocket library's generic "bad handshake" error.
func websocketConnectionError(err error, response *http.Response, bearerTokenProvided bool) string {
	if response == nil {
		return err.Error()
	}
	if response.StatusCode == http.StatusUnauthorized {
		if bearerTokenProvided {
			return "server rejected the configured bearer token"
		}
		return "server requires authentication, but no bearer token is configured"
	}
	return fmt.Sprintf("WebSocket handshake failed: %s", response.Status)
}

func (c *WSClient) tunnelUnavailable() bool {
	if c.params.Tunnel == nil {
		return false
	}
	timer := time.NewTimer(tunnelFailureSettleDelay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return !c.params.Tunnel.IsReady()
	case <-c.interrupt:
		return true
	}
}

func validateNotification(notif *montray.Notification) error {
	if notif == nil {
		return fmt.Errorf("notification is null")
	}
	if notif.OngoingIncidents.NumItemsOK < 0 {
		return fmt.Errorf("ongoingIncidents.numItemsOK is negative")
	}

	lists := []struct {
		name  string
		items []*montray.ItemWContext
	}{
		{name: "total", items: notif.OngoingIncidents.Total},
		{name: "added", items: notif.OngoingIncidents.Added},
		{name: "removed", items: notif.OngoingIncidents.Removed},
		{name: "updated", items: notif.OngoingIncidents.Updated},
	}
	for _, list := range lists {
		for i, item := range list.items {
			field := fmt.Sprintf("ongoingIncidents.%s[%d]", list.name, i)
			if item == nil {
				return fmt.Errorf("%s is null", field)
			}
			if item.Key == "" {
				return fmt.Errorf("%s.key is empty", field)
			}
			if !montray.IsItemStateValid(item.State) {
				return fmt.Errorf("%s.state %q is invalid", field, item.State)
			}
		}
	}

	return nil
}

func (c *WSClient) sendOngoingIncidents(notif *montray.Notification) bool {
	return c.sendServerEvent(ServerEvent{
		Kind:             ServerEventKindOngoingIncidents,
		OngoingIncidents: notif,
	})
}

func (c *WSClient) sendConnectionError(err string) bool {
	return c.sendServerEvent(ServerEvent{
		Kind:            ServerEventKindConnectionError,
		ConnectionError: err,
	})
}

func (c *WSClient) sendConnectionEvent(event ConnectionEvent) bool {
	return c.sendServerEvent(ServerEvent{
		Kind:       ServerEventKindConnection,
		Connection: event,
	})
}

func (c *WSClient) sendServerEvent(event ServerEvent) bool {
	select {
	case c.params.EventCh <- event:
		return true
	case <-c.interrupt:
		return false
	default:
	}

	started := time.Now()
	c.params.Logger.Log(logs.Warning, "%s event delivery is blocked; waiting for the consumer", event.Kind)
	select {
	case c.params.EventCh <- event:
		c.params.Logger.Log(logs.Warning, "%s event delivery resumed after %s", event.Kind, time.Since(started))
		return true
	case <-c.interrupt:
		return false
	}
}
