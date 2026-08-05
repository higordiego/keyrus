// Package runtimeobs supplies the minimum health, metrics and structured
// telemetry shared by the T02 adapters. It deliberately contains no business
// metric or domain rule.
package runtimeobs

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/higordiegoti/keyrus/internal/platform/observability/tracecontext"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// numDurationBuckets must equal len(durationBuckets); Go array sizes must be
// compile-time constants, so it cannot be derived from the slice directly.
const numDurationBuckets = 7

// durationBuckets are the upper bounds (seconds) of the hand-rolled request
// duration histogram, in the shape Prometheus's histogram_quantile() expects
// (cumulative "le" buckets). They bracket the RNF alert threshold (p95
// >500ms) with enough resolution on both sides of it to be useful, without
// pulling in a metrics client library only for this one instrument.
var durationBuckets = [numDurationBuckets]float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

type Metrics struct {
	entrypoint         atomic.Uint64
	requests           atomic.Uint64
	rejections         atomic.Uint64
	failures           atomic.Uint64
	idempotencyHeaders atomic.Uint64
	traceHeaders       atomic.Uint64
	durationBucketHits [numDurationBuckets + 1]atomic.Uint64 // last slot is the +Inf bucket
	durationSumNanos   atomic.Uint64
}

const (
	publicReadTimeout  = 5 * time.Second
	publicWriteTimeout = 5 * time.Second
	publicIdleTimeout  = 30 * time.Second
)

// HTTPServer applies complete connection budgets, including body reads.
func HTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       publicReadTimeout,
		WriteTimeout:      publicWriteTimeout,
		IdleTimeout:       publicIdleTimeout,
	}
}

// EntrypointMiddleware counts every request that reaches the adapter's HTTP
// listener, before any authentication decision runs. It exists so a caller can
// prove the edge never forwarded a rejected request: the regular request
// counter only advances inside the authenticated handler, so it cannot tell
// "the edge rejected the call" apart from "the edge forwarded it and this
// adapter's own auth middleware rejected it afterwards." This counter moves in
// both cases when the adapter is reached at all, and only in the second case
// when the edge is the one making the decision.
func EntrypointMiddleware(metrics *Metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		metrics.entrypoint.Add(1)
		next.ServeHTTP(writer, request)
	})
}

func (m *Metrics) Observe(request *http.Request, status int, duration time.Duration) {
	m.requests.Add(1)
	if request.Header.Get("Idempotency-Key") != "" {
		m.idempotencyHeaders.Add(1)
	}
	if _, err := tracecontext.ParseTraceParent(request.Header.Get(tracecontext.TraceParentHeader)); err == nil {
		m.traceHeaders.Add(1)
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		m.rejections.Add(1)
	}
	if status >= 500 {
		m.failures.Add(1)
	}
	m.observeDuration(duration)
}

// observeDuration increments every cumulative bucket whose upper bound is at
// or above the observed duration, plus the +Inf bucket, matching the
// semantics histogram_quantile() expects.
func (m *Metrics) observeDuration(duration time.Duration) {
	if duration < 0 {
		duration = 0
	}
	seconds := duration.Seconds()
	m.durationSumNanos.Add(uint64(duration))
	for i, bound := range durationBuckets {
		if seconds <= bound {
			m.durationBucketHits[i].Add(1)
		}
	}
	m.durationBucketHits[len(durationBuckets)].Add(1) // +Inf
}

// Handler exposes Prometheus text format only on the management listener.
func (m *Metrics) Handler(service string) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = fmt.Fprintf(writer,
			"cashflow_http_entrypoint_total{service=%q} %d\n"+
				"cashflow_http_requests_total{service=%q} %d\n"+
				"cashflow_http_rejections_total{service=%q} %d\n"+
				"cashflow_http_failures_total{service=%q} %d\n"+
				"cashflow_http_idempotency_header_total{service=%q} %d\n"+
				"cashflow_http_trace_header_total{service=%q} %d\n",
			service, m.entrypoint.Load(),
			service, m.requests.Load(), service, m.rejections.Load(), service, m.failures.Load(),
			service, m.idempotencyHeaders.Load(), service, m.traceHeaders.Load())
		m.writeDurationHistogram(writer, service)
	})
}

// writeDurationHistogram emits cashflow_http_request_duration_seconds in
// standard Prometheus histogram exposition format, so
// histogram_quantile(0.95, ...) works in Prometheus/Grafana exactly as it
// would against a client_golang histogram.
func (m *Metrics) writeDurationHistogram(writer io.Writer, service string) {
	_, _ = fmt.Fprintf(writer, "# HELP cashflow_http_request_duration_seconds HTTP request duration in seconds.\n# TYPE cashflow_http_request_duration_seconds histogram\n")
	for i, bound := range durationBuckets {
		_, _ = fmt.Fprintf(writer, "cashflow_http_request_duration_seconds_bucket{service=%q,le=%q} %d\n",
			service, strconv.FormatFloat(bound, 'g', -1, 64), m.durationBucketHits[i].Load())
	}
	_, _ = fmt.Fprintf(writer, "cashflow_http_request_duration_seconds_bucket{service=%q,le=\"+Inf\"} %d\n",
		service, m.durationBucketHits[len(durationBuckets)].Load())
	_, _ = fmt.Fprintf(writer, "cashflow_http_request_duration_seconds_sum{service=%q} %v\n",
		service, float64(m.durationSumNanos.Load())/float64(time.Second))
	_, _ = fmt.Fprintf(writer, "cashflow_http_request_duration_seconds_count{service=%q} %d\n",
		service, m.durationBucketHits[len(durationBuckets)].Load())
}

type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (r *responseRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(body []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(body)
}

// Middleware records an actual adapter exchange without request headers or
// payload fields, keeping the log safe by construction.
func Middleware(service string, metrics *Metrics, logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		state := tracecontext.SanitizeTraceState(request.Header.Get(tracecontext.TraceStateHeader))
		parent := request.Context()
		if carrier, _, ok := tracecontext.FromContext(parent); ok {
			parent = tracecontext.WithRemoteParent(parent, carrier, state)
		} else if carrier, err := tracecontext.ParseTraceParent(request.Header.Get(tracecontext.TraceParentHeader)); err == nil {
			parent = tracecontext.WithRemoteParent(parent, carrier, state)
		}
		spanContext, span := otel.Tracer("cashflow/runtimeobs").Start(parent,
			request.Method+" "+request.URL.Path,
			oteltrace.WithSpanKind(oteltrace.SpanKindServer),
			oteltrace.WithAttributes(
				attribute.String("http.request.method", request.Method),
				attribute.String("url.path", request.URL.Path),
				attribute.String("service.name", service),
			),
		)
		spanContext = tracecontext.WithCurrentSpan(spanContext, state)
		request = request.WithContext(spanContext)
		recorder := &responseRecorder{ResponseWriter: writer}
		next.ServeHTTP(recorder, request)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		metrics.Observe(request, status, time.Since(started))
		span.SetAttributes(attribute.Int("http.response.status_code", status))
		if status >= 500 {
			span.SetStatus(otelcodes.Error, "server_error")
		}
		span.End()
		attributes := []any{
			slog.String("service", service),
			slog.String("method", request.Method),
			slog.String("path", request.URL.Path),
			slog.Int("status", status),
			slog.Duration("duration", time.Since(started)),
		}
		if span, _, ok := tracecontext.FromContext(request.Context()); ok {
			attributes = append(attributes, slog.String("trace_id", span.TraceID))
		} else if span, err := tracecontext.ParseTraceParent(request.Header.Get(tracecontext.TraceParentHeader)); err == nil {
			attributes = append(attributes, slog.String("trace_id", span.TraceID))
		}
		logger.InfoContext(request.Context(), "HTTP request completed", attributes...)
	})
}

// ManagementHandler keeps health and metrics off the public adapter listener.
func ManagementHandler(service string, ready func(context.Context) error, metrics *Metrics) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /health/ready", func(writer http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		defer cancel()
		if err := ready(ctx); err != nil {
			http.Error(writer, "Service Unavailable", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("GET /metrics", metrics.Handler(service))
	return mux
}
