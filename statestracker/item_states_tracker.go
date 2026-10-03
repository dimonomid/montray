package statestracker

import (
	"sort"

	"github.com/benbjohnson/clock"

	"github.com/dimonomid/montray/v2"
)

type ItemStatesTracker struct {
	params ItemStatesTrackerParams

	itemsOK    map[montray.ItemKey]*montray.ItemWContext
	itemsNotOK map[montray.ItemKey]*montray.ItemWContext
}

type ItemStatesTrackerParams struct {
	Clock clock.Clock
}

func NewItemStatesTracker(params ItemStatesTrackerParams) *ItemStatesTracker {
	return &ItemStatesTracker{
		params: params,

		itemsOK:    map[montray.ItemKey]*montray.ItemWContext{},
		itemsNotOK: map[montray.ItemKey]*montray.ItemWContext{},
	}
}

func (ist *ItemStatesTracker) FeedItems(newItems map[montray.ItemKey]*montray.Item) *montray.Notification {
	added := map[montray.ItemKey]*montray.ItemWContext{}
	removed := map[montray.ItemKey]*montray.ItemWContext{}
	updated := map[montray.ItemKey]*montray.ItemWContext{}

	for _, item := range newItems {
		if item.State == montray.ItemStateOK {
			if _, exists := ist.itemsOK[item.Key]; !exists {
				// Either it's a new item, or it transitioned from non-ok to ok.
				iwc := &montray.ItemWContext{
					Item: *item,
				}
				ist.itemsOK[item.Key] = iwc
			}

			if exItem, exists := ist.itemsNotOK[item.Key]; exists {
				delete(ist.itemsNotOK, item.Key)

				// Add a delta that an incident was removed.
				removed[item.Key] = exItem
			}
		} else {
			if exItem, exists := ist.itemsNotOK[item.Key]; !exists {
				// Either it's a new item, or it transitioned from ok to non-ok.
				iwc := &montray.ItemWContext{
					Item:              *item,
					IncidentStartedAt: ist.params.Clock.Now(),
				}
				ist.itemsNotOK[item.Key] = iwc

				// Add a delta that an incident was added.
				added[item.Key] = iwc
			} else if !exItem.Item.Equals(item) {
				// Incident existed already, and its details have changed, add a delta
				// with that.
				exItem.Item = *item
				updated[item.Key] = exItem
			}

			if _, exists := ist.itemsOK[item.Key]; exists {
				delete(ist.itemsOK, item.Key)
			}
		}
	}

	// If there were no changes, just return nil.
	if len(added) == 0 && len(removed) == 0 && len(updated) == 0 {
		return nil
	}

	// There were some changes, so return them.

	notif := montray.Notification{
		Time: ist.params.Clock.Now(),

		OngoingIncidents: montray.OngoingIncidentsWDelta{
			Total: itemsMapToSortedSlice(ist.itemsNotOK),

			Added:   itemsMapToSortedSlice(added),
			Removed: itemsMapToSortedSlice(removed),
			Updated: itemsMapToSortedSlice(updated),

			NumItemsOK: len(ist.itemsOK),
		},
	}
	return &notif
}

func itemsMapToSortedSlice(m map[montray.ItemKey]*montray.ItemWContext) []*montray.ItemWContext {
	ret := make([]*montray.ItemWContext, 0, len(m))
	for _, item := range m {
		// The tracker mutates its stored items when later observations update an
		// incident. Notifications are consumed asynchronously, so publishing
		// those stored pointers would let a later FeedItems call change an
		// earlier notification while a messenger is reading it.
		itemSnapshot := *item
		ret = append(ret, &itemSnapshot)
	}

	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Key < ret[j].Key
	})

	return ret
}
