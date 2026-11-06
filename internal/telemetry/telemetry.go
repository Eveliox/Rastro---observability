// Package telemetry turns on OpenTelemetry tracing for a service and gives it
// a logger that stamps every log line with the current trace_id and span_id.
package telemetry

import (
	"context"
	"fmt"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Setup configures tracing for one service. Call it once at the top of main
// and call the returned shutdown function before exiting, so buffered spans
// get sent instead of lost.
func Setup(ctx context.Context, serviceName string) (shutdown func(context.Context) error, err error) {
	// A "resource" describes WHO is producing the telemetry. service.name is
	// the most important attribute: it's how the UI will label each span.
	res, err := resource.New(ctx,
		resource.WithAttributes(attribute.String("service.name", serviceName)),
		resource.WithFromEnv(),      // also read OTEL_RESOURCE_ATTRIBUTES
		resource.WithTelemetrySDK(), // records which SDK version made the data
	)
	if err != nil {
		return nil, fmt.Errorf("build resource: %w", err)
	}

	opts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}

	// Phase 1: there's no ingest service yet, so by default spans are created
	// (they get real IDs, and those IDs flow between services) but aren't sent anywhere.
	// Phase 2 sets OTEL_EXPORTER_OTLP_ENDPOINT and spans start flowing out.
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		exp, err := otlptracehttp.New(ctx) // reads endpoint/headers from env
		if err != nil {
			return nil, fmt.Errorf("create OTLP exporter: %w", err)
		}
		// The batcher collects spans in memory and sends them in groups,
		// so a request never waits on the network to export its own span.
		opts = append(opts, sdktrace.WithBatcher(exp))
	}

	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)

	// The propagator decides HOW trace context travels between services.
	// TraceContext = the W3C "traceparent" HTTP header, the standard format.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tp.Shutdown, nil
}
