// inventory: the bottom of the chain. Reserves stock for an order.
//
//	POST /reserve       {"order_id": "...", "item_id": "sku-keyboard", "quantity": 1}
//	PUT  /admin/fault   break it on purpose (see internal/fault)
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/Eveliox/miniobs/internal/fault"
	"github.com/Eveliox/miniobs/internal/httpx"
	"github.com/Eveliox/miniobs/internal/telemetry"
)

const serviceName = "inventory"

var tracer = otel.Tracer(serviceName)

var errOutOfStock = errors.New("out of stock")

type reserveRequest struct {
	OrderID  string `json:"order_id"`
	ItemID   string `json:"item_id"`
	Quantity int    `json:"quantity"`
}

type reserveResponse struct {
	ReservationID string `json:"reservation_id"`
	Remaining     int    `json:"remaining"`
}

// stock is a pretend database: item -> units on hand.
type stock struct {
	mu    sync.Mutex
	units map[string]int
}

func main() {
	shutdown, err := telemetry.Setup(context.Background(), serviceName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "telemetry setup:", err)
		os.Exit(1)
	}
	log := telemetry.NewLogger(serviceName)
	faults := fault.New(log)
	db := &stock{units: map[string]int{"sku-keyboard": 1000, "sku-mouse": 1000, "sku-monitor": 50}}

	mux := http.NewServeMux()
	mux.Handle("/admin/fault", faults.Handler())
	mux.HandleFunc("POST /reserve", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context() // already holds the server span otelhttp created

		var req reserveRequest
		if err := httpx.ReadJSON(w, r, &req); err != nil || req.ItemID == "" || req.Quantity <= 0 {
			httpx.WriteError(w, http.StatusBadRequest, errors.New("need item_id and quantity > 0"))
			return
		}
		log.InfoContext(ctx, "reserving stock", "order_id", req.OrderID, "item_id", req.ItemID, "quantity", req.Quantity)

		remaining, err := db.reserve(ctx, faults, req)
		switch {
		case errors.Is(err, errOutOfStock):
			// A business outcome, not a system failure: 409, and the span is NOT marked as an error.
			log.WarnContext(ctx, "out of stock", "item_id", req.ItemID)
			httpx.WriteError(w, http.StatusConflict, err)
			return
		case err != nil:
			log.ErrorContext(ctx, "reserve failed", "error", err)
			httpx.WriteError(w, http.StatusInternalServerError, fmt.Errorf("inventory: %w", err))
			return
		}

		log.InfoContext(ctx, "stock reserved", "item_id", req.ItemID, "remaining", remaining)
		httpx.WriteJSON(w, http.StatusOK, reserveResponse{
			ReservationID: "res-" + req.OrderID,
			Remaining:     remaining,
		})
	})

	httpx.Serve(serviceName, ":"+envOr("PORT", "8083"), mux, log, shutdown)
}

// reserve is the "real work". It gets its own child span, so in a trace
// waterfall you'll see it nested under "POST /reserve".
func (s *stock) reserve(ctx context.Context, faults *fault.Injector, req reserveRequest) (int, error) {
	ctx, span := tracer.Start(ctx, "stock.reserve", trace.WithAttributes(
		attribute.String("item.id", req.ItemID),
		attribute.Int("item.quantity", req.Quantity),
	))
	defer span.End() // the span's duration = from Start until here

	// Pretend this is a database round trip.
	time.Sleep(5 * time.Millisecond)

	// The fault fires HERE, inside the deepest span. That makes this span
	// the true root cause of the failure: the spans above it only fail
	// because this one did.
	if err := faults.Maybe(ctx); err != nil {
		span.RecordError(err)                    // attaches an "exception" event to the span
		span.SetStatus(codes.Error, err.Error()) // marks the span itself as failed
		return 0, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	have, ok := s.units[req.ItemID]
	if !ok || have < req.Quantity {
		span.SetAttributes(attribute.Bool("stock.insufficient", true))
		return have, errOutOfStock
	}
	s.units[req.ItemID] = have - req.Quantity
	return s.units[req.ItemID], nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
