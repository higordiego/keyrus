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

	"github.com/higordiegoti/keyrus/services/ledger/internal/outbox"
)

type readinessStore struct{ ready error }

func (s readinessStore) Claim(context.Context, string, int, time.Duration) ([]outbox.Event, error) {
	return nil, nil
}
func (s readinessStore) MarkPublished(context.Context, string, string) error { return nil }
func (s readinessStore) MarkFailed(context.Context, string, string, time.Duration, string) error {
	return nil
}
func (s readinessStore) Ready(context.Context) error { return s.ready }
func (s readinessStore) Stats(context.Context, time.Time) (outbox.Stats, error) {
	return outbox.Stats{}, nil
}

type readinessBroker struct{ ready error }

func (b readinessBroker) Publish(context.Context, outbox.Event) error { return nil }
func (b readinessBroker) Ready(context.Context) error                 { return b.ready }
func (b readinessBroker) Close() error                                { return nil }

type cancelableWorker struct {
	started chan<- struct{}
	active  *atomic.Int32
}

func (w cancelableWorker) Run(ctx context.Context) error {
	w.active.Add(1)
	w.started <- struct{}{}
	defer w.active.Add(-1)
	<-ctx.Done()
	return ctx.Err()
}

func TestReadinessRequiresEveryWorkerBroker(t *testing.T) {
	empty := operationsServer(":0", readinessStore{}, nil, &outbox.Metrics{})
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	empty.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("zero RabbitMQ capacity reported ready: status=%d", response.Code)
	}

	server := operationsServer(":0", readinessStore{}, []outbox.Broker{
		readinessBroker{},
		readinessBroker{ready: errors.New("second broker unavailable")},
	}, &outbox.Metrics{})
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("partial RabbitMQ capacity reported ready: status=%d", response.Code)
	}

	server = operationsServer(":0", readinessStore{}, []outbox.Broker{readinessBroker{}, readinessBroker{}}, &outbox.Metrics{})
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("full RabbitMQ capacity reported unavailable: status=%d", response.Code)
	}
}

func TestConfigurationRejectsLeaseWithoutConfirmSafety(t *testing.T) {
	t.Setenv("OUTBOX_POSTGRES_DSN", "postgres://publisher:secret@localhost/ledger")
	t.Setenv("OUTBOX_RABBITMQ_URL", "amqp://publisher:secret@localhost/outbox")
	t.Setenv("OUTBOX_RABBITMQ_ALLOW_INSECURE", "true")
	t.Setenv("OUTBOX_LEASE", "10s")
	t.Setenv("OUTBOX_CONFIRM_TIMEOUT", "10s")
	if _, err := loadConfig(); err == nil {
		t.Fatal("configuration accepted a lease with no budget for persisting the publisher confirm")
	}
}

func TestWorkerLifecycleJoinsEveryCanceledWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 3)
	active := &atomic.Int32{}
	workers := []outbox.Runner{
		cancelableWorker{started: started, active: active},
		cancelableWorker{started: started, active: active},
		cancelableWorker{started: started, active: active},
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.NewServeMux()}
	result := make(chan error, 1)
	go func() {
		result <- (outbox.Runtime{
			Server: server, Listener: listener, Workers: workers,
			Brokers: []outbox.Broker{readinessBroker{}}, ShutdownTimeout: time.Second,
		}).Run(ctx)
	}()
	for range workers {
		<-started
	}
	if got := active.Load(); got != int32(len(workers)) {
		t.Fatalf("not all workers started: active=%d", got)
	}
	cancel()
	if err := <-result; err != nil {
		t.Fatalf("runtime did not join workers after cancellation: %v", err)
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("worker leaked after join: active=%d", got)
	}
}
