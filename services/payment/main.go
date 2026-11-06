// payment: the middle of the chain. Authorizes a card, asks inventory to
// reserve stock, then captures the money.
//
//	POST /charge        {"order_id": "...", "item_id": "...", "quantity": 1, "amount_cents": 4999}
//	PUT  /admin/fault   break it on purpose (see internal/fault)
package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/Eveliox/miniobs/internal/fault"
	"github.com/Eveliox/miniobs/internal/httpx"
	"github.com/Eveliox/miniobs/internal/telemetry"
)

const serviceName = "payment"

var tracer = otel.Tracer(serviceName)

type chargeRequest struct {
	OrderID     string `json:"order_id"`
	ItemID      string `json:"item_id"`
	Quantity    int    `json:"quantity"`
	AmountCents int    `json:"amount_cents"`
}

type chargeResponse struct {
	PaymentID     string `json:"payment_id"`
	ReservationID string `json:"reservation_id"`
}

func main() {
	shutdown, err := telemetry.Setup(context.Background(), serviceName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "telemetry setup:", err)
		os.Exit(1)
	}
	log := telemetry.NewLogger(serviceName)
	faults := fault.New(log)
	client := httpx.NewClient()
	inventoryURL := envOr("INVENTORY_URL", "http://localhost:8083")

	mux := http.NewServeMux()
	mux.Handle("/admin/fault", faults.Handler())
	mux.HandleFunc("POST /charge", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var req chargeRequest
		if err := httpx.ReadJSON(w, r, &req); err != nil || req.AmountCents <= 0 {
			httpx.WriteError(w, http.StatusBadRequest, errors.New("need a positive amount_cents"))
			return
		}
		log.InfoContext(ctx, "charging", "order_id", req.OrderID, "amount_cents", req.AmountCents)

		// 1. Authorize the card (pretend call to a card network).
		if err := step(ctx, "card.authorize", req, faults); err != nil {
			log.ErrorContext(ctx, "card authorization failed", "error", err)
			httpx.WriteError(w, http.StatusInternalServerError, fmt.Errorf("payment: %w", err))
			return
		}

		// 2. Reserve stock. Passing ctx is what keeps this in the same trace:
		//    otelhttp reads the current span from ctx and writes it into the
		//    traceparent header of the outgoing request.
		var res struct {
			ReservationID string `json:"reservation_id"`
		}
		err := httpx.PostJSON(ctx, client, inventoryURL+"/reserve", map[string]any{
			"order_id": req.OrderID, "item_id": req.ItemID, "quantity": req.Quantity,
		}, &res)
		if err != nil {
			var up *httpx.UpstreamError
			if errors.As(err, &up) && up.Status == http.StatusConflict {
				log.WarnContext(ctx, "inventory says out of stock", "item_id", req.ItemID)
				httpx.WriteError(w, http.StatusConflict, up)
				return
			}
			// Inventory broke. This service is fine; its dependency isn't.
			// 502 Bad Gateway literally means "the thing behind me failed".
			log.ErrorContext(ctx, "inventory call failed", "error", err)
			httpx.WriteError(w, http.StatusBadGateway, fmt.Errorf("payment: reserve stock: %w", err))
			return
		}

		// 3. Capture the money now that stock is held.
		if err := step(ctx, "card.capture", req, nil); err != nil {
			log.ErrorContext(ctx, "capture failed", "error", err)
			httpx.WriteError(w, http.StatusInternalServerError, fmt.Errorf("payment: %w", err))
			return
		}

		log.InfoContext(ctx, "charge complete", "order_id", req.OrderID)
		httpx.WriteJSON(w, http.StatusOK, chargeResponse{
			PaymentID:     "pay-" + req.OrderID,
			ReservationID: res.ReservationID,
		})
	})

	httpx.Serve(serviceName, ":"+envOr("PORT", "8082"), mux, log, shutdown)
}

// step simulates one call to the card network inside its own span.
// If faults is non-nil, the injected fault can fire here.
func step(ctx context.Context, name string, req chargeRequest, faults *fault.Injector) error {
	ctx, span := tracer.Start(ctx, name, trace.WithAttributes(
		attribute.String("order.id", req.OrderID),
		attribute.Int("payment.amount_cents", req.AmountCents),
	))
	defer span.End()

	time.Sleep(time.Duration(10+rand.IntN(30)) * time.Millisecond) // 10-40ms "network"

	if faults != nil {
		if err := faults.Maybe(ctx); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return err
		}
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
