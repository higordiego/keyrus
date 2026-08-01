package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"regexp"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type WorkerConfig struct {
	Owner        string
	BatchSize    int
	Lease        time.Duration
	PollInterval time.Duration
	BackoffBase  time.Duration
	BackoffMax   time.Duration
}

type Worker struct {
	store    Store
	broker   Broker
	config   WorkerConfig
	clock    Clock
	metrics  *Metrics
	logger   *slog.Logger
	tracer   trace.Tracer
	randomMu sync.Mutex
	random   *rand.Rand
}

func NewWorker(
	store Store,
	broker Broker,
	config WorkerConfig,
	metrics *Metrics,
	logger *slog.Logger,
) (*Worker, error) {
	if store == nil || broker == nil || metrics == nil || logger == nil {
		return nil, errorsNew("outbox worker dependencies are required")
	}
	if config.Owner == "" || config.BatchSize < 1 || config.Lease <= 0 ||
		config.PollInterval <= 0 || config.BackoffBase <= 0 ||
		config.BackoffMax < config.BackoffBase {
		return nil, errorsNew("invalid outbox worker configuration")
	}
	return &Worker{
		store: store, broker: broker, config: config, clock: systemClock{},
		metrics: metrics, logger: logger,
		tracer: otel.Tracer("github.com/higordiegoti/keyrus/outbox-publisher"),
		random: rand.New(rand.NewSource(time.Now().UnixNano())),
	}, nil
}

func (w *Worker) SetClockForTest(clock Clock) {
	if clock != nil {
		w.clock = clock
	}
}

func (w *Worker) Run(ctx context.Context) error {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		processed, err := w.ProcessOnce(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			w.logger.Error("outbox polling failed", "error", sanitizeError(err.Error()))
		}
		delay := w.config.PollInterval
		if processed == w.config.BatchSize {
			delay = 0
		}
		timer.Reset(delay)
	}
}

func (w *Worker) ProcessOnce(ctx context.Context) (int, error) {
	events, err := w.store.Claim(ctx, w.config.Owner, w.config.BatchSize, w.config.Lease)
	if err != nil {
		w.metrics.RecordError()
		return 0, err
	}
	for index, event := range events {
		if err := w.publishOne(ctx, event); err != nil {
			if errors.Is(err, ErrPublicationInterrupted) {
				return index, err
			}
			return index + 1, err
		}
	}
	return len(events), nil
}

func (w *Worker) publishOne(ctx context.Context, event Event) error {
	ctx = propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{
		"traceparent": traceparentFromPayload(event.Payload),
	})
	ctx, span := w.tracer.Start(ctx, "outbox.publish", trace.WithSpanKind(trace.SpanKindProducer))
	defer span.End()
	started := w.clock.Now()
	if err := w.broker.Publish(ctx, event); err != nil {
		w.metrics.RecordError()
		span.RecordError(err)
		if errors.Is(err, ErrPublicationInterrupted) {
			return err
		}
		delay := w.retryDelay(event.Attempts)
		releaseErr := w.store.MarkFailed(
			ctx, event.EventID, event.LeaseOwner,
			w.clock.Now().Add(delay), sanitizeError(err.Error()),
		)
		if releaseErr != nil {
			return errors.Join(err, releaseErr)
		}
		w.logger.Warn("outbox publication failed",
			"event_id", event.EventID,
			"attempt", event.Attempts,
			"retry_in_ms", delay.Milliseconds(),
			"error", sanitizeError(err.Error()),
		)
		return err
	}
	confirmedAt := w.clock.Now()
	if err := w.store.MarkPublished(ctx, event.EventID, event.LeaseOwner, confirmedAt); err != nil {
		w.metrics.RecordError()
		span.RecordError(err)
		return fmt.Errorf("persist publisher confirm: %w", err)
	}
	w.metrics.RecordConfirm(confirmedAt.Sub(started), event.Attempts)
	w.logger.Info("outbox event published",
		"event_id", event.EventID,
		"entry_id", event.AggregateID,
		"attempt", event.Attempts,
	)
	return nil
}

func (w *Worker) retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := w.config.BackoffBase
	for step := 1; step < attempt && delay < w.config.BackoffMax; step++ {
		if delay > w.config.BackoffMax/2 {
			delay = w.config.BackoffMax
			break
		}
		delay *= 2
	}
	w.randomMu.Lock()
	factor := 0.5 + w.random.Float64()
	w.randomMu.Unlock()
	result := time.Duration(float64(delay) * factor)
	if result > w.config.BackoffMax {
		return w.config.BackoffMax
	}
	return result
}

var credentialURL = regexp.MustCompile(`(?i)amqps?://[^@[:space:]]+@`)

func sanitizeError(message string) string {
	message = credentialURL.ReplaceAllString(message, "amqp://[redacted]@")
	if len(message) > 500 {
		return message[:500]
	}
	return message
}

func traceparentFromPayload(payload []byte) string {
	var header struct {
		Traceparent string `json:"traceparent"`
	}
	if json.Unmarshal(payload, &header) != nil {
		return ""
	}
	return header.Traceparent
}
