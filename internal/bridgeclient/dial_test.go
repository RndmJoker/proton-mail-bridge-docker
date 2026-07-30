package bridgeclient

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"
)

// selfSignedPEM builds a certificate shaped like the one the bridge generates:
// self-signed, common name 127.0.0.1, that address as its only IP.
func selfSignedPEM(t *testing.T) string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("could not generate a key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("could not create a certificate: %v", err)
	}

	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestTransportCredentials(t *testing.T) {
	t.Run("accepts the certificate the bridge writes", func(t *testing.T) {
		config := ServerConfig{Cert: selfSignedPEM(t), Token: "t", FileSocketPath: "/tmp/bridge0042"}

		if _, err := config.transportCredentials(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	// A config whose certificate does not parse must fail here rather than at
	// the first call, where it would surface as a transport error with nothing
	// pointing at the cause.
	t.Run("rejects something that is not a certificate", func(t *testing.T) {
		config := ServerConfig{Cert: "not a certificate", Token: "t", FileSocketPath: "/tmp/bridge0042"}

		if _, err := config.transportCredentials(); err == nil {
			t.Fatal("expected an error, got none")
		}
	})
}

func TestDialRejectsIncompleteConfig(t *testing.T) {
	// Dial connects lazily, so a bad address would not be noticed here. What
	// must be noticed is a config that cannot work at all: without the token
	// every call comes back Unauthenticated, and the cause would be nowhere
	// near the symptom.
	if _, err := Dial(ServerConfig{Cert: selfSignedPEM(t), FileSocketPath: "/tmp/bridge0042"}); err == nil {
		t.Fatal("expected an error, got none")
	}
}

func TestDialBuildsAClient(t *testing.T) {
	config := ServerConfig{Cert: selfSignedPEM(t), Token: "t", FileSocketPath: "/tmp/bridge0042"}

	client, err := Dial(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	defer func() { _ = client.Close() }()

	if client.BridgeClient == nil {
		t.Fatal("no bridge client on the connection")
	}
}
