package setup

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Certificate lifetime. Long, on purpose: this certificate is trusted by
// fingerprint rather than by an authority, and an expiry that arrives
// unannounced would break the only way into a container that has locked
// itself out.
const certValidity = 10 * 365 * 24 * time.Hour

const (
	certFileName = "cert.pem"
	keyFileName  = "key.pem"
)

// TLSMaterial is the certificate the setup page presents, plus the fingerprint
// to compare it against.
type TLSMaterial struct {
	Certificate tls.Certificate

	// Fingerprint is the SHA-256 of the certificate, uppercase hex with
	// colons, the way browsers and openssl show it.
	Fingerprint string
}

// LoadOrCreateCertificate returns the certificate in dir, creating it if it is
// not there yet.
//
// Kept in the volume rather than generated per start so that the fingerprint
// stays the same. A fingerprint that changes on every restart is one nobody
// can check, and a warning nobody can check is a warning everybody clicks
// through.
func LoadOrCreateCertificate(dir, hostname string) (TLSMaterial, error) {
	certPath := filepath.Join(dir, certFileName)
	keyPath := filepath.Join(dir, keyFileName)

	material, err := loadCertificate(certPath, keyPath)
	if err == nil {
		return material, nil
	}

	if !errors.Is(err, os.ErrNotExist) {
		return TLSMaterial{}, fmt.Errorf("could not read the existing setup certificate: %w", err)
	}

	return createCertificate(dir, certPath, keyPath, hostname)
}

func loadCertificate(certPath, keyPath string) (TLSMaterial, error) {
	certPEM, err := os.ReadFile(certPath) //nolint:gosec // path is ours
	if err != nil {
		return TLSMaterial{}, err
	}

	keyPEM, err := os.ReadFile(keyPath) //nolint:gosec // path is ours
	if err != nil {
		return TLSMaterial{}, err
	}

	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return TLSMaterial{}, fmt.Errorf("the stored setup certificate is unusable: %w", err)
	}

	return TLSMaterial{
		Certificate: certificate,
		Fingerprint: FingerprintOf(certificate.Certificate[0]),
	}, nil
}

func createCertificate(dir, certPath, keyPath, hostname string) (TLSMaterial, error) {
	// 0700: the private key is in here, and the volume already holds a GPG key
	// without a passphrase. Nothing in it is readable by anyone else.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return TLSMaterial{}, err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return TLSMaterial{}, fmt.Errorf("could not generate a key for the setup page: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return TLSMaterial{}, err
	}

	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: hostname, Organization: []string{"proton-mail-bridge-docker"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(certValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	// Both a name and an address, because the page is reached as 127.0.0.1
	// from inside the container and by whatever the operator forwards from
	// outside.
	if ip := net.ParseIP(hostname); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{hostname}
	}

	template.IPAddresses = append(template.IPAddresses, net.ParseIP("127.0.0.1"))

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return TLSMaterial{}, fmt.Errorf("could not create the setup certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return TLSMaterial{}, err
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return TLSMaterial{}, err
	}

	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return TLSMaterial{}, err
	}

	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return TLSMaterial{}, err
	}

	return TLSMaterial{
		Certificate: certificate,
		Fingerprint: FingerprintOf(der),
	}, nil
}

// FingerprintOf formats the SHA-256 of a DER certificate the way browsers and
// openssl show it.
func FingerprintOf(der []byte) string {
	sum := sha256.Sum256(der)

	encoded := strings.ToUpper(hex.EncodeToString(sum[:]))

	parts := make([]string, 0, len(sum))
	for i := 0; i < len(encoded); i += 2 {
		parts = append(parts, encoded[i:i+2])
	}

	return strings.Join(parts, ":")
}

// parseLeaf returns the parsed leaf certificate. Used by tests and by anything
// that needs to look at what was issued rather than at the fingerprint alone.
func parseLeaf(material TLSMaterial) (*x509.Certificate, error) {
	if len(material.Certificate.Certificate) == 0 {
		return nil, errors.New("no certificate")
	}

	return x509.ParseCertificate(material.Certificate.Certificate[0])
}
