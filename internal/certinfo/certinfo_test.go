package certinfo

import (
	"bufio"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func TestFingerprint(t *testing.T) {
	// An empty certificate still has a Raw of its own once parsed; here the
	// bytes are set directly, because the format of the output is what is
	// under test, not the parsing.
	cert := &x509.Certificate{Raw: []byte("proton mail bridge")}

	got := Fingerprint(cert)

	// 32 bytes as two hex digits each, separated by 31 colons.
	if len(got) != 32*2+31 {
		t.Fatalf("unexpected length %d: %s", len(got), got)
	}

	if strings.ToUpper(got) != got {
		t.Fatalf("not uppercase: %s", got)
	}

	for _, part := range strings.Split(got, ":") {
		if len(part) != 2 {
			t.Fatalf("group %q is not two digits, in %s", part, got)
		}
	}

	// Same input, same fingerprint: the point of showing one at all.
	if second := Fingerprint(cert); second != got {
		t.Fatalf("not stable: %s then %s", got, second)
	}
}

func TestFingerprintDiffersPerCertificate(t *testing.T) {
	a := Fingerprint(&x509.Certificate{Raw: []byte("one")})
	b := Fingerprint(&x509.Certificate{Raw: []byte("two")})

	if a == b {
		t.Fatal("two different certificates produced the same fingerprint")
	}
}

// fakeIMAP answers on a pipe the way the bridge does: a greeting, then a
// tagged OK for STARTTLS. answer is what it replies with.
func fakeIMAP(t *testing.T, greeting, answer string) net.Conn {
	t.Helper()

	server, client := net.Pipe()

	go func() {
		defer func() { _ = server.Close() }()

		if _, err := server.Write([]byte(greeting)); err != nil {
			return
		}

		reader := bufio.NewReader(server)

		if _, err := reader.ReadString('\n'); err != nil {
			return
		}

		_, _ = server.Write([]byte(answer))
	}()

	t.Cleanup(func() { _ = client.Close() })

	return client
}

func TestStartTLS(t *testing.T) {
	tests := []struct {
		name     string
		greeting string
		answer   string
		wantErr  bool
	}{
		{
			name:     "the server agrees",
			greeting: "* OK Proton Mail Bridge\r\n",
			answer:   "a001 OK Begin TLS negotiation now\r\n",
		},
		{
			// Untagged lines before the tagged response are legal and have to
			// be skipped rather than mistaken for the answer.
			name:     "an untagged line comes first",
			greeting: "* OK Proton Mail Bridge\r\n",
			answer:   "* CAPABILITY IMAP4rev1\r\na001 OK Begin TLS negotiation now\r\n",
		},
		{
			name:     "the server refuses",
			greeting: "* OK Proton Mail Bridge\r\n",
			answer:   "a001 NO STARTTLS is not available\r\n",
			wantErr:  true,
		},
		{
			// Anything that is not an IMAP greeting means this is not the
			// port we think it is. Continuing into a TLS handshake there would
			// report a confusing error much further along.
			name:     "not an IMAP server at all",
			greeting: "220 smtp ready\r\n",
			answer:   "",
			wantErr:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conn := fakeIMAP(t, test.greeting, test.answer)

			if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatalf("could not set a deadline: %v", err)
			}

			err := StartTLS(conn)

			if test.wantErr && err == nil {
				t.Fatal("expected an error, got none")
			}

			if !test.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// selfSignedCert returns a certificate and its key, for a TLS server to serve.
func selfSignedCert(t *testing.T) tls.Certificate {
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

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// TestFetchDirectTLS runs against a real TLS listener, which is the only way
// to prove that what comes back is the certificate that was served.
func TestFetchDirectTLS(t *testing.T) {
	serverCert := selfSignedCert(t)

	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("could not listen: %v", err)
	}

	defer func() { _ = listener.Close() }()

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}

		_ = conn.(*tls.Conn).Handshake()
		_ = conn.Close()
	}()

	port := listener.Addr().(*net.TCPAddr).Port

	cert, err := Fetch("127.0.0.1", port, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if want := Fingerprint(&x509.Certificate{Raw: serverCert.Certificate[0]}); Fingerprint(cert) != want {
		t.Fatalf("got a different certificate than the one served")
	}
}

func TestFetchOnAClosedPort(t *testing.T) {
	// Bind and immediately release, so the port is one nothing is listening on
	// while still being a plausible number.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("could not listen: %v", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	if _, err := Fetch("127.0.0.1", port, true); err == nil {
		t.Fatal("expected an error, got none")
	}
}
