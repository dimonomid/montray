package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateBearerTokenCreatesSecureFileAndPrintsExactInstructions(t *testing.T) {
	configFilename := filepath.Join(t.TempDir(), "config", "montray.yml")
	command := newWatchRootCommand()
	output := &bytes.Buffer{}
	command.SetOut(output)
	command.SetErr(output)
	command.SetArgs([]string{"--config", configFilename, "generate-bearer-token", "my-server"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}

	tokenFilename := filepath.Join(filepath.Dir(configFilename), "tokens", "my-server.token")
	tokenBytes, err := os.ReadFile(tokenFilename)
	if err != nil {
		t.Fatal(err)
	}
	decodedToken, err := base64.RawURLEncoding.DecodeString(string(tokenBytes))
	if err != nil {
		t.Fatalf("token is not valid unpadded base64url: %v", err)
	}
	if len(decodedToken) != bearerTokenNumBytes {
		t.Fatalf("decoded token length = %d, want %d", len(decodedToken), bearerTokenNumBytes)
	}
	info, err := os.Stat(tokenFilename)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0600); got != want {
		t.Fatalf("token permissions = %o, want %o", got, want)
	}

	tokenHash := sha256.Sum256(tokenBytes)
	outputText := output.String()
	wantOutput := fmt.Sprintf(`Created bearer token file:

    %s

In %s, find:

wsClient:
  servers:
    - id: my-server

And add this block inside that server entry, alongside id and addr:

      auth:
        bearerTokenFile: %q

In the corresponding Montray Server configuration, find:

core:
  messengers:
    - webserver:

And add this block inside the webserver messenger, alongside listenAddress:

        auth:
          - id: my-laptop # Identifies this credential; adjust as needed.
            bearerTokenHash: %q

If auth already exists in the webserver messenger, add only the new list entry to it.
`, tokenFilename, configFilename, tokenFilename, "sha256:"+hex.EncodeToString(tokenHash[:]))
	if outputText != wantOutput {
		t.Errorf("output:\n%s\nwant:\n%s", outputText, wantOutput)
	}
	if strings.Contains(outputText, string(tokenBytes)) {
		t.Fatal("command output contains the bearer token secret")
	}

	command = newWatchRootCommand()
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"--config", configFilename, "generate-bearer-token", "my-server"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("second generation error = %v, want overwrite refusal", err)
	}
	unchangedToken, err := os.ReadFile(tokenFilename)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(unchangedToken, tokenBytes) {
		t.Fatal("existing token was changed")
	}
}

func TestGenerateBearerTokenUsesOutputOverrideAndRejectsInvalidServerID(t *testing.T) {
	directory := t.TempDir()
	configFilename := filepath.Join(directory, "montray.yml")
	outputFilename := filepath.Join(directory, "custom.token")
	if err := generateBearerToken(&bytes.Buffer{}, configFilename, "remote", outputFilename); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outputFilename); err != nil {
		t.Fatal(err)
	}

	if err := generateBearerToken(&bytes.Buffer{}, configFilename, "../escape", ""); err == nil || !strings.Contains(err.Error(), "invalid server ID") {
		t.Fatalf("invalid-ID error = %v", err)
	}
}
