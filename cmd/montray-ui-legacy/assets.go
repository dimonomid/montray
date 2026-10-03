package main

import (
	"embed"
	"fmt"
)

// embeddedAssets contains the tray icons and static status UI.
//
//go:embed assets
var embeddedAssets embed.FS

func mustEmbeddedAsset(name string) []byte {
	data, err := embeddedAssets.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("embedded asset %q is unavailable: %s", name, err))
	}
	return data
}
