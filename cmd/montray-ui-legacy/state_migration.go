package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	stateFilename       = ".montray-state.json"
	legacyStateFilename = ".montray-ui-legacy-state.json"
)

// preserveLegacyState copies an unversioned (or pre-v1) canonical state file
// to the legacy app's dedicated path. The no-clobber installation makes this
// safe when both applications perform the migration at the same time.
func preserveLegacyState(sourcePath, legacyPath string) (bool, error) {
	if _, err := os.Stat(legacyPath); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("inspect legacy state file: %w", err)
	}

	data, err := os.ReadFile(sourcePath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read state file for legacy migration: %w", err)
	}

	legacy, err := isLegacyState(data)
	if err != nil {
		return false, fmt.Errorf("inspect state file for legacy migration: %w", err)
	}
	if !legacy {
		return false, nil
	}

	copied, err := copyFileNoReplace(legacyPath, data)
	if err != nil {
		return false, fmt.Errorf("preserve legacy state file: %w", err)
	}
	return copied, nil
}

func isLegacyState(data []byte) (bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return false, err
	}
	encodedVersion, present := fields["schema_version"]
	if !present {
		return true, nil
	}
	var version uint64
	if err := json.Unmarshal(encodedVersion, &version); err != nil {
		return false, fmt.Errorf("decode schema_version: %w", err)
	}
	return version < 1, nil
}

// copyFileNoReplace publishes a complete file atomically and treats another
// process winning the destination-creation race as success.
func copyFileNoReplace(destination string, data []byte) (bool, error) {
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".montray-ui-legacy-state-*")
	if err != nil {
		return false, err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()

	if err := temporary.Chmod(0600); err != nil {
		return false, err
	}
	if written, err := temporary.Write(data); err != nil {
		return false, err
	} else if written != len(data) {
		return false, fmt.Errorf("short write: wrote %d of %d bytes", written, len(data))
	}
	if err := temporary.Sync(); err != nil {
		return false, err
	}
	if err := temporary.Close(); err != nil {
		return false, err
	}
	if err := os.Link(temporaryPath, destination); os.IsExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}
