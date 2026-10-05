package main

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"sync"
	"time"

	"github.com/dimonomid/clock"

	"github.com/dimonomid/montray/v2"
	"github.com/dimonomid/montray/v2/logs"
)

// snoozeDurations are the durations accepted by the status UI and API.
// There is intentionally no permanent snooze: incidents that should be ignored
// forever belong in the Montray UI configuration instead.
var snoozeDurations = map[string]time.Duration{
	"15m": 15 * time.Minute,
	"30m": 30 * time.Minute,
	"1h":  time.Hour,
	"4h":  4 * time.Hour,
	"6h":  6 * time.Hour,
	"12h": 12 * time.Hour,
	"1d":  24 * time.Hour,
	"2d":  2 * 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
}

// snoozeEntry is the on-disk representation of one incident's snooze expiry.
type snoozeEntry struct {
	SnoozedUntil time.Time `json:"snoozed_until"`
}

// persistedMontrayLegacyState is the complete on-disk montray state-file
// structure.
type persistedMontrayLegacyState struct {
	Snoozed map[string]snoozeEntry `json:"snoozed"`
}

// snoozeState owns the persisted snooze map and serializes concurrent updates.
type snoozeState struct {
	mtx     sync.RWMutex
	path    string
	snoozed map[string]snoozeEntry
}

// latestOngoingIncidents keeps a copy of the most recently combined incident
// snapshot for the local status UI and incident-state classifier.
type latestOngoingIncidents struct {
	mtx   sync.RWMutex
	items []montray.ItemWContext
}

// incidentSnapshot is the shared, already-classified view consumed by the
// tray and the local status webserver.
type incidentSnapshot struct {
	Alerting []montray.ItemWContext
	Snoozed  []snoozedIncident
}

type snoozedIncident struct {
	montray.ItemWContext
	SnoozedUntil time.Time `json:"snoozedUntil"`
}

// SnoozedItems returns the incident data without snooze metadata for state
// aggregation by consumers such as the tray icon.
func (s incidentSnapshot) SnoozedItems() []montray.ItemWContext {
	items := make([]montray.ItemWContext, 0, len(s.Snoozed))
	for _, item := range s.Snoozed {
		items = append(items, item.ItemWContext)
	}
	return items
}

// incidentState is the shared source of truth for Montray UI Legacy's current active
// incidents and persisted snooze decisions. It produces the classified
// snapshot consumed by both the tray icon and the status webserver.
type incidentState struct {
	// ongoingIncidents stores the latest combined Montray Server snapshot.
	ongoingIncidents latestOngoingIncidents
	// snoozes stores persisted snooze decisions and their expiration times.
	snoozes   *snoozeState
	clock     clock.Clock
	logger    *logs.Logger
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once

	// OnUpdate is called after the classified snapshot changes. The application
	// uses it to publish the same snapshot to the webserver and tray.
	OnUpdate func(snapshot incidentSnapshot)
	// OnSnoozeExpired is called only for automatically unsnoozed incidents that
	// are still present in the latest combined snapshot.
	OnSnoozeExpired func(item montray.ItemWContext)
}

// newIncidentState loads the persisted snoozes and initializes the shared
// active-incident classifier.
func newIncidentState(path string, clk clock.Clock, logger *logs.Logger) (*incidentState, error) {
	return newIncidentStateWithInterval(path, 10*time.Second, clk, logger)
}

func newIncidentStateWithInterval(path string, expirationInterval time.Duration, clk clock.Clock, logger *logs.Logger) (*incidentState, error) {
	if clk == nil {
		panic("Clock is required")
	}
	if logger == nil {
		panic("Logger is required")
	}
	snoozes, err := newSnoozeState(path)
	if err != nil {
		return nil, err
	}
	state := &incidentState{
		snoozes: snoozes,
		clock:   clk,
		logger:  logger.WithNamespaceAppended("IncidentState"),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	ticker := clk.Ticker(expirationInterval)
	go state.watchSnoozeExpirations(ticker)
	return state, nil
}

// Close stops the snooze-expiration worker and waits for it to exit. It is
// safe to call more than once.
func (s *incidentState) Close() {
	s.closeOnce.Do(func() {
		s.logger.Log(logs.Info, "Shutting down")
		close(s.stop)
		<-s.done
		s.logger.Log(logs.Info, "Shutdown complete")
	})
}

// newSnoozeState loads an existing state file or creates an empty one.
func newSnoozeState(path string) (*snoozeState, error) {
	state := &snoozeState{path: path, snoozed: make(map[string]snoozeEntry)}
	data, err := ioutil.ReadFile(path)
	if os.IsNotExist(err) {
		if err := state.writeLocked(state.snoozed); err != nil {
			return nil, err
		}
		return state, nil
	}
	if err != nil {
		return nil, err
	}

	var persisted persistedMontrayLegacyState
	if err := json.Unmarshal(data, &persisted); err != nil {
		return nil, err
	}
	if persisted.Snoozed != nil {
		state.snoozed = persisted.Snoozed
	}
	return state, nil
}

// Update stores the current active incidents and classifies them according to
// the persisted snooze state.
func (s *incidentState) Update(items []*montray.ItemWContext) incidentSnapshot {
	s.ongoingIncidents.Set(items)
	snapshot := s.snapshot()
	if s.OnUpdate != nil {
		s.OnUpdate(snapshot)
	}
	return snapshot
}

// Snooze persists a snooze and notifies the update hook of the new snapshot.
func (s *incidentState) Snooze(key string, duration time.Duration) error {
	if err := s.snoozes.Snooze(key, duration, s.clock.Now()); err != nil {
		return err
	}
	s.logger.Log(logs.Info, "Snoozed incident %s for %s", key, duration)
	s.notifyUpdate()
	return nil
}

// Unsnooze removes a persisted snooze and notifies the update hook of the new
// snapshot.
func (s *incidentState) Unsnooze(key string) error {
	if err := s.snoozes.Unsnooze(key); err != nil {
		return err
	}
	s.logger.Log(logs.Info, "Unsnoozed incident %s", key)
	s.notifyUpdate()
	return nil
}

// IsSnoozed reports whether a key is currently suppressed from notifications.
func (s *incidentState) IsSnoozed(key string) bool {
	return s.snoozes.IsSnoozed(key, s.clock.Now())
}

// watchSnoozeExpirations refreshes consumers periodically so expired snoozes
// are noticed even if no new incident snapshot arrives from the server.
func (s *incidentState) watchSnoozeExpirations(ticker *clock.Ticker) {
	defer ticker.Stop()
	defer close(s.done)

	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			expired, err := s.snoozes.expire(s.clock.Now())
			if err != nil {
				s.logger.Log(logs.Error, "Failed to expire snoozes: %s", err)
				continue
			}
			for _, key := range expired {
				s.logger.Log(logs.Info, "Snooze expired for incident %s", key)
			}
			if len(expired) > 0 {
				s.notifySnoozeExpired(expired)
				s.notifyUpdate()
			}
		}
	}
}

func (s *incidentState) notifySnoozeExpired(expired []string) {
	if s.OnSnoozeExpired == nil {
		return
	}
	expiredKeys := make(map[string]struct{}, len(expired))
	for _, key := range expired {
		expiredKeys[key] = struct{}{}
	}
	for _, item := range s.ongoingIncidents.Get() {
		if _, ok := expiredKeys[string(item.Key)]; ok {
			s.OnSnoozeExpired(item)
		}
	}
}

func (s *incidentState) notifyUpdate() {
	if s.OnUpdate != nil {
		s.OnUpdate(s.snapshot())
	}
}

func (s *incidentState) snapshot() incidentSnapshot {
	alerting := make([]montray.ItemWContext, 0)
	snoozed := make([]snoozedIncident, 0)
	now := s.clock.Now()
	for _, item := range s.ongoingIncidents.Get() {
		if snoozedUntil, ok := s.snoozes.snoozedUntil(string(item.Key), now); ok {
			snoozed = append(snoozed, snoozedIncident{
				ItemWContext: item,
				SnoozedUntil: snoozedUntil,
			})
		} else {
			alerting = append(alerting, item)
		}
	}
	return incidentSnapshot{Alerting: alerting, Snoozed: snoozed}
}

// Snooze marks the given key as snoozed for the given duration.
func (s *snoozeState) Snooze(key string, duration time.Duration, now time.Time) error {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	updated := make(map[string]snoozeEntry, len(s.snoozed)+1)
	for existingKey, entry := range s.snoozed {
		updated[existingKey] = entry
	}
	// Snoozes are wall-clock deadlines. Strip Go's monotonic clock reading so
	// time spent with the machine suspended still counts toward the duration.
	// On systems where the monotonic clock pauses during suspend, retaining it
	// would make the displayed deadline pass while the snooze remained active.
	updated[key] = snoozeEntry{SnoozedUntil: now.Round(0).Add(duration)}

	if err := s.writeLocked(updated); err != nil {
		return err
	}
	s.snoozed = updated
	return nil
}

func (s *snoozeState) Unsnooze(key string) error {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	updated := make(map[string]snoozeEntry, len(s.snoozed))
	for existingKey, entry := range s.snoozed {
		if existingKey != key {
			updated[existingKey] = entry
		}
	}

	if err := s.writeLocked(updated); err != nil {
		return err
	}
	s.snoozed = updated
	return nil
}

// IsSnoozed reports whether the entry exists and has not expired at now.
func (s *snoozeState) IsSnoozed(key string, now time.Time) bool {
	s.mtx.RLock()
	defer s.mtx.RUnlock()

	entry, exists := s.snoozed[key]
	return exists && entry.SnoozedUntil.After(now)
}

func (s *snoozeState) snoozedUntil(key string, now time.Time) (time.Time, bool) {
	s.mtx.RLock()
	defer s.mtx.RUnlock()

	entry, exists := s.snoozed[key]
	return entry.SnoozedUntil, exists && entry.SnoozedUntil.After(now)
}

// expire removes snoozes whose expiration time has passed and returns their
// keys.
func (s *snoozeState) expire(now time.Time) ([]string, error) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	updated := make(map[string]snoozeEntry, len(s.snoozed))
	expired := make([]string, 0)
	for key, entry := range s.snoozed {
		if entry.SnoozedUntil.After(now) {
			updated[key] = entry
		} else {
			expired = append(expired, key)
		}
	}
	if len(expired) == 0 {
		return nil, nil
	}
	if err := s.writeLocked(updated); err != nil {
		return nil, err
	}
	s.snoozed = updated
	return expired, nil
}

// writeLocked writes a human-readable state file. The caller must hold mtx
// when changing the live snooze map.
func (s *snoozeState) writeLocked(snoozed map[string]snoozeEntry) error {
	data, err := json.MarshalIndent(persistedMontrayLegacyState{Snoozed: snoozed}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return ioutil.WriteFile(s.path, data, 0600)
}

// Set replaces the snapshot with copies of the supplied items.
func (o *latestOngoingIncidents) Set(items []*montray.ItemWContext) {
	o.mtx.Lock()
	defer o.mtx.Unlock()

	o.items = make([]montray.ItemWContext, 0, len(items))
	for _, item := range items {
		if item != nil {
			o.items = append(o.items, *item)
		}
	}
}

// Get returns a copy so consumers never share mutable items with the
// websocket-combiner goroutine.
func (o *latestOngoingIncidents) Get() []montray.ItemWContext {
	o.mtx.RLock()
	defer o.mtx.RUnlock()

	items := make([]montray.ItemWContext, len(o.items))
	copy(items, o.items)
	return items
}
