package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrCreateCertificateCreatesOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "setup-tls")

	first, err := LoadOrCreateCertificate(dir, "127.0.0.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if first.Fingerprint == "" {
		t.Fatal("no fingerprint")
	}

	// The whole point of keeping it in the volume. A fingerprint that changes
	// on every start is one nobody can check, and a warning nobody can check
	// is a warning everybody clicks through.
	second, err := LoadOrCreateCertificate(dir, "127.0.0.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if second.Fingerprint != first.Fingerprint {
		t.Fatalf("the fingerprint changed: %s then %s", first.Fingerprint, second.Fingerprint)
	}
}

// The private key of the page that takes the Proton password. Everything in
// the volume is 0700 or 0600; this must not be the exception.
func TestCertificateFilesAreNotReadableByOthers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "setup-tls")

	if _, err := LoadOrCreateCertificate(dir, "127.0.0.1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for name, want := range map[string]os.FileMode{
		certFileName: 0o600,
		keyFileName:  0o600,
	} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("could not stat %s: %v", name, err)
		}

		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s is mode %04o, want %04o", name, got, want)
		}
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("could not stat the directory: %v", err)
	}

	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("the directory is mode %04o, want 0700", got)
	}
}

func TestFingerprintFormat(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "setup-tls")

	material, err := LoadOrCreateCertificate(dir, "127.0.0.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 32 bytes as two uppercase hex digits, colon-separated. It is printed for
	// a person to compare against what a browser shows, so the shape has to
	// match what browsers use.
	if len(material.Fingerprint) != 32*2+31 {
		t.Fatalf("unexpected length: %s", material.Fingerprint)
	}

	if strings.ToUpper(material.Fingerprint) != material.Fingerprint {
		t.Fatalf("not uppercase: %s", material.Fingerprint)
	}

	for _, part := range strings.Split(material.Fingerprint, ":") {
		if len(part) != 2 {
			t.Fatalf("group %q is not two digits", part)
		}
	}
}

// A certificate that cannot be parsed has to stop the server with a clear
// error. Silently generating a new one would change the fingerprint that
// somebody may have written down, which is exactly the event this design is
// built to make impossible.
func TestUnusableCertificateIsAnError(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, certFileName), []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("could not write: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, keyFileName), []byte("not a key"), 0o600); err != nil {
		t.Fatalf("could not write: %v", err)
	}

	if _, err := LoadOrCreateCertificate(dir, "127.0.0.1"); err == nil {
		t.Fatal("expected an error, got none")
	}
}

func TestCertificateCoversLoopbackAndTheGivenHost(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "setup-tls")

	material, err := LoadOrCreateCertificate(dir, "192.168.1.2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	leaf, err := parseLeaf(material)
	if err != nil {
		t.Fatalf("could not parse the certificate: %v", err)
	}

	var hasLoopback, hasGiven bool

	for _, ip := range leaf.IPAddresses {
		switch ip.String() {
		case "127.0.0.1":
			hasLoopback = true
		case "192.168.1.2":
			hasGiven = true
		}
	}

	// Reached as 127.0.0.1 from inside the container by proton-login, and as
	// the container address from outside when the page is exposed. One
	// certificate has to work for both.
	if !hasLoopback {
		t.Error("127.0.0.1 is missing, proton-login could not verify it")
	}

	if !hasGiven {
		t.Error("the given address is missing")
	}
}
