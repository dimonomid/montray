package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPreserveLegacyStateCopiesUnversionedCanonicalState(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, stateFilename)
	destination := filepath.Join(directory, legacyStateFilename)
	contents := []byte(`{
  "snoozed": {"local.disk": {"snoozed_until": "2026-09-06T12:00:00Z"}}
}
`)
	if err := os.WriteFile(source, contents, 0644); err != nil {
		t.Fatal(err)
	}

	copied, err := preserveLegacyState(source, destination)
	if err != nil {
		t.Fatal(err)
	}
	if !copied {
		t.Fatal("legacy state was not reported as copied")
	}

	assertFileContents(t, source, contents)
	assertFileContents(t, destination, contents)
}

func TestPreserveLegacyStateCopiesSchemaVersionZero(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, stateFilename)
	destination := filepath.Join(directory, legacyStateFilename)
	contents := []byte(`{"schema_version":0,"snoozed":{}}`)
	if err := os.WriteFile(source, contents, 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := preserveLegacyState(source, destination); err != nil {
		t.Fatal(err)
	}

	assertFileContents(t, destination, contents)
}

func TestPreserveLegacyStateIgnoresVersionOneCanonicalState(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, stateFilename)
	destination := filepath.Join(directory, legacyStateFilename)
	if err := os.WriteFile(source, []byte(`{"schema_version":1,"snoozed":{}}`), 0600); err != nil {
		t.Fatal(err)
	}

	copied, err := preserveLegacyState(source, destination)
	if err != nil {
		t.Fatal(err)
	}
	if copied {
		t.Fatal("versioned state was reported as copied")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("legacy state exists after versioned source: %v", err)
	}
}

func TestPreserveLegacyStateDoesNotOverwriteExistingDestination(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, stateFilename)
	destination := filepath.Join(directory, legacyStateFilename)
	if err := os.WriteFile(source, []byte(`{"snoozed":{"source":{}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	existing := []byte(`{"snoozed":{"destination":{}}}`)
	if err := os.WriteFile(destination, existing, 0600); err != nil {
		t.Fatal(err)
	}

	copied, err := preserveLegacyState(source, destination)
	if err != nil {
		t.Fatal(err)
	}
	if copied {
		t.Fatal("existing destination was reported as copied")
	}

	assertFileContents(t, destination, existing)
}

func TestPreserveLegacyStateIsSafeWhenCalledConcurrently(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, stateFilename)
	destination := filepath.Join(directory, legacyStateFilename)
	contents := []byte(`{"snoozed":{"local.disk":{"snoozed_until":"2026-09-06T12:00:00Z"}}}`)
	if err := os.WriteFile(source, contents, 0600); err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	results := make(chan bool, 8)
	errors := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			copied, err := preserveLegacyState(source, destination)
			results <- copied
			errors <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errors)
	copies := 0
	for copied := range results {
		if copied {
			copies++
		}
	}
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if copies != 1 {
		t.Fatalf("successful copies = %d, want 1", copies)
	}
	assertFileContents(t, destination, contents)
}

func assertFileContents(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("contents of %s = %q, want %q", path, got, want)
	}
}
