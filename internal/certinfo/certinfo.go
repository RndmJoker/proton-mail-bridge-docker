// Package certinfo fetches the TLS certificate a running bridge presents on
// its mail ports, so that it can be compared against what a mail client shows.
//
// The certificate is self-signed and generated per installation, so every mail
// client will ask about it once. The only way to answer that question honestly
// is to have the fingerprint from the other side.
//
// It is read off the wire rather than exported over gRPC on purpose. The
// export call writes both the certificate and its private key to a directory,
// and a private key on disk is exactly what the rest of this container is
// built to avoid.
package certinfo

import (
	"bufio"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// DialTimeout bounds the whole exchange. Everything here happens over the
// loopback address inside one container, so a wait of more than a moment means
// something is wrong rather than slow.
const DialTimeout = 10 * time.Second

// Fingerprint returns the SHA-256 fingerprint of cert, formatted the way mail
// clients and openssl show it: uppercase hex, colon-separated.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)

	encoded := strings.ToUpper(hex.EncodeToString(sum[:]))

	parts := make([]string, 0, len(sum))
	for i := 0; i < len(encoded); i += 2 {
		parts = append(parts, encoded[i:i+2])
	}

	return strings.Join(parts, ":")
}

// StartTLS performs the IMAP STARTTLS exchange on an already-open connection
// and returns once the server has agreed to it.
//
// The bridge serves IMAP without direct TLS by default, so this is the usual
// case rather than the exotic one.
func StartTLS(conn net.Conn) error {
	reader := bufio.NewReader(conn)

	// The greeting comes first, unprompted, and has to be consumed before
	// anything is sent.
	greeting, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("no IMAP greeting: %w", err)
	}

	if !strings.HasPrefix(greeting, "* OK") {
		return fmt.Errorf("unexpected IMAP greeting: %s", strings.TrimSpace(greeting))
	}

	if _, err := io.WriteString(conn, "a001 STARTTLS\r\n"); err != nil {
		return fmt.Errorf("could not send STARTTLS: %w", err)
	}

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("no answer to STARTTLS: %w", err)
		}

		// Untagged lines may precede the tagged response.
		if !strings.HasPrefix(line, "a001 ") {
			continue
		}

		if !strings.HasPrefix(line, "a001 OK") {
			return fmt.Errorf("the server refused STARTTLS: %s", strings.TrimSpace(line))
		}

		return nil
	}
}

// Fetch returns the certificate the bridge presents on the given port.
//
// useSSL selects between a direct TLS handshake and STARTTLS, matching the
// bridge's own setting for that port.
func Fetch(address string, port int, useSSL bool) (*x509.Certificate, error) {
	// JoinHostPort rather than Sprintf: an IPv6 literal has to be bracketed,
	// and the bridge's address is only 127.0.0.1 by convention, not by rule.
	target := net.JoinHostPort(address, strconv.Itoa(port))

	conn, err := net.DialTimeout("tcp", target, DialTimeout)
	if err != nil {
		return nil, fmt.Errorf("could not connect to %s: %w", target, err)
	}

	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(time.Now().Add(DialTimeout)); err != nil {
		return nil, err
	}

	if !useSSL {
		if err := StartTLS(conn); err != nil {
			return nil, err
		}
	}

	// InsecureSkipVerify is correct here and only here: the certificate is
	// self-signed and this code exists to show it to a person, not to trust
	// it. Verifying would mean already knowing what we are trying to find out.
	tlsConn := tls.Client(conn, &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // the certificate is the subject of this call, not a trust decision
		ServerName:         address,
		MinVersion:         tls.VersionTLS12,
	})

	if err := tlsConn.Handshake(); err != nil {
		return nil, fmt.Errorf("TLS handshake with %s failed: %w", target, err)
	}

	certs := tlsConn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil, fmt.Errorf("%s presented no certificate", target)
	}

	return certs[0], nil
}
