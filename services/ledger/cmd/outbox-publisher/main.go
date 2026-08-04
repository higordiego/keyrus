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
	"github.com/higordiegoti/keyrus/internal/platform/observability/redact"
	"github.com/higordiegoti/keyrus/services/ledger/internal/outbox"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/exaring/otelpgx"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
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
	workerID       string
	workers        int
	batchSize      int
	lease          time.Duration
	pollInterval   time.Duration
	confirmTimeout time.Duration
	backoffBase    time.Duration
	backoffMax     time.Duration
}

func main() {
	logger := slog.New(redact.NewHandler(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	configuration, err := loadConfig()
	if err != nil {
		logger.Error("invalid outbox publisher configuration", "error", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, configuration, logger); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("outbox publisher stopped", "error", err)
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
	poolConfig.ConnConfig.Tracer = otelpgx.NewTracer()
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return fmt.Errorf("open PostgreSQL pool: %w", err)
	}
	defer pool.Close()
	store, err := outbox.NewPostgresStore(pool)
	if err != nil {
		return err
	}
	schema, err := apievents.LedgerEntryConfirmedV1Schema()
	if err != nil {
		return err
	}
	metrics := &outbox.Metrics{}
	brokers := make([]*outbox.RabbitBroker, 0, configuration.workers)
	workers := make([]*outbox.Worker, 0, configuration.workers)
	readinessBrokers := make([]outbox.Broker, 0, configuration.workers)
	for index := range configuration.workers {
		broker, err := outbox.NewRabbitBroker(outbox.RabbitConfig{
			URL:            configuration.rabbitURL,
			AllowInsecure:  configuration.allowInsecure,
			TLS:            configuration.tlsConfig,
			Topology:       outbox.DefaultTopology(),
			Schema:         schema,
			ConfirmTimeout: configuration.confirmTimeout,
		})
		if err != nil {
			return err
		}
		brokers = append(brokers, broker)
		readinessBrokers = append(readinessBrokers, broker)
		worker, err := outbox.NewWorker(store, broker, outbox.WorkerConfig{
			Owner:         fmt.Sprintf("%s-%d", configuration.workerID, index),
			BatchSize:     configuration.batchSize,
			Lease:         configuration.lease,
			PollInterval:  configuration.pollInterval,
			BackoffBase:   configuration.backoffBase,
			BackoffMax:    configuration.backoffMax,
			PublishBudget: configuration.confirmTimeout,
		}, metrics, logger)
		if err != nil {
			return err
		}
		workers = append(workers, worker)
	}
	defer func() {
		for _, broker := range brokers {
			_ = broker.Close()
		}
	}()

	server := operationsServer(configuration.httpAddress, store, readinessBrokers, metrics)
	logger.Info("outbox operations server listening", "address", configuration.httpAddress)
	runners := make([]outbox.Runner, 0, len(workers))
	for _, worker := range workers {
		runners = append(runners, worker)
	}
	return (outbox.Runtime{
		Server: server, Workers: runners, Brokers: readinessBrokers,
		ShutdownTimeout: 10 * time.Second,
	}).Run(ctx)
}

func operationsServer(address string, store outbox.Store, brokers []outbox.Broker, metrics *outbox.Metrics) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /readyz", func(writer http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		defer cancel()
		if err := store.Ready(ctx); err != nil {
			http.Error(writer, "outbox store unavailable", http.StatusServiceUnavailable)
			return
		}
		if len(brokers) == 0 {
			http.Error(writer, "RabbitMQ capacity unavailable", http.StatusServiceUnavailable)
			return
		}
		for _, broker := range brokers {
			if err := broker.Ready(ctx); err != nil {
				http.Error(writer, "RabbitMQ capacity unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /metrics", func(writer http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		defer cancel()
		stats, err := store.Stats(ctx, time.Now().UTC())
		if err != nil {
			http.Error(writer, "metrics unavailable", http.StatusServiceUnavailable)
			return
		}
		metrics.UpdateStats(stats)
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
		postgresDSN: strings.TrimSpace(os.Getenv("OUTBOX_POSTGRES_DSN")),
		rabbitURL:   strings.TrimSpace(os.Getenv("OUTBOX_RABBITMQ_URL")),
		httpAddress: envOr("OUTBOX_HTTP_ADDRESS", ":8080"),
		workerID:    envOr("OUTBOX_WORKER_ID", hostname+"-"+strconv.Itoa(os.Getpid())),
	}
	if configuration.postgresDSN == "" {
		return config{}, errors.New("OUTBOX_POSTGRES_DSN is required")
	}
	if configuration.rabbitURL == "" {
		return config{}, errors.New("OUTBOX_RABBITMQ_URL is required")
	}
	var err error
	if configuration.allowInsecure, err = parseBool("OUTBOX_RABBITMQ_ALLOW_INSECURE", false); err != nil {
		return config{}, err
	}
	if configuration.workers, err = parseInt("OUTBOX_WORKERS", 2, 1, 32); err != nil {
		return config{}, err
	}
	if configuration.batchSize, err = parseInt("OUTBOX_BATCH_SIZE", 50, 1, 1000); err != nil {
		return config{}, err
	}
	if configuration.lease, err = parseDuration("OUTBOX_LEASE", 30*time.Second); err != nil {
		return config{}, err
	}
	if configuration.pollInterval, err = parseDuration("OUTBOX_POLL_INTERVAL", 250*time.Millisecond); err != nil {
		return config{}, err
	}
	if configuration.confirmTimeout, err = parseDuration("OUTBOX_CONFIRM_TIMEOUT", 10*time.Second); err != nil {
		return config{}, err
	}
	if configuration.backoffBase, err = parseDuration("OUTBOX_BACKOFF_BASE", 500*time.Millisecond); err != nil {
		return config{}, err
	}
	if configuration.backoffMax, err = parseDuration("OUTBOX_BACKOFF_MAX", 30*time.Second); err != nil {
		return config{}, err
	}
	if configuration.backoffMax < configuration.backoffBase {
		return config{}, errors.New("OUTBOX_BACKOFF_MAX must be at least OUTBOX_BACKOFF_BASE")
	}
	if configuration.lease < configuration.confirmTimeout+2*time.Second {
		return config{}, errors.New("OUTBOX_LEASE must exceed OUTBOX_CONFIRM_TIMEOUT by at least 2s")
	}
	configuration.tlsConfig, err = loadRabbitTLS()
	if err != nil {
		return config{}, err
	}
	return configuration, nil
}

func loadRabbitTLS() (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	caPath := strings.TrimSpace(os.Getenv("OUTBOX_RABBITMQ_CA_FILE"))
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
	certPath := strings.TrimSpace(os.Getenv("OUTBOX_RABBITMQ_CERT_FILE"))
	keyPath := strings.TrimSpace(os.Getenv("OUTBOX_RABBITMQ_KEY_FILE"))
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
		resource.WithAttributes(semconv.ServiceName("outbox-publisher")),
	)
	if err != nil {
		return nil, fmt.Errorf("create OpenTelemetry resource: %w", err)
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(resources))
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
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
