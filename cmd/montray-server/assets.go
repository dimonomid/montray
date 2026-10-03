package main

import (
	"embed"
	"fmt"
)

// embeddedSetupAssets contains the default configuration and systemd unit
// templates used by the setup commands.
//
//go:embed assets/setup
var embeddedSetupAssets embed.FS

func mustSetupAsset(name string) []byte {
	data, err := embeddedSetupAssets.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("embedded setup asset %q is unavailable: %s", name, err))
	}
	return data
}
