package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/higordiegoti/keyrus/services/consolidation/internal/adapters/inbound/rabbitmq"
)

type fakeHealthStore struct {
	ready error
}

func (s fakeHealthStore) Ready(context.Context) error { return s.ready }
func (s fakeHealthStore) PendingBacklog(context.Context) (int64, int64, error) {
	return 0, 0, nil
}

type fakeReadyConsumer struct{ ready error }

func (c fakeReadyConsumer) Ready(context.Context) error { return c.ready }

func TestReadinessRequiresStoreAndAtLeastOneConsumer(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)

	unavailableStore := operationsServer(":0", fakeHealthStore{ready: errors.New("postgres down")}, []readyConsumer{fakeReadyConsumer{}}, &rabbitmq.Metrics{})
	response := httptest.NewRecorder()
	unavailableStore.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable store reported ready: status=%d", response.Code)
	}

	noConsumers := operationsServer(":0", fakeHealthStore{}, nil, &rabbitmq.Metrics{})
	response = httptest.NewRecorder()
	noConsumers.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("zero RabbitMQ consumer capacity reported ready: status=%d", response.Code)
	}

	partialConsumers := operationsServer(":0", fakeHealthStore{}, []readyConsumer{
		fakeReadyConsumer{ready: errors.New("connecting")},
		fakeReadyConsumer{},
	}, &rabbitmq.Metrics{})
	response = httptest.NewRecorder()
	partialConsumers.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("at least one ready consumer must report the process ready: status=%d", response.Code)
	}
}

func TestConfigurationRequiresBackoffOrdering(t *testing.T) {
	t.Setenv("CONSOLIDATION_DATABASE_URL", "postgres://consumer:secret@localhost/cashflow")
	t.Setenv("CONSUMER_RABBITMQ_URL", "amqp://consumer:secret@localhost/outbox_test")
	t.Setenv("CONSUMER_RABBITMQ_ALLOW_INSECURE", "true")
	t.Setenv("CONSUMER_BACKOFF_BASE", "10s")
	t.Setenv("CONSUMER_BACKOFF_MAX", "1s")
	if _, err := loadConfig(); err == nil {
		t.Fatal("configuration accepted CONSUMER_BACKOFF_MAX below CONSUMER_BACKOFF_BASE")
	}
}

func TestConfigurationDefaults(t *testing.T) {
	t.Setenv("CONSOLIDATION_DATABASE_URL", "postgres://consumer:secret@localhost/cashflow")
	t.Setenv("CONSUMER_RABBITMQ_URL", "amqp://consumer:secret@localhost/outbox_test")
	configuration, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if configuration.workers != 4 || configuration.maxAttempts != 5 {
		t.Fatalf("unexpected defaults: workers=%d maxAttempts=%d", configuration.workers, configuration.maxAttempts)
	}
}

type cancelableConsumer struct {
	started chan<- struct{}
	active  *atomic.Int32
}

func (c cancelableConsumer) Run(ctx context.Context) error {
	c.active.Add(1)
	c.started <- struct{}{}
	defer c.active.Add(-1)
	<-ctx.Done()
	return ctx.Err()
}

func TestRuntimeJoinsEveryCanceledConsumer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 3)
	active := &atomic.Int32{}
	consumers := []rabbitmq.Runner{
		cancelableConsumer{started: started, active: active},
		cancelableConsumer{started: started, active: active},
		cancelableConsumer{started: started, active: active},
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.NewServeMux()}
	result := make(chan error, 1)
	go func() {
		result <- (rabbitmq.Runtime{
			Server: server, Listener: listener, Consumers: consumers, ShutdownTimeout: time.Second,
		}).Run(ctx)
	}()
	for range consumers {
		<-started
	}
	if got := active.Load(); got != int32(len(consumers)) {
		t.Fatalf("not all consumers started: active=%d", got)
	}
	cancel()
	if err := <-result; err != nil {
		t.Fatalf("runtime did not join consumers after cancellation: %v", err)
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("consumer leaked after join: active=%d", got)
	}
}
