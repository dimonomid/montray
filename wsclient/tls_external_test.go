package wsclient_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dimonomid/montray"
	"github.com/dimonomid/montray/backend/itemsboard"
	"github.com/dimonomid/montray/backend/messengers"
	webserver "github.com/dimonomid/montray/backend/messengers/webserver"
	"github.com/dimonomid/montray/wsclient"
)

func TestClientReceivesIncidentsFromTLSServerWithSelfSignedCertificate(t *testing.T) {
	certFile, keyFile := writeSelfSignedServerCertificate(t, "montray.test")
	board := itemsboard.New()
	board.Set([]*montray.ItemWContext{{
		Item:              montray.Item{Key: "disk", State: montray.ItemStateError, Details: "full"},
		IncidentStartedAt: time.Now(),
	}})
	notifications := make(chan *montray.Notification)
	serverDone := make(chan struct{})
	server, err := webserver.New(webserver.Params{
		Common: messengers.Params{
			Logger:            testLogger,
			ItemsBoard:        board,
			NotificationsChan: notifications,
			TornDown:          serverDone,
		},
		Config: webserver.Config{
			ListenAddress: "127.0.0.1:0",
			TLS:           &webserver.ConfigTLS{CertFile: certFile, KeyFile: keyFile},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		close(notifications)
		select {
		case <-serverDone:
		case <-time.After(3 * time.Second):
			t.Error("TLS server did not shut down")
		}
	})

	assertTLSConnectionFails(t, server.Addr().String(), &wsclient.ConfigTLS{ServerName: "montray.test"}, "certificate signed by unknown authority")
	assertTLSConnectionFails(t, server.Addr().String(), &wsclient.ConfigTLS{CAFile: certFile}, "doesn't contain any IP SANs")

	events := make(chan wsclient.ServerEvent, 16)
	client, err := wsclient.New(wsclient.Params{
		Config: wsclient.ConfigServer{
			ID:   "test",
			Addr: server.Addr().String(),
			TLS:  &wsclient.ConfigTLS{CAFile: certFile, ServerName: "montray.test"},
		},
		Logger:         testLogger,
		EventCh:        events,
		ReconnectDelay: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)

	deadline := time.After(3 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Kind != wsclient.ServerEventKindOngoingIncidents {
				continue
			}
			total := event.OngoingIncidents.OngoingIncidents.Total
			if len(total) != 1 || total[0].Key != "disk" {
				t.Fatalf("received incidents = %#v, want disk incident", total)
			}
			return
		case <-deadline:
			t.Fatal("client did not receive the incident snapshot over TLS")
		}
	}
}

func TestClientAuthenticatesToTLSServerWithBearerToken(t *testing.T) {
	certFile, keyFile := writeSelfSignedServerCertificate(t, "montray.test")
	token := "valid-generated-bearer-token"
	tokenHash := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(token)))
	board := itemsboard.New()
	board.Set([]*montray.ItemWContext{{
		Item:              montray.Item{Key: "disk", State: montray.ItemStateError, Details: "full"},
		IncidentStartedAt: time.Now(),
	}})
	notifications := make(chan *montray.Notification)
	serverDone := make(chan struct{})
	server, err := webserver.New(webserver.Params{
		Common: messengers.Params{
			Logger:            testLogger,
			ItemsBoard:        board,
			NotificationsChan: notifications,
			TornDown:          serverDone,
		},
		Config: webserver.Config{
			ListenAddress: "127.0.0.1:0",
			TLS:           &webserver.ConfigTLS{CertFile: certFile, KeyFile: keyFile},
			Auth:          []webserver.ConfigAuth{{ID: "laptop", BearerTokenHash: tokenHash}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		close(notifications)
		select {
		case <-serverDone:
		case <-time.After(3 * time.Second):
			t.Error("authenticated TLS server did not shut down")
		}
	})

	directory := t.TempDir()
	missingEvents := make(chan wsclient.ServerEvent, 16)
	missingClient, err := wsclient.New(wsclient.Params{
		Config: wsclient.ConfigServer{
			ID: "test", Addr: server.Addr().String(),
			TLS: &wsclient.ConfigTLS{CAFile: certFile, ServerName: "montray.test"},
		},
		Logger:         testLogger,
		EventCh:        missingEvents,
		ReconnectDelay: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForConnectionError(t, missingEvents, "server requires authentication, but no bearer token is configured")
	missingClient.Close()

	wrongTokenFile := filepath.Join(directory, "wrong.token")
	if err := os.WriteFile(wrongTokenFile, []byte("wrong-token"), 0600); err != nil {
		t.Fatal(err)
	}
	wrongEvents := make(chan wsclient.ServerEvent, 16)
	wrongClient, err := wsclient.New(wsclient.Params{
		Config: wsclient.ConfigServer{
			ID: "test", Addr: server.Addr().String(),
			TLS:  &wsclient.ConfigTLS{CAFile: certFile, ServerName: "montray.test"},
			Auth: &wsclient.ConfigAuth{BearerTokenFile: wrongTokenFile},
		},
		Logger:         testLogger,
		EventCh:        wrongEvents,
		ReconnectDelay: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForConnectionError(t, wrongEvents, "server rejected the configured bearer token")
	wrongClient.Close()

	tokenFile := filepath.Join(directory, "valid.token")
	if err := os.WriteFile(tokenFile, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	events := make(chan wsclient.ServerEvent, 16)
	client, err := wsclient.New(wsclient.Params{
		Config: wsclient.ConfigServer{
			ID: "test", Addr: server.Addr().String(),
			TLS:  &wsclient.ConfigTLS{CAFile: certFile, ServerName: "montray.test"},
			Auth: &wsclient.ConfigAuth{BearerTokenFile: tokenFile},
		},
		Logger:         testLogger,
		EventCh:        events,
		ReconnectDelay: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)

	deadline := time.After(3 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Kind != wsclient.ServerEventKindOngoingIncidents {
				continue
			}
			total := event.OngoingIncidents.OngoingIncidents.Total
			if len(total) != 1 || total[0].Key != "disk" {
				t.Fatalf("received incidents = %#v, want disk incident", total)
			}
			return
		case <-deadline:
			t.Fatal("authenticated client did not receive the incident snapshot over TLS")
		}
	}
}

func assertTLSConnectionFails(t *testing.T, address string, tlsConfig *wsclient.ConfigTLS, wantError string) {
	t.Helper()
	events := make(chan wsclient.ServerEvent, 16)
	client, err := wsclient.New(wsclient.Params{
		Config:         wsclient.ConfigServer{ID: "test", Addr: address, TLS: tlsConfig},
		Logger:         testLogger,
		EventCh:        events,
		ReconnectDelay: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	waitForConnectionError(t, events, wantError)
}

func waitForConnectionError(t *testing.T, events <-chan wsclient.ServerEvent, wantError string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	lastConnectionError := ""
	for {
		select {
		case event := <-events:
			if event.Kind == wsclient.ServerEventKindConnectionError {
				lastConnectionError = event.ConnectionError
				if strings.Contains(event.ConnectionError, wantError) {
					return
				}
			}
		case <-deadline:
			t.Fatalf("connection error = %q, want it to contain %q", lastConnectionError, wantError)
		}
	}
}

func writeSelfSignedServerCertificate(t *testing.T, serverName string) (string, string) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          serialNumber,
		Subject:               pkix.Name{CommonName: serverName},
		DNSNames:              []string{serverName},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}

	directory := t.TempDir()
	certFile := filepath.Join(directory, "server-cert.pem")
	keyFile := filepath.Join(directory, "server-key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privateKeyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}
