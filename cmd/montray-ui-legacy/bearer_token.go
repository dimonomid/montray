package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/dimonomid/montray/v2/wsclient"
)

// bearerTokenNumBytes gives generated credentials 256 bits of entropy.
const bearerTokenNumBytes = 32

// generateBearerToken creates a new token file and prints exact configuration
// fragments for Montray UI and Montray Server.
func generateBearerToken(output io.Writer, configFilename, serverID, outputFilename string) error {
	if err := wsclient.ValidateServerID(serverID); err != nil {
		return fmt.Errorf("invalid server ID: %w", err)
	}

	absoluteConfigFilename, err := filepath.Abs(configFilename)
	if err != nil {
		return fmt.Errorf("resolve montray config path: %w", err)
	}
	if outputFilename == "" {
		outputFilename = filepath.Join(filepath.Dir(absoluteConfigFilename), "tokens", serverID+".token")
	}
	absoluteOutputFilename, err := filepath.Abs(outputFilename)
	if err != nil {
		return fmt.Errorf("resolve bearer token path: %w", err)
	}

	tokenBytes := make([]byte, bearerTokenNumBytes)
	if _, err := rand.Read(tokenBytes); err != nil {
		return fmt.Errorf("generate bearer token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	if err := writeNewBearerTokenFile(absoluteOutputFilename, token); err != nil {
		return err
	}
	tokenHash := sha256.Sum256([]byte(token))

	_, err = fmt.Fprintf(output, `Created bearer token file:

    %s

In %s, find:

wsClient:
  servers:
    - id: %s

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
`, absoluteOutputFilename, absoluteConfigFilename, serverID, absoluteOutputFilename, "sha256:"+hex.EncodeToString(tokenHash[:]))
	return err
}

// writeNewBearerTokenFile writes a token with owner-only permissions and
// refuses to replace an existing credential.
func writeNewBearerTokenFile(filename, token string) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return fmt.Errorf("create bearer token directory: %w", err)
	}
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("bearer token file already exists at %s; refusing to overwrite it", filename)
		}
		return fmt.Errorf("create bearer token file: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(filename)
		}
	}()
	if _, err := file.WriteString(token); err != nil {
		_ = file.Close()
		return fmt.Errorf("write bearer token file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close bearer token file: %w", err)
	}
	complete = true
	return nil
}
