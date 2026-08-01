package runtimeobs

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
)

// NewTracerProvider configures a real OTLP exporter. An absent endpoint fails
// startup so tracing cannot silently degrade into a no-op runtime.
func NewTracerProvider(ctx context.Context, service, endpoint string) (*sdktrace.TracerProvider, error) {
	if strings.TrimSpace(service) == "" || strings.TrimSpace(endpoint) == "" {
		return nil, errors.New("runtimeobs: service and OTLP endpoint are required")
	}
	exporterContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	exporter, err := otlptracegrpc.New(exporterContext,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, errors.New("runtimeobs: initialize OTLP exporter")
	}
	resources, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName(service)))
	if err != nil {
		return nil, errors.New("runtimeobs: initialize trace resource")
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(resources),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(200*time.Millisecond),
			sdktrace.WithExportTimeout(3*time.Second),
		),
	)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return provider, nil
}

// ErrorClass returns one opaque, allowlisted class for process-level failures.
func ErrorClass(error) string { return "runtime_failure" }
