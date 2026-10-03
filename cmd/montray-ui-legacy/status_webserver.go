package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/dimonomid/montray"
	"github.com/dimonomid/montray/logs"
)

const defPort = 41991

const statusWebsocketQueueSize = 64

// statusWebserver transports already-classified incident snapshots to the
// local status UI. Classification itself belongs to incidentState.
type statusWebserver struct {
	logger         *logs.Logger
	snapshotMtx    sync.RWMutex
	snapshot       incidentSnapshot
	serverStatuses []serverStatus
	onSnooze       func(string, time.Duration) error
	onUnsnooze     func(string) error
	onForget       func(string) bool

	clientsMtx sync.Mutex
	clients    map[*statusWebsocketClient]struct{}
	closing    bool
}

// statusWebserverParams contains the application callbacks needed by the
// status webserver.
type statusWebserverParams struct {
	Logger *logs.Logger
	// OnSnooze persists a snooze. The incident-state update hook publishes the
	// resulting snapshot to connected status pages.
	OnSnooze func(key string, duration time.Duration) error
	// OnUnsnooze removes a snooze. The incident-state update hook publishes the
	// resulting snapshot to connected status pages.
	OnUnsnooze func(key string) error
	// OnForget drops a stale incident from the source cache and publishes the
	// resulting snapshot.
	OnForget func(key string) bool
}

// localStatusServer owns the HTTP server and listener used by the local status
// UI. Its private handler avoids registrations on http.DefaultServeMux.
type localStatusServer struct {
	server          *http.Server
	listener        net.Listener
	statusWebserver *statusWebserver
	closeOnce       sync.Once
	closeErr        error
}

// statusWebsocketClient has a single websocket writer and a buffered queue of
// complete incident snapshots.
type statusWebsocketClient struct {
	conn     *websocket.Conn
	updates  chan statusWebsocketMessage
	done     chan struct{}
	doneOnce sync.Once
}

func (c *statusWebsocketClient) close() {
	c.doneOnce.Do(func() { close(c.done) })
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

// statusWebsocketMessage is the local browser API payload.
type statusWebsocketMessage struct {
	OngoingIncidents struct {
		Alerting []montray.ItemWContext `json:"alerting"`
		Snoozed  []snoozedIncident      `json:"snoozed"`
	} `json:"ongoingIncidents"`
	Servers []serverStatus `json:"servers"`
}

func newStatusWebserver(params statusWebserverParams) *statusWebserver {
	if params.Logger == nil {
		panic("Logger is required")
	}
	return &statusWebserver{
		logger:     params.Logger.WithNamespaceAppended("StatusWebserver"),
		onSnooze:   params.OnSnooze,
		onUnsnooze: params.OnUnsnooze,
		onForget:   params.OnForget,
		clients:    make(map[*statusWebsocketClient]struct{}),
	}
}

// SetOngoingIncidents stores and publishes a snapshot already classified by
// incidentState.
func (s *statusWebserver) SetOngoingIncidents(snapshot incidentSnapshot) {
	s.snapshotMtx.Lock()
	s.snapshot = snapshot
	s.snapshotMtx.Unlock()
	s.broadcast(s.message())
}

// SetServerStatuses stores and publishes the latest Montray Server connection metadata.
func (s *statusWebserver) SetServerStatuses(statuses []serverStatus) {
	s.snapshotMtx.Lock()
	s.serverStatuses = append([]serverStatus(nil), statuses...)
	s.snapshotMtx.Unlock()
	s.broadcast(s.message())
}

// message constructs the browser API payload from the latest classified
// snapshot.
func (s *statusWebserver) message() statusWebsocketMessage {
	s.snapshotMtx.RLock()
	defer s.snapshotMtx.RUnlock()

	message := statusWebsocketMessage{}
	message.OngoingIncidents.Alerting = append([]montray.ItemWContext(nil), s.snapshot.Alerting...)
	message.OngoingIncidents.Snoozed = append([]snoozedIncident(nil), s.snapshot.Snoozed...)
	message.Servers = append([]serverStatus(nil), s.serverStatuses...)
	return message
}

// snoozeRequest is the JSON body accepted by the snooze endpoint.
type snoozeRequest struct {
	Key      string `json:"key"`
	Duration string `json:"duration"`
}

type incidentKeyRequest struct {
	Key string `json:"key"`
}

func (s *statusWebserver) snooze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var request snoozeRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "invalid snooze request", http.StatusBadRequest)
		return
	}
	duration, ok := snoozeDurations[request.Duration]
	if request.Key == "" || !ok {
		http.Error(w, "invalid snooze key or duration", http.StatusBadRequest)
		return
	}

	if err := s.onSnooze(request.Key, duration); err != nil {
		http.Error(w, "failed to persist snooze", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *statusWebserver) unsnooze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var request incidentKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Key == "" {
		http.Error(w, "invalid unsnooze request", http.StatusBadRequest)
		return
	}

	if err := s.onUnsnooze(request.Key); err != nil {
		http.Error(w, "failed to persist unsnooze", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *statusWebserver) forget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var request incidentKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Key == "" {
		http.Error(w, "invalid forget request", http.StatusBadRequest)
		return
	}

	if s.onForget == nil || !s.onForget(request.Key) {
		http.Error(w, "incident is not stale or no longer exists", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// broadcast sends each snapshot to every connected client. A client whose
// queue is full is disconnected so it cannot delay updates to healthy clients;
// reconnecting gives it a fresh snapshot.
func (s *statusWebserver) broadcast(message statusWebsocketMessage) {
	s.clientsMtx.Lock()
	clients := make([]*statusWebsocketClient, 0, len(s.clients))
	for client := range s.clients {
		clients = append(clients, client)
	}
	s.clientsMtx.Unlock()

	for _, client := range clients {
		select {
		case <-client.done:
			continue
		default:
		}
		if !sendStatusWebsocketUpdate(client.updates, message) {
			s.logger.Log(logs.Warning, "Disconnecting status WebSocket client because its update queue is full")
			// Close the connection immediately instead of draining its stale
			// backlog. If the browser reconnects, it receives a fresh snapshot.
			s.clientsMtx.Lock()
			delete(s.clients, client)
			s.clientsMtx.Unlock()
			client.close()
		}
	}
}

// sendStatusWebsocketUpdate attempts to enqueue message without blocking. It
// returns false when the client queue cannot accept the update.
func sendStatusWebsocketUpdate(ch chan<- statusWebsocketMessage, message statusWebsocketMessage) bool {
	select {
	case ch <- message:
		return true
	default:
		return false
	}
}

// wsConnect upgrades a local status-page connection and streams the initial
// snapshot followed by updates. Incoming messages are ignored; reading them
// only detects browser disconnects.
func (s *statusWebserver) wsConnect(w http.ResponseWriter, r *http.Request) {
	conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
	if err != nil {
		s.logger.Log(logs.Warning, "Failed to upgrade status WebSocket connection: %s", err)
		return
	}

	client := &statusWebsocketClient{
		conn:    conn,
		updates: make(chan statusWebsocketMessage, statusWebsocketQueueSize),
		done:    make(chan struct{}),
	}

	s.clientsMtx.Lock()
	if s.closing {
		s.clientsMtx.Unlock()
		client.close()
		return
	}
	s.clients[client] = struct{}{}
	client.updates <- s.message()
	s.clientsMtx.Unlock()

	defer func() {
		s.clientsMtx.Lock()
		delete(s.clients, client)
		s.clientsMtx.Unlock()
		client.close()
	}()

	go func() {
		for {
			select {
			case message := <-client.updates:
				if err := conn.WriteJSON(message); err != nil {
					client.close()
					return
				}
			case <-client.done:
				return
			}
		}
	}()

	for {
		if _, _, err := conn.NextReader(); err != nil {
			return
		}
	}
}

// closeClients prevents new WebSocket subscriptions and closes all existing
// clients. Connections upgraded concurrently with shutdown are rejected when
// they attempt to register under clientsMtx.
func (s *statusWebserver) closeClients() {
	s.clientsMtx.Lock()
	s.closing = true
	clients := make([]*statusWebsocketClient, 0, len(s.clients))
	for client := range s.clients {
		clients = append(clients, client)
		delete(s.clients, client)
	}
	s.clientsMtx.Unlock()

	for _, client := range clients {
		client.close()
	}
}

// setupWebserver creates the local status HTTP server on the default port
// (defPort). If that port is unavailable, it tries a random port and panics if
// that also fails.
func setupWebserver(statusWebserver *statusWebserver) *localStatusServer {
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", defPort))
	if err != nil {
		statusWebserver.logger.Log(logs.Warning, "Port %d is unavailable (%s); using a random local port", defPort, err)
		listener, err = net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			panic(err.Error())
		}
	}

	return &localStatusServer{
		server: &http.Server{
			Handler: rootHandler(statusWebserver),
		},
		listener:        listener,
		statusWebserver: statusWebserver,
	}
}

func (s *localStatusServer) Addr() net.Addr {
	return s.listener.Addr()
}

func (s *localStatusServer) Serve() error {
	return s.server.Serve(s.listener)
}

func (s *localStatusServer) Close() error {
	s.closeOnce.Do(func() {
		s.statusWebserver.logger.Log(logs.Info, "Shutting down")
		// WebSocket connections are hijacked from net/http, so mark subscription
		// admission closed and drain them explicitly before closing the HTTP server.
		s.statusWebserver.closeClients()
		serverErr := s.server.Close()
		listenerErr := s.listener.Close()
		if serverErr != nil {
			s.closeErr = serverErr
		} else if listenerErr != nil && !errors.Is(listenerErr, net.ErrClosed) {
			s.closeErr = listenerErr
		}
		s.statusWebserver.logger.Log(logs.Info, "Shutdown complete")
	})
	return s.closeErr
}

func rootHandler(statusWebserver *statusWebserver) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", serveStatusPage)
	mux.HandleFunc("/api/v1/wsconnect", statusWebserver.wsConnect)
	mux.HandleFunc("/api/v1/snooze", statusWebserver.snooze)
	mux.HandleFunc("/api/v1/unsnooze", statusWebserver.unsnooze)
	mux.HandleFunc("/api/v1/forget", statusWebserver.forget)
	for _, iconName := range []string{"gray", "green", "magenta", "yellow", "red"} {
		iconAssetPath := fmt.Sprintf("assets/status_%s.png", iconName)
		mux.HandleFunc("/icons/status_"+iconName+".png", func(assetPath string) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				serveIcon(w, r, assetPath)
			}
		}(iconAssetPath))
	}
	mux.Handle("/", noStoreHandler(webrootFileServer()))
	return mux
}

func serveStatusPage(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(embeddedAssets, "assets/webroot/index.html")
	if err != nil {
		http.Error(w, "status page is unavailable", http.StatusInternalServerError)
		return
	}

	setNoStoreHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, "index.html", time.Unix(1, 0), bytes.NewReader(data))
}

// serveIcon exposes an embedded systray icon to the local status UI.
func serveIcon(w http.ResponseWriter, r *http.Request, assetName string) {
	data, err := fs.ReadFile(embeddedAssets, assetName)
	if err != nil {
		http.Error(w, "icon is unavailable", http.StatusInternalServerError)
		return
	}

	setNoStoreHeaders(w)
	w.Header().Set("Content-Type", "image/png")
	http.ServeContent(w, r, assetName, time.Unix(1, 0), bytes.NewReader(data))
}

// noStoreHandler ensures the browser does not keep stale copies of static UI
// assets after Montray UI Legacy is rebuilt with new embedded assets.
func noStoreHandler(inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setNoStoreHeaders(w)
		inner.ServeHTTP(w, r)
	})
}

// setNoStoreHeaders disables caching for the status document and its assets.
func setNoStoreHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
}

func webrootFileServer() http.Handler {
	webroot, err := fs.Sub(embeddedAssets, "assets/webroot")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(webroot))
}
