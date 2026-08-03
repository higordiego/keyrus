package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// Runner is implemented by *Consumer; kept as an interface so Runtime stays
// testable without a real AMQP connection.
type Runner interface {
	Run(context.Context) error
}

// Runtime owns the consumer executable's complete lifecycle: it does not
// return until the HTTP operations server is closed and every consumer
// instance has joined, mirroring
// services/ledger/internal/outbox.Runtime so both transport executables
// shut down with the same guarantees.
type Runtime struct {
	Server          *http.Server
	Listener        net.Listener
	Consumers       []Runner
	ShutdownTimeout time.Duration
}

func (runtime Runtime) Run(ctx context.Context) error {
	if runtime.Server == nil || len(runtime.Consumers) == 0 || runtime.ShutdownTimeout <= 0 {
		return errors.New("invalid consumer runtime")
	}
	consumerContext, stopConsumers := context.WithCancel(ctx)
	consumerErrors := make(chan error, len(runtime.Consumers))
	consumers := &sync.WaitGroup{}
	for _, consumer := range runtime.Consumers {
		consumers.Add(1)
		go func(consumer Runner) {
			defer consumers.Done()
			consumerErrors <- consumer.Run(consumerContext)
		}(consumer)
	}
	serverErrors := make(chan error, 1)
	go func() {
		if runtime.Listener != nil {
			serverErrors <- runtime.Server.Serve(runtime.Listener)
			return
		}
		serverErrors <- runtime.Server.ListenAndServe()
	}()

	var runError error
	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			runError = fmt.Errorf("operations server: %w", err)
		}
	case err := <-consumerErrors:
		if !errors.Is(err, context.Canceled) {
			runError = fmt.Errorf("consolidation consumer: %w", err)
		}
	}
	stopConsumers()
	shutdownContext, cancel := context.WithTimeout(context.Background(), runtime.ShutdownTimeout)
	defer cancel()
	serverError := runtime.Server.Shutdown(shutdownContext)
	consumerError := waitRuntimeConsumers(shutdownContext, consumers)
	return errors.Join(runError, serverError, consumerError)
}

func waitRuntimeConsumers(ctx context.Context, consumers *sync.WaitGroup) error {
	done := make(chan struct{})
	go func() {
		consumers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return errors.New("consolidation consumers did not stop before shutdown deadline")
	}
}
