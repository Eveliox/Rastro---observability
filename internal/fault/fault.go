// Package fault lets you break a service on purpose, at runtime, over HTTP.
//
//	GET    /admin/fault   show the current fault
//	PUT    /admin/fault   set it: {"error_rate": 1.0, "latency_ms": 500}
//	DELETE /admin/fault   clear it (service is healthy again)
package fault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
)

// ErrInjected is returned when a request is chosen to fail.
var ErrInjected = errors.New("injected fault")

// Config is the fault currently applied to a service.
type Config struct {
	ErrorRate float64 `json:"error_rate"` // 0.0 = never fail, 1.0 = always fail
	LatencyMS int     `json:"latency_ms"` // extra delay added to every request
}

// Injector holds the current Config. HTTP handlers run on many goroutines
// at once, so a mutex protects it from being read and written simultaneously.
type Injector struct {
	mu  sync.RWMutex
	cfg Config
	log *slog.Logger
}

func New(log *slog.Logger) *Injector { return &Injector{log: log} }

func (i *Injector) get() Config {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.cfg
}

func (i *Injector) set(c Config) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.cfg = c
}

// Maybe applies the current fault. Call it where the "real work" happens.
// It sleeps for the configured latency, then returns ErrInjected with
// probability ErrorRate. If the caller gives up (ctx cancelled), it stops waiting.
func (i *Injector) Maybe(ctx context.Context) error {
	c := i.get()
	if c.LatencyMS > 0 {
		select {
		case <-time.After(time.Duration(c.LatencyMS) * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if c.ErrorRate > 0 && rand.Float64() < c.ErrorRate {
		return ErrInjected
	}
	return nil
}

// Handler serves the /admin/fault endpoint.
func (i *Injector) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		switch r.Method {
		case http.MethodGet:
			// read only, nothing to change
		case http.MethodPut, http.MethodPost:
			var c Config
			if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
				http.Error(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
			if c.ErrorRate < 0 || c.ErrorRate > 1 || c.LatencyMS < 0 {
				http.Error(w, "error_rate must be 0..1 and latency_ms >= 0", http.StatusBadRequest)
				return
			}
			i.set(c)
			i.log.WarnContext(ctx, "fault injection enabled", "error_rate", c.ErrorRate, "latency_ms", c.LatencyMS)
		case http.MethodDelete:
			i.set(Config{})
			i.log.InfoContext(ctx, "fault injection cleared")
		default:
			http.Error(w, fmt.Sprintf("method %s not allowed", r.Method), http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(i.get())
	})
}
