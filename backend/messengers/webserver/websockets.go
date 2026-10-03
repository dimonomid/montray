package webserver

import (
	"context"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/juju/errors"

	"github.com/dimonomid/montray/v2"
	"github.com/dimonomid/montray/v2/logs"
)

type wsEvent string

// wsTxMsg is a WebSocket event sent by the server to clients.
type wsTxMsg struct {
	Event wsEvent     `json:"event"`
	Data  interface{} `json:"data,omitempty"`
}

const (
	wsEventOngoingIncidentsSnapshot wsEvent = "OngoingIncidentsSnapshot"
	wsEventOngoingIncidentsUpdate   wsEvent = "OngoingIncidentsUpdate"
)

var upgrader = websocket.Upgrader{}

type wsConn struct {
	conn *websocket.Conn
	// logger carries connection-specific context such as the authenticated
	// client ID.
	logger *logs.Logger

	// ctx is a context which is canceled when the client disconnects.
	ctx       context.Context
	ctxCancel context.CancelFunc

	// subID is the subscription ID which should be used to unsubscribe from
	// updates when the connection is disconnected (unless it's 0).
	subID int
}

func (s *Webserver) wsConnect(w http.ResponseWriter, r *http.Request) (resp interface{}, err error) {
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return nil, errors.Trace(err)
	}

	ctx, ctxCancel := context.WithCancel(context.Background())

	conn := &wsConn{
		conn:   c,
		logger: s.params.Common.Logger,

		ctx:       ctx,
		ctxCancel: ctxCancel,
	}
	if clientID := bearerClientID(r.Context()); clientID != "" {
		conn.logger = conn.logger.WithContext("client_id", clientID)
	}

	subID, ch, ok := s.subscribe(conn)
	if !ok {
		conn.ctxCancel()
		_ = conn.conn.Close()
		return nil, nil
	}
	conn.subID = subID

	go s.wsRxLoop(conn)
	go s.wsTxLoop(conn, ch)

	return nil, nil
}

func (s *Webserver) wsRxLoop(conn *wsConn) (err error) {
	defer func() {
		if err != nil {
			select {
			case <-conn.ctx.Done():
				conn.logger.Log(logs.Debug, "WebSocket subscriber %d receive loop stopped", conn.subID)
			default:
				conn.logger.Log(logs.Warning, "WebSocket subscriber %d disconnected: %s", conn.subID, err)
			}
		}

		s.unsubscribe(conn.subID)
	}()
	// This is a server-to-client-only protocol. Receiving any client data
	// message is a protocol violation, so disconnect the client immediately.
	for {
		_, _, err := conn.conn.NextReader()
		if err != nil {
			return errors.Trace(err)
		}

		message := websocket.FormatCloseMessage(
			websocket.ClosePolicyViolation,
			"unsupported data",
		)
		_ = conn.conn.WriteControl(websocket.CloseMessage, message, time.Now().Add(time.Second))
		return errors.New("client sent an unsupported WebSocket message")
	}
}

func (s *Webserver) wsTxLoop(conn *wsConn, notifications <-chan *montray.Notification) {
	defer func() {
		conn.logger.Log(logs.Debug, "WebSocket subscriber %d send loop stopped", conn.subID)
		s.unsubscribe(conn.subID)
	}()

	initialItems := s.params.Common.ItemsBoard.Get()
	if err := conn.conn.WriteJSON(wsTxMsg{
		Event: wsEventOngoingIncidentsSnapshot,
		Data: &montray.Notification{
			Time: time.Now(),
			OngoingIncidents: montray.OngoingIncidentsWDelta{
				Total: initialItems,
			},
		},
	}); err != nil {
		conn.logger.Log(logs.Warning, "Failed to send initial snapshot to WebSocket subscriber %d: %s", conn.subID, err)
		return
	}

	// Create ticker for heartbeats
	heartbeats := time.NewTicker(10 * time.Second)
	defer heartbeats.Stop()

	for {
		select {
		case <-conn.ctx.Done():
			return

		case notif := <-notifications:
			if err := conn.conn.WriteJSON(wsTxMsg{
				Event: wsEventOngoingIncidentsUpdate,
				Data:  notif,
			}); err != nil {
				conn.logger.Log(logs.Warning, "Failed to send incident update to WebSocket subscriber %d: %s", conn.subID, err)
				return
			}

		case <-heartbeats.C:
			if err := conn.conn.WriteMessage(websocket.BinaryMessage, []byte{0}); err != nil {
				conn.logger.Log(logs.Warning, "Failed to send heartbeat to WebSocket subscriber %d: %s", conn.subID, err)
				return
			}
		}
	}
}
