package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/higordiegoti/keyrus/services/ledger/internal/outbox"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, getenv func(string) string) error {
	if len(arguments) != 1 || (arguments[0] != "upgrade" && arguments[0] != "rollback") {
		return fmt.Errorf("usage: outbox-topology <upgrade|rollback>")
	}
	url := strings.TrimSpace(getenv("OUTBOX_RABBITMQ_URL"))
	if url == "" {
		return fmt.Errorf("OUTBOX_RABBITMQ_URL is required")
	}
	allowInsecure := false
	if raw := strings.TrimSpace(getenv("OUTBOX_RABBITMQ_ALLOW_INSECURE")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("OUTBOX_RABBITMQ_ALLOW_INSECURE must be a boolean")
		}
		allowInsecure = parsed
	}
	tlsConfig, err := loadTopologyTLS(getenv)
	if err != nil {
		return err
	}
	config := outbox.TopologyMigrationConfig{
		URL: url, AllowInsecure: allowInsecure,
		TLS: tlsConfig, ConfirmBudget: 10 * time.Second,
	}
	var report outbox.MigrationReport
	if arguments[0] == "upgrade" {
		report, err = outbox.UpgradeTopology(ctx, config)
	} else {
		report, err = outbox.RollbackTopology(ctx, config)
	}
	if err != nil {
		return fmt.Errorf("%s failed after %d confirmed message moves: %w", arguments[0], report.Moved, err)
	}
	_, _ = fmt.Fprintf(os.Stdout, "%s complete: confirmed_moves=%d\n", report.Direction, report.Moved)
	return nil
}

func loadTopologyTLS(getenv func(string) string) (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if caPath := strings.TrimSpace(getenv("OUTBOX_RABBITMQ_CA_FILE")); caPath != "" {
		content, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf("read RabbitMQ CA: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("load system roots: %w", err)
		}
		if !roots.AppendCertsFromPEM(content) {
			return nil, errors.New("RabbitMQ CA file contains no certificates")
		}
		config.RootCAs = roots
	}
	certPath := strings.TrimSpace(getenv("OUTBOX_RABBITMQ_CERT_FILE"))
	keyPath := strings.TrimSpace(getenv("OUTBOX_RABBITMQ_KEY_FILE"))
	if (certPath == "") != (keyPath == "") {
		return nil, errors.New("RabbitMQ client certificate and key must be configured together")
	}
	if certPath != "" {
		certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, fmt.Errorf("load RabbitMQ client certificate: %w", err)
		}
		config.Certificates = []tls.Certificate{certificate}
	}
	return config, nil
}
