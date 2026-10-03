package itemsboard

import (
	"sync"

	"github.com/dimonomid/montray/v2"
)

type ItemsBoard struct {
	items []*montray.ItemWContext

	mtx sync.RWMutex
}

func New() *ItemsBoard {
	return &ItemsBoard{}
}

func (ib *ItemsBoard) Set(items []*montray.ItemWContext) {
	ib.mtx.Lock()
	defer ib.mtx.Unlock()

	ib.items = items
}

func (ib *ItemsBoard) Get() []*montray.ItemWContext {
	ib.mtx.RLock()
	defer ib.mtx.RUnlock()

	return ib.items
}
