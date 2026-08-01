package identityruntime

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"
)

func RootCAs(path string) (*x509.CertPool, error) {
	if path == "" {
		return nil, errors.New("identityruntime: CA file is required")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("identityruntime: read CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(contents) {
		return nil, errors.New("identityruntime: CA file contains no certificate")
	}
	return pool, nil
}

func HTTPClient(caFile string) (*http.Client, error) {
	roots, err := RootCAs(caFile)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
		}},
	}, nil
}

func ServerTLS(certFile, keyFile, clientCAFile string) (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("identityruntime: load server certificate: %w", err)
	}
	clientRoots, err := RootCAs(clientCAFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{certificate},
		ClientCAs:    clientRoots,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}, nil
}

func ClientTLS(certFile, keyFile, caFile, serverName string) (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("identityruntime: load client certificate: %w", err)
	}
	roots, err := RootCAs(caFile)
	if err != nil {
		return nil, err
	}
	if serverName == "" {
		return nil, errors.New("identityruntime: TLS server name is required")
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{certificate},
		RootCAs:      roots,
		ServerName:   serverName,
	}, nil
}
