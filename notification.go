package montray

import "time"

type Notification struct {
	// Time indicates when the notification was generated.
	Time time.Time `json:"time"`

	OngoingIncidents OngoingIncidentsWDelta `json:"ongoingIncidents"`

	// TODO: add one-off incidents in some form
}

type OngoingIncidentsWDelta struct {
	Total []*ItemWContext `json:"total"`

	Added   []*ItemWContext `json:"added"`
	Removed []*ItemWContext `json:"removed"`
	Updated []*ItemWContext `json:"updated"`

	NumItemsOK int `json:"numItemsOK"`
}
