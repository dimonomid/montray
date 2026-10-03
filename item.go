package montray

import "time"

type Item struct {
	// Key uniquely identifies the item.
	Key ItemKey `json:"key"`

	// State is the current item's state.
	State ItemState `json:"state"`

	// Details, if not empty, contains a human-readable clarification of the
	// item's current state.
	Details string `json:"details"`
}

type ItemKey string

type ItemState string

const (
	ItemStateOK      ItemState = "ok"
	ItemStateWarning ItemState = "warning"
	ItemStateError   ItemState = "error"
)

// IsItemStateValid reports whether state is one of Montray's defined item
// states.
func IsItemStateValid(state ItemState) bool {
	return state == ItemStateOK || state == ItemStateWarning || state == ItemStateError
}

type ItemWContext struct {
	Item `json:""`

	// IncidentStartedAt indicates when the current non-OK incident began.
	IncidentStartedAt time.Time `json:"incidentStartedAt"`

	// Stale reports that the incident's source disconnected after supplying it.
	// A subsequent source snapshot replaces it with the snapshot's freshness.
	Stale bool `json:"stale,omitempty"`
}

func (item *Item) Equals(other *Item) bool {
	return item.Key == other.Key && item.State == other.State && item.Details == other.Details
}
