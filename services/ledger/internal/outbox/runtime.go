package outbox

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

type Runner interface {
	Run(context.Context) error
}

// Runtime owns the complete publisher lifecycle. It does not return until the
// HTTP server is closed, every worker has joined, and every broker is closed.
type Runtime struct {
	Server          *http.Server
	Listener        net.Listener
	Workers         []Runner
	Brokers         []Broker
	ShutdownTimeout time.Duration
}

func (runtime Runtime) Run(ctx context.Context) error {
	if runtime.Server == nil || len(runtime.Workers) == 0 || len(runtime.Brokers) == 0 || runtime.ShutdownTimeout <= 0 {
		return errorsNew("invalid outbox runtime")
	}
	workerContext, stopWorkers := context.WithCancel(ctx)
	workerErrors := make(chan error, len(runtime.Workers))
	workers := &sync.WaitGroup{}
	for _, worker := range runtime.Workers {
		workers.Add(1)
		go func(worker Runner) {
			defer workers.Done()
			workerErrors <- worker.Run(workerContext)
		}(worker)
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
	case err := <-workerErrors:
		if !errors.Is(err, context.Canceled) {
			runError = fmt.Errorf("publisher worker: %w", err)
		}
	}
	stopWorkers()
	shutdownContext, cancel := context.WithTimeout(context.Background(), runtime.ShutdownTimeout)
	defer cancel()
	serverError := runtime.Server.Shutdown(shutdownContext)
	var brokerErrors []error
	for _, broker := range runtime.Brokers {
		// Close starts by interrupting the active socket, so even a syscall that
		// did not observe context cannot prevent the subsequent bounded join.
		brokerErrors = append(brokerErrors, broker.Close())
	}
	workerError := waitRuntimeWorkers(shutdownContext, workers)
	return errors.Join(runError, serverError, workerError, errors.Join(brokerErrors...))
}

func waitRuntimeWorkers(ctx context.Context, workers *sync.WaitGroup) error {
	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return errors.New("publisher workers did not stop before shutdown deadline")
	}
}
