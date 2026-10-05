package main

import (
	"sync"
	"time"

	"github.com/dimonomid/clock"

	"github.com/dimonomid/montray/v2"
	"github.com/dimonomid/montray/v2/logs"
	"github.com/dimonomid/montray/v2/wsclient"
)

// montrayLegacyCore contains Montray UI Legacy's event processing without any systray or
// process-lifecycle concerns, making it usable from end-to-end tests.
type montrayLegacyCore struct {
	logger          *logs.Logger
	incidentState   *incidentState
	statusWebserver *statusWebserver
	notifications   notificator
	onIconState     func(trayState)
	// combiner owns all outbound Montray Server connections and is closed with the core.
	combiner          *wsclient.Combiner
	closeOnce         sync.Once
	serverStatusesMtx sync.RWMutex
	serverStatuses    map[string]serverStatus
	// serverIDs preserves the order of servers in the Montray UI configuration.
	serverIDs []string
}

type serverStatus struct {
	// ID identifies the configured Montray Server.
	ID string `json:"id"`
	// Connected reports the latest connection state.
	Connected bool `json:"connected"`
	// ConnectionChangedAt is when the connection most recently changed state.
	ConnectionChangedAt *time.Time `json:"connectionChangedAt,omitempty"`
	// LastHeartbeatTime is when the most recent heartbeat was received.
	LastHeartbeatTime *time.Time `json:"lastHeartbeatTime,omitempty"`
	// hasConnectedOrFailed becomes true after the server first connects or fails
	// to connect, and stays true for the rest of this run.
	hasConnectedOrFailed bool `json:"-"`
}

// trayState is the already-aggregated incident and server-initialization
// information needed by the tray UI. Snoozed is nil when no snoozed incidents
// need an icon overlay.
type trayState struct {
	Alerting overallState
	// UnknownServerCount is the number of configured servers that have neither
	// connected nor failed yet during this run.
	UnknownServerCount int
	// ServerCount is the total number of configured servers.
	ServerCount   int
	AlertingCount int
	Snoozed       *overallState
	SnoozedCount  int
}

type montrayLegacyCoreParams struct {
	// Config describes the Montray Servers that Montray UI Legacy should connect to.
	Config wsclient.Config
	// StatePath is the location of the persisted snooze state file.
	StatePath string
	// Notifications receives desktop notification events.
	Notifications notificator
	// OnIconState applies the aggregated incident state to the tray UI.
	OnIconState func(trayState)
	// Clock supplies time to all incident and snooze state; it is required.
	Clock  clock.Clock
	Logger *logs.Logger
	// ReconnectDelay overrides the production reconnect backoff when non-zero.
	ReconnectDelay time.Duration
	// SnoozeCheckInterval overrides the production 10*time.Second snooze
	// expiration polling interval when non-zero.
	SnoozeCheckInterval time.Duration
}

func newMontrayLegacyCore(params montrayLegacyCoreParams) (*montrayLegacyCore, error) {
	if params.Clock == nil {
		panic("Clock is required")
	}
	if params.Logger == nil {
		panic("Logger is required")
	}
	params.Logger = params.Logger.WithNamespaceAppended("Core")
	if err := params.Config.Validate(); err != nil {
		return nil, err
	}

	var incidentState *incidentState
	var err error
	if params.SnoozeCheckInterval > 0 {
		incidentState, err = newIncidentStateWithInterval(params.StatePath, params.SnoozeCheckInterval, params.Clock, params.Logger)
	} else {
		incidentState, err = newIncidentState(params.StatePath, params.Clock, params.Logger)
	}
	if err != nil {
		return nil, err
	}

	core := &montrayLegacyCore{
		logger:         params.Logger,
		incidentState:  incidentState,
		notifications:  params.Notifications,
		onIconState:    params.OnIconState,
		serverStatuses: make(map[string]serverStatus, len(params.Config.Servers)),
		serverIDs:      make([]string, 0, len(params.Config.Servers)),
	}
	for _, server := range params.Config.Servers {
		core.serverStatuses[server.ID] = serverStatus{ID: server.ID}
		core.serverIDs = append(core.serverIDs, server.ID)
	}
	core.statusWebserver = newStatusWebserver(statusWebserverParams{
		OnSnooze:   incidentState.Snooze,
		OnUnsnooze: incidentState.Unsnooze,
		OnForget:   core.forgetStaleIncident,
		Logger:     params.Logger,
	})
	core.publishServerStatuses()
	incidentState.OnUpdate = core.onIncidentUpdate
	incidentState.OnSnoozeExpired = core.onSnoozeExpired

	core.combiner, err = wsclient.NewCombiner(wsclient.CombinerParams{
		Config:                  params.Config,
		Logger:                  params.Logger,
		OngoingIncidentsHandler: core.onNotification,
		Clock:                   params.Clock,
		ReconnectDelay:          params.ReconnectDelay,
		ConnectionStatusHandler: core.onConnectionEvent,
	})
	if err != nil {
		incidentState.Close()
		return nil, err
	}
	return core, nil
}

func (c *montrayLegacyCore) onSnoozeExpired(item montray.ItemWContext) {
	if c.notifications != nil {
		c.notifications.Push("Snooze ended: "+string(item.Key), item.Details)
	}
}

func (c *montrayLegacyCore) forgetStaleIncident(key string) bool {
	if c.combiner == nil {
		return false
	}
	return c.combiner.ForgetStaleIncident(key)
}

func (c *montrayLegacyCore) onConnectionEvent(id string, event wsclient.ConnectionEvent) {
	c.serverStatusesMtx.Lock()
	status := c.serverStatuses[id]
	if event.EventKind == wsclient.EventKindHeartbeat {
		now := event.Time
		status.LastHeartbeatTime = &now
	} else if event.EventKind == wsclient.EventKindConnected || event.EventKind == wsclient.EventKindDisconnected {
		now := event.Time
		connected := event.EventKind == wsclient.EventKindConnected
		if status.hasConnectedOrFailed && connected == status.Connected {
			c.serverStatusesMtx.Unlock()
			return
		}
		status.Connected = connected
		status.hasConnectedOrFailed = true
		status.ConnectionChangedAt = &now
		if connected {
			status.LastHeartbeatTime = nil
		}
	}
	c.serverStatuses[id] = status
	c.serverStatusesMtx.Unlock()
	c.publishServerStatuses()
	if event.EventKind == wsclient.EventKindConnected {
		c.onIncidentUpdate(c.incidentState.snapshot())
	}
}

func (c *montrayLegacyCore) publishServerStatuses() {
	c.serverStatusesMtx.RLock()
	statuses := make([]serverStatus, 0, len(c.serverIDs))
	// Iterate by configured ID rather than over serverStatuses: Go map iteration
	// order is deliberately unstable, while the status UI shows this order.
	for _, id := range c.serverIDs {
		statuses = append(statuses, c.serverStatuses[id])
	}
	c.serverStatusesMtx.RUnlock()
	c.statusWebserver.SetServerStatuses(statuses)
}

// Close stops Montray UI Legacy's workers and waits for them to exit.
func (c *montrayLegacyCore) Close() {
	c.closeOnce.Do(func() {
		c.logger.Log(logs.Info, "Shutting down")
		if c.combiner != nil {
			c.combiner.Close()
		}
		if c.incidentState != nil {
			c.incidentState.Close()
		}
		c.logger.Log(logs.Info, "Shutdown complete")
	})
}

func (c *montrayLegacyCore) onIncidentUpdate(snapshot incidentSnapshot) {
	c.statusWebserver.SetOngoingIncidents(snapshot)
	if c.onIconState != nil {
		unknownServerCount, serverCount := c.getServerInitializationCounts()
		state := trayState{
			Alerting:           getAlertingOverallStateFromItems(snapshot.Alerting, unknownServerCount),
			UnknownServerCount: unknownServerCount,
			ServerCount:        serverCount,
			AlertingCount:      len(snapshot.Alerting),
			SnoozedCount:       len(snapshot.Snoozed),
		}
		if len(snapshot.Snoozed) > 0 {
			snoozed := getOverallStateFromItems(snapshot.SnoozedItems())
			state.Snoozed = &snoozed
		}
		c.onIconState(state)
	}
}

// getAlertingOverallStateFromItems keeps an otherwise healthy state unknown
// while at least one configured server has neither connected nor failed.
func getAlertingOverallStateFromItems(items []montray.ItemWContext, unknownServerCount int) overallState {
	state := getOverallStateFromItems(items)
	if state == overallStateOK && unknownServerCount > 0 {
		return overallStateUnknown
	}
	return state
}

// getServerInitializationCounts returns the pending and total configured
// server counts from one consistent status snapshot.
func (c *montrayLegacyCore) getServerInitializationCounts() (unknown, total int) {
	c.serverStatusesMtx.RLock()
	defer c.serverStatusesMtx.RUnlock()
	total = len(c.serverStatuses)
	for _, status := range c.serverStatuses {
		if !status.hasConnectedOrFailed {
			unknown++
		}
	}
	return unknown, total
}

func (c *montrayLegacyCore) onNotification(notif *montray.Notification) {
	snapshot := c.incidentState.Update(notif.OngoingIncidents.Total)

	if c.notifications != nil {
		for _, item := range notif.OngoingIncidents.Added {
			if c.incidentState.IsSnoozed(string(item.Key)) {
				continue
			}
			c.notifications.Push(string(item.State)+": "+string(item.Key), item.Details)
		}
		// Do not show desktop notifications for updates to existing incidents:
		// volatile details (such as connection ports in an error) can change
		// repeatedly while the underlying incident is still the same.
		for _, item := range notif.OngoingIncidents.Removed {
			if c.incidentState.IsSnoozed(string(item.Key)) {
				continue
			}
			c.notifications.Push("OK: "+string(item.Key), "")
		}
	}

	unknownServerCount, _ := c.getServerInitializationCounts()
	state := getAlertingOverallStateFromItems(snapshot.Alerting, unknownServerCount)
	c.logger.Log(logs.Info, "Overall state is %s (%d alerting, %d snoozed)", state, len(snapshot.Alerting), len(snapshot.Snoozed))
}
