package statestracker_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/dimonomid/clock"
	"github.com/dimonomid/montray/v2"
	"github.com/dimonomid/montray/v2/statestracker"
)

func TestTrackerPublishesIncidentLifecycle(t *testing.T) {
	mockClock := clock.NewMock()
	tracker := statestracker.NewItemStatesTracker(statestracker.ItemStatesTrackerParams{Clock: mockClock})
	key := montray.ItemKey("systemd.sync.service")

	if got := tracker.FeedItems(items(item(key, montray.ItemStateOK, "active"))); got != nil {
		t.Fatalf("initial healthy observation produced notification %#v", got)
	}

	mockClock.Add(time.Minute)
	addedAt := mockClock.Now()
	added := tracker.FeedItems(items(item(key, montray.ItemStateWarning, "failed")))
	assertNotification(t, added, []string{string(key)}, []string{string(key)}, nil, nil, 0)
	if got := added.OngoingIncidents.Total[0].IncidentStartedAt; !got.Equal(addedAt) {
		t.Fatalf("incident IncidentStartedAt = %s, want %s", got, addedAt)
	}

	mockClock.Add(time.Minute)
	updated := tracker.FeedItems(items(item(key, montray.ItemStateError, "still failed")))
	assertNotification(t, updated, []string{string(key)}, nil, nil, []string{string(key)}, 0)
	if got := updated.OngoingIncidents.Total[0].IncidentStartedAt; !got.Equal(addedAt) {
		t.Fatalf("updated incident IncidentStartedAt = %s, want original %s", got, addedAt)
	}

	if got := tracker.FeedItems(items(item(key, montray.ItemStateError, "still failed"))); got != nil {
		t.Fatalf("redundant observation produced notification %#v", got)
	}

	mockClock.Add(time.Minute)
	removed := tracker.FeedItems(items(item(key, montray.ItemStateOK, "active")))
	assertNotification(t, removed, nil, nil, []string{string(key)}, nil, 1)
}

func TestTrackerSortsSnapshotsAndKeepsIndependentItems(t *testing.T) {
	tracker := statestracker.NewItemStatesTracker(statestracker.ItemStatesTrackerParams{Clock: clock.NewMock()})
	notification := tracker.FeedItems(items(
		item("z.last", montray.ItemStateError, "z"),
		item("a.first", montray.ItemStateWarning, "a"),
		item("m.healthy", montray.ItemStateOK, "m"),
	))
	assertNotification(t, notification,
		[]string{"a.first", "z.last"},
		[]string{"a.first", "z.last"}, nil, nil, 1,
	)
}

func TestPublishedNotificationIsNotChangedByLaterUpdates(t *testing.T) {
	tracker := statestracker.NewItemStatesTracker(statestracker.ItemStatesTrackerParams{Clock: clock.NewMock()})
	key := montray.ItemKey("probe")
	first := tracker.FeedItems(items(item(key, montray.ItemStateWarning, "first failure")))

	tracker.FeedItems(items(item(key, montray.ItemStateError, "failure changed")))

	assertItem := func(name string, got *montray.ItemWContext) {
		t.Helper()
		if got.State != montray.ItemStateWarning || got.Details != "first failure" {
			t.Errorf("%s after later update = state %q, details %q; want state %q, details %q",
				name, got.State, got.Details, montray.ItemStateWarning, "first failure")
		}
	}
	assertItem("Total item", first.OngoingIncidents.Total[0])
	assertItem("Added item", first.OngoingIncidents.Added[0])
}

func item(key montray.ItemKey, state montray.ItemState, details string) *montray.Item {
	return &montray.Item{Key: key, State: state, Details: details}
}

func items(values ...*montray.Item) map[montray.ItemKey]*montray.Item {
	result := make(map[montray.ItemKey]*montray.Item, len(values))
	for _, value := range values {
		result[value.Key] = value
	}
	return result
}

func assertNotification(t *testing.T, notification *montray.Notification, total, added, removed, updated []string, healthy int) {
	t.Helper()
	if notification == nil {
		t.Fatal("notification is nil")
	}
	incidentKeys := func(values []*montray.ItemWContext) []string {
		keys := make([]string, 0, len(values))
		for _, value := range values {
			keys = append(keys, string(value.Key))
		}
		return keys
	}
	normalize := func(values []string) []string {
		if values == nil {
			return []string{}
		}
		return values
	}
	got := notification.OngoingIncidents
	if keys := incidentKeys(got.Total); !reflect.DeepEqual(keys, normalize(total)) {
		t.Errorf("Total keys = %#v, want %#v", keys, total)
	}
	if keys := incidentKeys(got.Added); !reflect.DeepEqual(keys, normalize(added)) {
		t.Errorf("Added keys = %#v, want %#v", keys, added)
	}
	if keys := incidentKeys(got.Removed); !reflect.DeepEqual(keys, normalize(removed)) {
		t.Errorf("Removed keys = %#v, want %#v", keys, removed)
	}
	if keys := incidentKeys(got.Updated); !reflect.DeepEqual(keys, normalize(updated)) {
		t.Errorf("Updated keys = %#v, want %#v", keys, updated)
	}
	if got.NumItemsOK != healthy {
		t.Errorf("NumItemsOK = %d, want %d", got.NumItemsOK, healthy)
	}
}
