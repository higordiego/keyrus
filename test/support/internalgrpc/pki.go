// Package internalgrpc starts the private gRPC surface over a real mutual TLS
// connection so identity, scope, deadline and size limits are exercised against
// the production interceptors rather than against a stub.
package internalgrpc

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"time"
)

// PKI is an ephemeral certificate authority with one server and one client
// certificate. It exists only for the lifetime of a test process.
type PKI struct {
	pool        *x509.CertPool
	serverCert  tls.Certificate
	clientCert  tls.Certificate
	serverNames []string
}

// NewPKI issues the certificate authority and both leaf certificates.
func NewPKI(serverName string) (*PKI, error) {
	authorityKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	authorityTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "cashflow-internal-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	authorityDER, err := x509.CreateCertificate(rand.Reader, authorityTemplate, authorityTemplate, &authorityKey.PublicKey, authorityKey)
	if err != nil {
		return nil, err
	}
	authority, err := x509.ParseCertificate(authorityDER)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(authority)

	serverCert, err := issueLeaf(authority, authorityKey, 2, serverName, []string{serverName}, x509.ExtKeyUsageServerAuth)
	if err != nil {
		return nil, err
	}
	clientCert, err := issueLeaf(authority, authorityKey, 3, "cashflow-consolidation-svc", nil, x509.ExtKeyUsageClientAuth)
	if err != nil {
		return nil, err
	}
	return &PKI{pool: pool, serverCert: serverCert, clientCert: clientCert, serverNames: []string{serverName}}, nil
}

func issueLeaf(authority *x509.Certificate, authorityKey *rsa.PrivateKey, serial int64, commonName string, dnsNames []string, usage x509.ExtKeyUsage) (tls.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		DNSNames:     dnsNames,
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, authority, &key.PublicKey, authorityKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

// ServerTLS requires and verifies a client certificate, which is what makes the
// transport mutual rather than merely encrypted.
func (p *PKI) ServerTLS() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{p.serverCert},
		ClientCAs:    p.pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}
}

// ClientTLS presents the service certificate.
func (p *PKI) ClientTLS() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{p.clientCert},
		RootCAs:      p.pool,
		ServerName:   p.serverNames[0],
		MinVersion:   tls.VersionTLS13,
	}
}

// ClientTLSWithoutCertificate trusts the server but presents no client identity,
// which the server must refuse.
func (p *PKI) ClientTLSWithoutCertificate() *tls.Config {
	return &tls.Config{
		RootCAs:    p.pool,
		ServerName: p.serverNames[0],
		MinVersion: tls.VersionTLS13,
	}
}
