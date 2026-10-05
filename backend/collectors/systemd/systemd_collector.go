package systemd

import (
	"fmt"
	"strings"
	"sync"

	"github.com/dimonomid/clock"
	"github.com/dimonomid/montray/v2"
	"github.com/dimonomid/montray/v2/backend/collectors"
	"github.com/dimonomid/montray/v2/logs"
)

type Collector struct {
	params CollectorParams

	pr        Provider
	teardown  chan struct{}
	torndown  chan struct{}
	closeOnce sync.Once
}

// pendingResolution holds the latest qualifying OK item while its incident's
// recovery timer is running. The generation distinguishes the current timer
// from callbacks belonging to earlier, canceled recovery attempts.
type pendingResolution struct {
	item       *montray.Item
	timer      *clock.Timer
	generation uint64
}

// resolutionReady is emitted by a recovery timer. run compares its generation
// with the pending resolution before publishing the stored OK item, so a timer
// callback that races with cancellation cannot resolve a newer incident state.
type resolutionReady struct {
	key        montray.ItemKey
	generation uint64
}

// deferredResolutionUnitState remembers the last systemd state logged while
// an incident remained unresolved under its resolve policy. It prevents
// unrelated DBus property updates from repeating the same message.
type deferredResolutionUnitState struct {
	state    UnitState
	subState string
}

var _ collectors.Collector = &Collector{}

type CollectorParams struct {
	Common collectors.Params

	Config Config
	Clock  clock.Clock

	// ProviderFactory exists primarily so black-box tests can replace the
	// external systemd/DBus boundary with a controlled provider. Production
	// callers should normally leave it nil to use the DBus-backed provider. A
	// custom provider must obey the shutdown contract documented by Provider.
	ProviderFactory func(ProviderParams) (Provider, error)
}

func NewCollector(params CollectorParams) (*Collector, error) {
	if params.Common.Logger == nil {
		panic("Logger is required")
	}
	if params.Clock == nil {
		params.Clock = clock.New()
	}
	params.Common.Logger = params.Common.Logger.WithNamespaceAppended("SystemdCollector")
	if err := validateConfig(params.Config); err != nil {
		return nil, err
	}
	providerUpdCh := make(chan *UnitUpdate, 16)

	providerFactory := params.ProviderFactory
	if providerFactory == nil {
		providerFactory = func(providerParams ProviderParams) (Provider, error) {
			return NewProviderCoreos(ProviderCoreosParams{Common: providerParams})
		}
	}
	pr, err := providerFactory(ProviderParams{UnitUpdatesChan: providerUpdCh})
	if err != nil {
		return nil, err
	}

	c := &Collector{
		params:   params,
		pr:       pr,
		teardown: make(chan struct{}),
		torndown: make(chan struct{}),
	}

	go c.run(providerUpdCh)

	params.Common.Logger.Log(logs.Info, "Started")

	return c, nil
}

func validateConfig(config Config) error {
	for ruleIndex, rule := range config.UnitRules {
		seenNames := make(map[string]struct{}, len(rule.Names))
		for nameIndex, name := range rule.Names {
			if name == "" {
				return fmt.Errorf("rule #%d name #%d must not be empty", ruleIndex, nameIndex)
			}
			if _, exists := seenNames[name]; exists {
				return fmt.Errorf("rule #%d contains duplicate name %q", ruleIndex, name)
			}
			seenNames[name] = struct{}{}
		}
		for conditionIndex, condition := range rule.Conditions {
			if condition.SubState != "" && condition.SubStateContains != "" {
				return fmt.Errorf("rule #%d condition #%d must not specify both subState and subStateContains", ruleIndex, conditionIndex)
			}
			if !montray.IsItemStateValid(condition.Result) {
				return fmt.Errorf("rule #%d condition #%d has invalid result %q", ruleIndex, conditionIndex, condition.Result)
			}
			if condition.Resolve != nil {
				if condition.Result == montray.ItemStateOK {
					return fmt.Errorf("rule #%d condition #%d resolve requires a non-OK result", ruleIndex, conditionIndex)
				}
				if condition.Resolve.After <= 0 {
					return fmt.Errorf("rule #%d condition #%d resolve.after must be positive", ruleIndex, conditionIndex)
				}
				if len(condition.Resolve.States) == 0 {
					return fmt.Errorf("rule #%d condition #%d resolve.states must not be empty", ruleIndex, conditionIndex)
				}
				seenStates := make(map[UnitState]struct{}, len(condition.Resolve.States))
				for stateIndex, state := range condition.Resolve.States {
					if state == "" {
						return fmt.Errorf("rule #%d condition #%d resolve state #%d must not be empty", ruleIndex, conditionIndex, stateIndex)
					}
					if _, exists := seenStates[state]; exists {
						return fmt.Errorf("rule #%d condition #%d resolve contains duplicate state %q", ruleIndex, conditionIndex, state)
					}
					seenStates[state] = struct{}{}
				}
			}
		}
	}
	return nil
}

func (c *Collector) Close() {
	c.closeOnce.Do(func() {
		// Stop forwarding updates first. The run goroutine continues draining the
		// provider channel so the provider cannot block while it shuts down.
		close(c.teardown)
		c.pr.Close()

		// Provider shutdown closes providerUpdCh. Once it has been drained, the
		// run goroutine exits and closes c.torndown.
		<-c.torndown
	})
}

func (c *Collector) run(providerUpdCh chan *UnitUpdate) {
	defer close(c.torndown)
	firstUpdate := true
	shuttingDown := false

	// incidentResolve remembers the policy of the condition that originally
	// created each active incident. A present nil value means that the incident
	// has no delayed-resolution policy and should resolve on its next OK update.
	incidentResolve := make(map[montray.ItemKey]*ConfigResolve)
	// incidentStates retains the latest non-OK severity for each incident. While
	// resolution is deferred, updates use this severity with the unit's current
	// details so consumers see the new systemd state without resolving it.
	incidentStates := make(map[montray.ItemKey]montray.ItemState)

	// pendingResolutions contains incidents currently receiving a qualifying OK
	// state and waiting for their configured recovery duration to elapse.
	pendingResolutions := make(map[montray.ItemKey]*pendingResolution)

	// deferredResolutionStates tracks the last state reported while resolution
	// was delayed, so each actual state or substate transition is logged once.
	deferredResolutionStates := make(map[montray.ItemKey]deferredResolutionUnitState)

	// Timer callbacks send only an identity through resolutionReadyCh. The run
	// goroutine remains the sole owner of the maps and verifies the generation
	// before publishing the pending OK item.
	resolutionReadyCh := make(chan resolutionReady, 16)
	var nextGeneration uint64

	defer func() {
		for _, pending := range pendingResolutions {
			pending.timer.Stop()
		}
	}()

	cancelPendingResolution := func(key montray.ItemKey) {
		if pending := pendingResolutions[key]; pending != nil {
			pending.timer.Stop()
			delete(pendingResolutions, key)
		}
	}

	for {
		var sysUpd *UnitUpdate
		select {
		case varSysUpd, ok := <-providerUpdCh:
			if !ok {
				return
			}
			sysUpd = varSysUpd
		case ready := <-resolutionReadyCh:
			pending := pendingResolutions[ready.key]
			if pending == nil || pending.generation != ready.generation {
				// This timer belongs to a recovery window that was canceled or
				// superseded after the service became unhealthy again.
				continue
			}

			delete(pendingResolutions, ready.key)
			delete(incidentResolve, ready.key)
			delete(incidentStates, ready.key)
			delete(deferredResolutionStates, ready.key)
			if !c.sendUpdate(&collectors.Update{Items: map[montray.ItemKey]*montray.Item{
				ready.key: pending.item,
			}}) {
				shuttingDown = true
			}
			continue
		}

		if !shuttingDown {
			select {
			case <-c.teardown:
				shuttingDown = true
			default:
			}
		}
		if shuttingDown {
			// Keep draining until provider shutdown closes providerUpdCh.
			continue
		}

		if sysUpd.Err != nil {
			if !c.sendUpdate(&collectors.Update{
				Err: fmt.Errorf("got error from systemd conn: %w", sysUpd.Err),
			}) {
				shuttingDown = true
			}
			continue
		}

		upd := &collectors.Update{
			Items: make(map[montray.ItemKey]*montray.Item, len(sysUpd.Units)),
		}

		// On the first update, ensure every explicitly named unit is represented.
		// Missing units get a synthetic not-sent-by-systemd state.
		if firstUpdate {
			if sysUpd.Units == nil {
				sysUpd.Units = make(map[string]*Unit)
			}
			for _, rule := range c.params.Config.UnitRules {
				for _, name := range rule.Names {
					if _, exists := sysUpd.Units[name]; exists {
						continue
					}

					// The unit is not present in the actual update from systemd, so let's
					// add the synthetic one.
					sysUpd.Units[name] = &Unit{
						Name:  name,
						State: UnitStateNotSentBySystemd,
					}
				}
			}

			firstUpdate = false
		}

		for k, unit := range sysUpd.Units {
			// If unit was removed, create a fake one saying that it's not sent
			if unit == nil {
				unit = &Unit{
					Name:  k,
					State: UnitStateNotSentBySystemd,
				}
			}

			item, resolve := c.getItemFromUnit(unit)
			if item == nil {
				// We're not interested in this unit.
				continue
			}

			if item.State != montray.ItemStateOK {
				// A non-OK update interrupts any recovery window. Preserve the
				// policy of the condition that originally created the incident,
				// rather than replacing it as the unit moves through failure states.
				cancelPendingResolution(item.Key)
				delete(deferredResolutionStates, item.Key)
				if _, exists := incidentResolve[item.Key]; !exists {
					incidentResolve[item.Key] = resolve
				}
				incidentStates[item.Key] = item.State
				upd.Items[item.Key] = item
				continue
			}

			resolve, incidentExists := incidentResolve[item.Key]
			if !incidentExists || resolve == nil {
				// Healthy units that never had an incident, and incidents created
				// by conditions without a resolve policy, are published immediately.
				cancelPendingResolution(item.Key)
				delete(incidentResolve, item.Key)
				delete(incidentStates, item.Key)
				delete(deferredResolutionStates, item.Key)
				upd.Items[item.Key] = item
				continue
			}

			deferredState := deferredResolutionUnitState{state: unit.State, subState: unit.SubState}
			lastDeferredState, stateAlreadyLogged := deferredResolutionStates[item.Key]
			stateChanged := !stateAlreadyLogged || lastDeferredState != deferredState
			if stateChanged {
				deferredResolutionStates[item.Key] = deferredState
			}

			contributesToResolution := containsUnitState(resolve.States, unit.State)
			if stateChanged {
				// Publish the new unit details as an incident update, but retain the
				// last non-OK severity until the recovery policy actually resolves it.
				deferredItem := *item
				deferredItem.State = incidentStates[item.Key]
				if contributesToResolution {
					deferredItem.Details = fmt.Sprintf(
						"%s; incident resolves after %s in this state",
						item.Details, resolve.After)
				} else {
					deferredItem.Details = fmt.Sprintf(
						"%s; incident remains unresolved: state %q does not contribute to recovery (resolve.states: %v)",
						item.Details, unit.State, resolve.States)
				}
				upd.Items[item.Key] = &deferredItem
			}

			if !contributesToResolution {
				// An OK state outside the configured recovery set does not prove
				// that this incident has recovered. Stop any running timer and wait
				// for one of the explicitly configured states.
				cancelPendingResolution(item.Key)
				if stateChanged {
					c.params.Common.Logger.Log(logs.Info,
						"Incident remains unresolved: %s: %s; state %q is not in resolve.states %v",
						item.Key, item.Details, unit.State, resolve.States)
				}
				continue
			}

			if stateChanged {
				c.params.Common.Logger.Log(logs.Info,
					"Incident resolution pending: %s: %s; resolve requires %s of continuous recovery in states %v",
					item.Key, item.Details, resolve.After, resolve.States)
			}

			if pending := pendingResolutions[item.Key]; pending != nil {
				// A different qualifying substate does not interrupt recovery. Keep
				// the original timer, but publish the freshest details when it expires.
				pending.item = item
				continue
			}

			// The incident has entered a configured recovery state and has no
			// recovery timer yet, so begin a new recovery attempt. Its unique
			// generation lets run discard the callback if a later update cancels
			// or replaces this attempt before the duration elapses.
			nextGeneration++
			generation := nextGeneration
			key := item.Key
			pending := &pendingResolution{item: item, generation: generation}
			pending.timer = c.params.Clock.AfterFunc(resolve.After, func() {
				select {
				case resolutionReadyCh <- resolutionReady{key: key, generation: generation}:
				case <-c.teardown:
				}
			})
			pendingResolutions[key] = pending
		}

		// If the update is empty, don't send it.
		if len(upd.Items) == 0 {
			continue
		}

		if !c.sendUpdate(upd) {
			shuttingDown = true
		}
	}
}

// sendUpdate forwards an update unless shutdown has started. Returning false
// tells run to stop publishing while it drains the provider channel.
func (c *Collector) sendUpdate(update *collectors.Update) bool {
	select {
	case c.params.Common.UpdatesChan <- update:
		return true
	case <-c.teardown:
		return false
	}
}

func (c *Collector) itemKeyFromSystemdName(name string) montray.ItemKey {
	return montray.ItemKey(c.params.Common.ID + "." + name)
}

func (c *Collector) getItemFromUnit(unit *Unit) (*montray.Item, *ConfigResolve) {
	for _, rule := range c.params.Config.UnitRules {
		if len(rule.Names) > 0 && !containsString(rule.Names, unit.Name) {
			continue
		}

		if rule.Type != "" && !strings.HasSuffix(unit.Name, "."+rule.Type) {
			continue
		}

		// This rule applies to the given unit

		item := &montray.Item{
			Key:     c.itemKeyFromSystemdName(unit.Name),
			Details: systemdUnitDetails(unit),
		}
		var resolve *ConfigResolve

		for _, cond := range rule.Conditions {
			if cond.State != "" && cond.State != unit.State {
				continue
			}
			if cond.SubState != "" && cond.SubState != unit.SubState {
				continue
			}
			if cond.SubStateContains != "" && !strings.Contains(unit.SubState, cond.SubStateContains) {
				continue
			}
			// Found the matching condition, so use its result
			item.State = cond.Result
			resolve = cond.Resolve
			break
		}

		if item.State == "" {
			// By default, assume error
			item.State = montray.ItemStateError
		}

		return item, resolve
	}

	// Found no rule that applies to the given unit, it means we're not
	// interested in this unit at all.
	return nil, nil
}

func containsUnitState(states []UnitState, want UnitState) bool {
	for _, state := range states {
		if state == want {
			return true
		}
	}
	return false
}

func systemdUnitDetails(unit *Unit) string {
	switch unit.State {
	case UnitStateNotSentBySystemd:
		return fmt.Sprintf("Unit %s was not reported by systemd", unit.Name)
	default:
		if unit.SubState != "" && unit.SubState != string(unit.State) {
			return fmt.Sprintf("Unit %s is %s (%s)", unit.Name, unit.State, unit.SubState)
		}
		return fmt.Sprintf("Unit %s is %s", unit.Name, unit.State)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
