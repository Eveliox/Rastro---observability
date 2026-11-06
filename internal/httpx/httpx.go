// Package httpx holds the small HTTP helpers every demo service shares:
// JSON in/out, a traced client for calling other services, and a server
// that shuts down cleanly.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
)

// ---------- JSON helpers ----------

// ReadJSON decodes the request body into v. Bodies over 1MB and unknown
// fields are rejected, so typos in requests fail loudly instead of silently.
func ReadJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func WriteError(w http.ResponseWriter, status int, err error) {
	WriteJSON(w, status, map[string]string{"error": err.Error()})
}

// ---------- calling other services ----------

// NewClient returns an HTTP client whose transport is wrapped by otelhttp.
// For every outgoing request it (1) creates a "client" span and
// (2) injects the traceparent header so the next service joins the same trace.
func NewClient() *http.Client {
	return &http.Client{
		Transport: otelhttp.NewTransport(http.DefaultTransport),
		Timeout:   5 * time.Second,
	}
}

// UpstreamError means another service answered with a non-2xx status.
type UpstreamError struct {
	Status  int
	Message string
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("upstream returned %d: %s", e.Status, e.Message)
}

// PostJSON sends `in` as JSON to url and decodes the response into `out`.
// ctx MUST be the incoming request's context, or the trace breaks in two.
func PostJSON(ctx context.Context, c *http.Client, url string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if json.Unmarshal(raw, &e) != nil || e.Error == "" {
			e.Error = string(raw)
		}
		return &UpstreamError{Status: resp.StatusCode, Message: e.Error}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ---------- running a server ----------

// Serve runs the service until Ctrl-C / SIGTERM (what `docker compose down`
// sends), then gives in-flight requests up to 5s to finish and flushes spans.
func Serve(name, addr string, mux http.Handler, log *slog.Logger, shutdownTelemetry func(context.Context) error) {
	// otelhttp.NewHandler creates a "server" span for every incoming request.
	// If the request carries a traceparent header, the span joins that trace.
	handler := otelhttp.NewHandler(mux, name,
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path // e.g. "POST /reserve"
		}),
	)
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	lifecycleLog(log, name, "service started", "addr", addr)

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			lifecycleLog(log, name, "server failed", "error", err)
			os.Exit(1)
		}
	}

	lifecycleLog(log, name, "shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	_ = shutdownTelemetry(shutdownCtx)
}

// lifecycleLog logs inside a tiny span so that even startup/shutdown lines
// follow the rule "every log carries trace_id and span_id".
func lifecycleLog(log *slog.Logger, name, msg string, args ...any) {
	ctx, span := otel.Tracer(name).Start(context.Background(), "lifecycle")
	defer span.End()
	log.InfoContext(ctx, msg, args...)
}
