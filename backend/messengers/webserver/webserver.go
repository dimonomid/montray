package webserver

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/dimonomid/montray"
	"github.com/dimonomid/montray/backend/messengers"
	"github.com/dimonomid/montray/logs"

	"github.com/juju/errors"
	"goji.io"
	"goji.io/pat"
)

type Webserver struct {
	params Params
	// authCredentials contains the compiled authentication methods accepted by
	// the API.
	authCredentials []authCredential

	server   *http.Server
	listener net.Listener

	subs      map[int]chan *montray.Notification
	wsConns   map[int]*wsConn
	nextSubID int
	closing   bool
	subsMtx   sync.Mutex
}

var _ messengers.Messenger = &Webserver{}

type Params struct {
	Common messengers.Params

	Config Config
}

type websocketSubscription struct {
	id   int
	ch   chan *montray.Notification
	conn *wsConn
}

type httpServerErrorWriter struct {
	logger *logs.Logger
}

func (w httpServerErrorWriter) Write(message []byte) (int, error) {
	w.logger.Log(logs.Warning, "%s", strings.TrimSpace(string(message)))
	return len(message), nil
}

const websocketSubscriptionQueueSize = 64

func New(params Params) (*Webserver, error) {
	if params.Common.Logger == nil {
		panic("Logger is required")
	}
	params.Common.Logger = params.Common.Logger.WithNamespaceAppended("Webserver")
	if params.Config.ListenAddress == "" {
		return nil, errors.Errorf("listen address can't be empty")
	}
	authCredentials, err := parseAuthCredentials(params.Config.Auth)
	if err != nil {
		return nil, err
	}
	var certificate *tls.Certificate
	if params.Config.TLS != nil {
		if params.Config.TLS.CertFile == "" {
			return nil, errors.Errorf("tls.certFile can't be empty")
		}
		if params.Config.TLS.KeyFile == "" {
			return nil, errors.Errorf("tls.keyFile can't be empty")
		}
		loadedCertificate, err := tls.LoadX509KeyPair(params.Config.TLS.CertFile, params.Config.TLS.KeyFile)
		if err != nil {
			return nil, errors.Annotate(err, "loading TLS certificate and key")
		}
		certificate = &loadedCertificate
	}

	s := &Webserver{
		params:          params,
		authCredentials: authCredentials,

		subs:    make(map[int]chan *montray.Notification),
		wsConns: make(map[int]*wsConn),
	}

	handler, err := s.createHandler()
	if err != nil {
		return nil, errors.Trace(err)
	}

	server := &http.Server{
		Addr:     params.Config.ListenAddress,
		Handler:  handler,
		ErrorLog: log.New(httpServerErrorWriter{logger: params.Common.Logger}, "", 0),
	}

	listener, err := net.Listen("tcp", params.Config.ListenAddress)
	if err != nil {
		return nil, errors.Annotate(err, "listening")
	}
	if certificate != nil {
		listener = tls.NewListener(listener, &tls.Config{
			Certificates: []tls.Certificate{*certificate},
			MinVersion:   tls.VersionTLS12,
		})
	}

	s.server = server
	s.listener = listener

	go s.serve()
	go s.run()

	if certificate != nil {
		params.Common.Logger.Log(logs.Info, "Listening with TLS on %s", listener.Addr())
	} else {
		params.Common.Logger.Log(logs.Info, "Listening on %s", listener.Addr())
	}

	return s, nil
}

func (s *Webserver) String() string {
	return fmt.Sprintf("webserver on %s", s.listener.Addr())
}

// Addr returns the address of the active listener.
func (s *Webserver) Addr() net.Addr { return s.listener.Addr() }

func (s *Webserver) serve() {
	_ = s.server.Serve(s.listener)

	close(s.params.Common.TornDown)
}

func (s *Webserver) run() {
	for notif := range s.params.Common.NotificationsChan {
		s.subsMtx.Lock()
		subscriptions := make([]websocketSubscription, 0, len(s.subs))
		for id, ch := range s.subs {
			subscriptions = append(subscriptions, websocketSubscription{id: id, ch: ch, conn: s.wsConns[id]})
		}
		s.subsMtx.Unlock()
		for _, sub := range subscriptions {
			select {
			case <-sub.conn.ctx.Done():
				continue
			default:
			}
			if !sendNotificationToSubscriber(sub.ch, notif) {
				sub.conn.logger.Log(logs.Warning, "Disconnecting WebSocket subscriber %d because its notification queue is full", sub.id)
				// Close the connection immediately instead of draining its stale
				// backlog. If the client reconnects, it receives a fresh snapshot.
				s.unsubscribe(sub.id)
			}
		}
	}

	// Input channel was closed, so teardown now
	s.closeWebsocketConnections()
	s.server.Close()
}

// sendNotificationToSubscriber attempts to enqueue notif without blocking.
// It returns false when the subscriber queue cannot accept the notification.
func sendNotificationToSubscriber(ch chan<- *montray.Notification, notif *montray.Notification) bool {
	select {
	case ch <- notif:
		return true
	default:
		return false
	}
}

func (s *Webserver) createHandler() (http.Handler, error) {
	rRoot := goji.NewMux()

	rAPI := goji.SubMux()
	rRoot.Handle(pat.New("/api/v1/*"), rAPI)
	{
		rAPI.Use(makeDesiredContentTypeMiddleware("application/json"))
		if len(s.authCredentials) > 0 {
			rAPI.Use(s.authMiddleware)
		}
		rAPI.HandleFunc(pat.Get("/status"), makeAPIHandlerWWriter(s.params.Common.Logger, s.status))
		rAPI.HandleFunc(pat.Get("/wsconnect"), makeAPIHandlerWWriter(s.params.Common.Logger, s.wsConnect))
	}

	return rRoot, nil
}

func (s *Webserver) status(w http.ResponseWriter, r *http.Request) (resp interface{}, err error) {
	return map[string]interface{}{
		"ongoingIncidents": s.params.Common.ItemsBoard.Get(),
	}, nil
}

func (s *Webserver) subscribe(conn *wsConn) (subID int, ch chan *montray.Notification, ok bool) {
	s.subsMtx.Lock()

	// Shutdown marks closing while holding this same mutex before taking its
	// snapshot of active connections. Rejecting registration here ensures a
	// connection cannot be upgraded concurrently and escape that snapshot.
	if s.closing {
		s.subsMtx.Unlock()
		return 0, nil, false
	}

	subID = s.nextSubID
	s.nextSubID++
	if conn.logger == nil {
		conn.logger = s.params.Common.Logger
	}

	ch = make(chan *montray.Notification, websocketSubscriptionQueueSize)
	s.subs[subID] = ch
	s.wsConns[subID] = conn
	conn.logger.Log(logs.Info, "WebSocket client %d connected from %s", subID, websocketRemoteAddress(conn))
	s.subsMtx.Unlock()

	return subID, ch, true
}

func websocketRemoteAddress(conn *wsConn) string {
	if conn == nil || conn.conn == nil || conn.conn.RemoteAddr() == nil {
		return "unknown address"
	}
	return conn.conn.RemoteAddr().String()
}

// unsubscribe removes a websocket connection from notification fan-out and
// stops all work owned by that subscription. It is called both when an
// individual connection's receive loop ends and when server shutdown closes
// all remaining connections. It is safe to call more than once for the same
// ID: only the call that removes the subscription cancels its context and
// closes its connection. Subscription channels remain open because fan-out may
// have snapshotted one immediately before unsubscription.
func (s *Webserver) unsubscribe(subID int) {
	s.subsMtx.Lock()
	_, exists := s.subs[subID]
	conn := s.wsConns[subID]
	if exists {
		delete(s.subs, subID)
		delete(s.wsConns, subID)
	}
	s.subsMtx.Unlock()
	if exists {
		remoteAddress := websocketRemoteAddress(conn)
		conn.ctxCancel()
		if conn.conn != nil {
			_ = conn.conn.Close()
		}
		conn.logger.Log(logs.Info, "WebSocket client %d disconnected from %s", subID, remoteAddress)
	}
}

func (s *Webserver) closeWebsocketConnections() {
	s.subsMtx.Lock()
	// Close admission before taking the snapshot. A handler that already
	// upgraded its HTTP connection will subsequently be rejected by subscribe.
	s.closing = true
	ids := make([]int, 0, len(s.wsConns))
	for id := range s.wsConns {
		ids = append(ids, id)
	}
	s.subsMtx.Unlock()
	for _, id := range ids {
		s.unsubscribe(id)
	}
}
