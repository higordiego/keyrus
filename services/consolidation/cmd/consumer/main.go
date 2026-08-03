// Command consumer is the Consolidation Consumer executable: it consumes
// ledger.entry.confirmed.v1 from RabbitMQ (published by the Ledger outbox
// publisher, T05) and hands validated events to the projector (T06A),
// ACKing only after the projector's transaction commits.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	apievents "github.com/higordiegoti/keyrus/api/events"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/adapters/inbound/rabbitmq"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/adapters/outbound/postgres"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/application"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

type config struct {
	postgresDSN    string
	rabbitURL      string
	allowInsecure  bool
	tlsConfig      *tls.Config
	httpAddress    string
	consumerID     string
	workers        int
	maxAttempts    int
	backoffBase    time.Duration
	backoffMax     time.Duration
	reconnectDelay time.Duration
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	configuration, err := loadConfig()
	if err != nil {
		logger.Error("invalid consolidation consumer configuration", "error", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, configuration, logger); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("consolidation consumer stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, configuration config, logger *slog.Logger) error {
	tracerShutdown, err := configureTracing(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tracerShutdown(context.Background()) }()

	poolConfig, err := pgxpool.ParseConfig(configuration.postgresDSN)
	if err != nil {
		return fmt.Errorf("parse PostgreSQL DSN: %w", err)
	}
	poolConfig.MaxConns = int32(max(configuration.workers*2, 4))
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return fmt.Errorf("open PostgreSQL pool: %w", err)
	}
	defer pool.Close()

	store, err := postgres.NewStore(pool)
	if err != nil {
		return err
	}
	projector, err := application.NewProjector(store)
	if err != nil {
		return err
	}
	schema, err := apievents.LedgerEntryConfirmedV1Schema()
	if err != nil {
		return err
	}
	metrics := &rabbitmq.Metrics{}
	consumers := make([]readyConsumer, 0, configuration.workers)
	runners := make([]rabbitmq.Runner, 0, configuration.workers)
	for index := range configuration.workers {
		consumer, err := rabbitmq.NewConsumer(rabbitmq.Config{
			URL: configuration.rabbitURL, AllowInsecure: configuration.allowInsecure,
			TLS: configuration.tlsConfig, Topology: rabbitmq.DefaultTopology(),
			Schema: schema, ConsumerTag: fmt.Sprintf("%s-%d", configuration.consumerID, index),
			MaxAttempts: configuration.maxAttempts, BackoffBase: configuration.backoffBase,
			BackoffMax: configuration.backoffMax, ReconnectDelay: configuration.reconnectDelay,
		}, projector, store, metrics, logger)
		if err != nil {
			return err
		}
		consumers = append(consumers, consumer)
		runners = append(runners, consumer)
	}

	server := operationsServer(configuration.httpAddress, store, consumers, metrics)
	logger.Info("consolidation consumer operations server listening", "address", configuration.httpAddress, "workers", configuration.workers)
	return (rabbitmq.Runtime{
		Server: server, Consumers: runners, ShutdownTimeout: 10 * time.Second,
	}).Run(ctx)
}

// healthStore and readyConsumer are narrow interfaces so tests can exercise
// operationsServer's readiness/metrics logic with fakes instead of a real
// PostgreSQL pool or AMQP connection.
type healthStore interface {
	Ready(context.Context) error
	PendingBacklog(context.Context) (retry, dlq int64, err error)
}

type readyConsumer interface {
	Ready(context.Context) error
}

func operationsServer(address string, store healthStore, consumers []readyConsumer, metrics *rabbitmq.Metrics) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /readyz", func(writer http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		defer cancel()
		if err := store.Ready(ctx); err != nil {
			http.Error(writer, "consolidation store unavailable", http.StatusServiceUnavailable)
			return
		}
		ready := false
		for _, consumer := range consumers {
			if consumer.Ready(ctx) == nil {
				ready = true
				break
			}
		}
		if !ready {
			http.Error(writer, "no RabbitMQ consumer capacity available", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /metrics", func(writer http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		defer cancel()
		retry, dlq, err := store.PendingBacklog(ctx)
		if err != nil {
			http.Error(writer, "metrics unavailable", http.StatusServiceUnavailable)
			return
		}
		metrics.UpdateBacklog(retry, dlq)
		writer.Header().Set("Content-Type", "text/plain; version=0.0.4")
		if err := metrics.WritePrometheus(writer); err != nil {
			return
		}
	})
	return &http.Server{
		Addr:              address,
		Handler:           mux,
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
}

func loadConfig() (config, error) {
	hostname, _ := os.Hostname()
	configuration := config{
		postgresDSN: strings.TrimSpace(os.Getenv("CONSOLIDATION_DATABASE_URL")),
		rabbitURL:   strings.TrimSpace(os.Getenv("CONSUMER_RABBITMQ_URL")),
		httpAddress: envOr("CONSUMER_HTTP_ADDRESS", ":8081"),
		consumerID:  envOr("CONSUMER_ID", hostname+"-"+strconv.Itoa(os.Getpid())),
	}
	if configuration.postgresDSN == "" {
		return config{}, errors.New("CONSOLIDATION_DATABASE_URL is required")
	}
	if configuration.rabbitURL == "" {
		return config{}, errors.New("CONSUMER_RABBITMQ_URL is required")
	}
	var err error
	if configuration.allowInsecure, err = parseBool("CONSUMER_RABBITMQ_ALLOW_INSECURE", false); err != nil {
		return config{}, err
	}
	if configuration.workers, err = parseInt("CONSUMER_WORKERS", 4, 1, 32); err != nil {
		return config{}, err
	}
	if configuration.maxAttempts, err = parseInt("CONSUMER_MAX_ATTEMPTS", 5, 1, 100); err != nil {
		return config{}, err
	}
	if configuration.backoffBase, err = parseDuration("CONSUMER_BACKOFF_BASE", 500*time.Millisecond); err != nil {
		return config{}, err
	}
	if configuration.backoffMax, err = parseDuration("CONSUMER_BACKOFF_MAX", 30*time.Second); err != nil {
		return config{}, err
	}
	if configuration.backoffMax < configuration.backoffBase {
		return config{}, errors.New("CONSUMER_BACKOFF_MAX must be at least CONSUMER_BACKOFF_BASE")
	}
	if configuration.reconnectDelay, err = parseDuration("CONSUMER_RECONNECT_DELAY", 2*time.Second); err != nil {
		return config{}, err
	}
	configuration.tlsConfig, err = loadRabbitTLS()
	if err != nil {
		return config{}, err
	}
	return configuration, nil
}

func loadRabbitTLS() (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	caPath := strings.TrimSpace(os.Getenv("CONSUMER_RABBITMQ_CA_FILE"))
	if caPath != "" {
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
	certPath := strings.TrimSpace(os.Getenv("CONSUMER_RABBITMQ_CERT_FILE"))
	keyPath := strings.TrimSpace(os.Getenv("CONSUMER_RABBITMQ_KEY_FILE"))
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

func configureTracing(ctx context.Context) (func(context.Context) error, error) {
	if strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")) == "" {
		return func(context.Context) error { return nil }, nil
	}
	exporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
	}
	resources, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(semconv.ServiceName("consolidation-consumer")),
	)
	if err != nil {
		return nil, fmt.Errorf("create OpenTelemetry resource: %w", err)
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(resources))
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func parseDuration(name string, fallback time.Duration) (time.Duration, error) {
	value := envOr(name, fallback.String())
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return parsed, nil
}

func parseInt(name string, fallback, minimum, maximum int) (int, error) {
	value, err := strconv.Atoi(envOr(name, strconv.Itoa(fallback)))
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d", name, minimum, maximum)
	}
	return value, nil
}

func parseBool(name string, fallback bool) (bool, error) {
	value, err := strconv.ParseBool(envOr(name, strconv.FormatBool(fallback)))
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	return value, nil
}
