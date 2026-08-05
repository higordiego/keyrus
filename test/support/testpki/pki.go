// Package testpki writes an ephemeral CA and leaf certificates for container
// integration tests. Nothing it creates is suitable for deployment.
package testpki

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

type Pair struct {
	CertFile string
	KeyFile  string
}

type Bundle struct {
	CA            string
	Keycloak      Pair
	Ledger        Pair
	Consolidation Pair
}

// defaultValidity keeps New's existing behavior for every current
// automated-test caller: short-lived, matching this package's "nothing here
// is suitable for deployment" contract.
const defaultValidity = 2 * time.Hour

func New(directory string) (Bundle, error) {
	return NewWithValidity(directory, defaultValidity)
}

// NewWithValidity is New with a caller-chosen certificate lifetime. It
// exists for scripts/generate-compose-secrets.go: that script documents
// itself in the README as the one-time setup step before a `docker compose
// up` meant to run as a developer's local environment for an entire working
// session, potentially many hours. New's 2-hour window silently expired
// mid-session (a real incident found while gathering T11 load evidence:
// every authenticated request started failing TLS verification once the CA
// aged past its NotAfter). Automated test callers should keep using New;
// only a long-running manual Compose session needs a longer-lived bundle.
func NewWithValidity(directory string, validity time.Duration) (Bundle, error) {
	authorityKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return Bundle{}, err
	}
	now := time.Now()
	authorityTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "cashflow-test-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(validity),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	authorityDER, err := x509.CreateCertificate(rand.Reader, authorityTemplate, authorityTemplate, &authorityKey.PublicKey, authorityKey)
	if err != nil {
		return Bundle{}, err
	}
	authority, err := x509.ParseCertificate(authorityDER)
	if err != nil {
		return Bundle{}, err
	}
	caPath := filepath.Join(directory, "ca.pem")
	if err := writePEM(caPath, "CERTIFICATE", authorityDER, 0o600); err != nil {
		return Bundle{}, err
	}
	keycloak, err := issue(directory, "keycloak", 2, authority, authorityKey, []string{"keycloak", "localhost", "edge.cashflow.local"}, []net.IP{net.ParseIP("127.0.0.1")}, x509.ExtKeyUsageServerAuth, validity)
	if err != nil {
		return Bundle{}, err
	}
	ledger, err := issue(directory, "ledger-api", 3, authority, authorityKey, []string{"ledger-api", "localhost"}, []net.IP{net.ParseIP("127.0.0.1")}, x509.ExtKeyUsageServerAuth, validity)
	if err != nil {
		return Bundle{}, err
	}
	consolidation, err := issue(directory, "consolidation", 4, authority, authorityKey, nil, nil, x509.ExtKeyUsageClientAuth, validity)
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{CA: caPath, Keycloak: keycloak, Ledger: ledger, Consolidation: consolidation}, nil
}

func issue(directory, name string, serial int64, authority *x509.Certificate, authorityKey *rsa.PrivateKey, dns []string, ips []net.IP, usage x509.ExtKeyUsage, validity time.Duration) (Pair, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return Pair{}, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(validity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		DNSNames:     dns,
		IPAddresses:  ips,
	}
	certificate, err := x509.CreateCertificate(rand.Reader, template, authority, &key.PublicKey, authorityKey)
	if err != nil {
		return Pair{}, err
	}
	certPath := filepath.Join(directory, name+".crt")
	keyPath := filepath.Join(directory, name+".key")
	if err := writePEM(certPath, "CERTIFICATE", certificate, 0o600); err != nil {
		return Pair{}, err
	}
	if err := writePEM(keyPath, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key), 0o600); err != nil {
		return Pair{}, err
	}
	return Pair{CertFile: certPath, KeyFile: keyPath}, nil
}

func writePEM(path, blockType string, contents []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if err := pem.Encode(file, &pem.Block{Type: blockType, Bytes: contents}); err != nil {
		_ = file.Close()
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return file.Close()
}
