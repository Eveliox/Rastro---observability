package telemetry

import (
	"context"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel/trace"
)

// NewLogger returns a JSON logger. Every line it writes includes trace_id and
// span_id, taken from the context passed to InfoContext/ErrorContext/etc.
//
// Rule: always use the ...Context methods. log.Info("x") has no context, so
// it prints all-zero IDs, which is your hint that the context got dropped.
func NewLogger(serviceName string) *slog.Logger {
	base := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(traceHandler{base}).With("service", serviceName)
}

// traceHandler wraps another slog.Handler and adds trace fields to each record.
// This is the "decorator" pattern: same interface, one extra behavior.
type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	sc := trace.SpanContextFromContext(ctx)
	r.AddAttrs(
		slog.String("trace_id", sc.TraceID().String()),
		slog.String("span_id", sc.SpanID().String()),
	)
	return h.Handler.Handle(ctx, r)
}

// These two make sure logger.With(...) and logger.WithGroup(...) keep our
// wrapper instead of silently returning the plain inner handler.
func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(name)}
}
