package setup

import (
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RndmJoker/proton-mail-bridge-docker/internal/login"
)

// A setup server that has gone away has to be distinguishable from one that
// answered with a refusal.
//
// proton-login depends on that difference. The page shutting itself down is how
// a successful sign-in ends, so an unreachable server in the middle of one is a
// result rather than a failure. Without this, a sign-in that had just succeeded
// was reported as "connection refused". See #35.
func TestUnreachableIsMarkedAsSuch(t *testing.T) {
	certDir := filepath.Join(t.TempDir(), "setup-tls")

	// NewServer is what writes the certificate the client then pins.
	if _, err := NewServer(login.New(&stubBridge{}), Options{
		BindAddress: "127.0.0.1",
		Port:        8443,
		Token:       "a-token",
		CertDir:     certDir,
	}); err != nil {
		t.Fatalf("could not build the server: %v", err)
	}

	// A port nothing listens on. Asked for and released again rather than
	// hard-coded, so this cannot collide with whatever else the machine runs.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("could not reserve a port: %v", err)
	}

	address := listener.Addr().String()

	if err := listener.Close(); err != nil {
		t.Fatalf("could not release the port again: %v", err)
	}

	client, err := NewClient("https://"+address, certDir, "a-token")
	if err != nil {
		t.Fatalf("could not build the client: %v", err)
	}

	_, err = client.Status()
	if err == nil {
		t.Fatal("a call to a closed port should not succeed")
	}

	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("got %v, want an error wrapping ErrUnreachable", err)
	}

	// And the cause is kept in the message, so a reader still learns which
	// address failed rather than only that something did.
	//
	// Not checked with errors.Unwrap: the error joins two causes with %w, which
	// gives it an Unwrap returning a slice, and the singular errors.Unwrap
	// answers nil for that by design.
	if !strings.Contains(err.Error(), address) {
		t.Fatalf("got %q, want the address %q in the message", err, address)
	}
}
