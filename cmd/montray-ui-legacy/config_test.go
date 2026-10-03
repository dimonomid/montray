package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigNotFound(t *testing.T) {
	_, err := loadConfig(filepath.Join(t.TempDir(), "missing.yml"))
	if err == nil {
		t.Fatal("loadConfig() succeeded for a missing config")
	}
	if !configNotFound(err) {
		t.Fatalf("configNotFound(%v) = false, want true", err)
	}
}

func TestWatchConfigReadErrorOnlySuggestsSetupForDefaultConfig(t *testing.T) {
	if got := watchConfigReadError("/custom/config.yml", os.ErrNotExist); strings.Contains(got.Error(), "setup") {
		t.Fatalf("custom config error = %q, unexpectedly contains setup guidance", got)
	}
	defaultConfigFilename, err := defaultWatchConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	got := watchConfigReadError(defaultConfigFilename, os.ErrNotExist)
	for _, want := range []string{
		"Hint: Run the following command to create the default configuration, desktop-autostart entry, and application launcher:\n\n    " + os.Args[0] + " setup",
		"To create only the default configuration without installing the desktop integration, run:\n\n    " + os.Args[0] + " setup create-config",
	} {
		if !strings.Contains(got.Error(), want) {
			t.Fatalf("default config error = %q, missing guidance %q", got, want)
		}
	}
}

func TestLoadWatchConfigRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "montray.yml")
	if err := os.WriteFile(path, []byte("wsClient:\n  serverz: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "serverz") {
		t.Fatalf("loadConfig error = %v, want unknown-field error", err)
	}
}

func TestLoadWatchConfigRejectsInvalidServerIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "montray.yml")
	data := "wsClient:\n  servers:\n    - id: remote\n      addr: first:41990\n    - id: remote\n      addr: second:41990\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), `id "remote" duplicates`) {
		t.Fatalf("loadConfig error = %v, want duplicate-ID error", err)
	}
}

func TestLoadWatchConfigUsesTunnelOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "montray.yml")
	data := `wsClient:
  servers:
    - id: remote
      addr: 127.0.0.1:41992
      tunnel:
        ssh:
          host: example.com
          user: montray
          port: 2222
          remoteServerAddr: 127.0.0.1:41990
          extraSshArgs: ["-i", "/etc/montray/key"]
    - id: custom
      addr: 127.0.0.1:41993
      tunnel:
        customCommand:
          command: ["my-tunnel", "--listen", "127.0.0.1:41993"]
          readinessProbe:
            containsOutput: "tunnel ready"
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	ssh := cfg.WSClient.Servers[0].Tunnel.SSH
	if ssh.Host != "example.com" || ssh.User != "montray" || ssh.Port != 2222 ||
		ssh.RemoteServerAddr != "127.0.0.1:41990" || len(ssh.ExtraSSHArgs) != 2 {
		t.Fatalf("SSH tunnel config = %#v", ssh)
	}
	custom := cfg.WSClient.Servers[1].Tunnel.CustomCommand
	if len(custom.Command) != 3 || custom.ReadinessProbe == nil ||
		custom.ReadinessProbe.ContainsOutput != "tunnel ready" {
		t.Fatalf("custom tunnel config = %#v", custom)
	}
}

func TestLoadWatchConfigUsesTLSOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "montray.yml")
	data := `wsClient:
  servers:
    - id: remote
      addr: 127.0.0.1:41990
      tls:
        caFile: /etc/montray/private-ca.pem
        serverName: montray.example.com
      auth:
        bearerTokenFile: /home/user/.config/montray-ui/tokens/remote.token
    - id: public-ca
      addr: montray.example.net:41990
      tls: {}
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig := cfg.WSClient.Servers[0].TLS
	if tlsConfig == nil || tlsConfig.CAFile != "/etc/montray/private-ca.pem" || tlsConfig.ServerName != "montray.example.com" {
		t.Fatalf("TLS config = %#v", tlsConfig)
	}
	if auth := cfg.WSClient.Servers[0].Auth; auth == nil || auth.BearerTokenFile != "/home/user/.config/montray-ui/tokens/remote.token" {
		t.Fatalf("auth config = %#v", auth)
	}
	if cfg.WSClient.Servers[1].TLS == nil {
		t.Fatal("empty tls object did not enable TLS")
	}
}
