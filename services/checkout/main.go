// checkout: the front door. A customer places an order here; checkout prices
// it and calls payment (which in turn calls inventory).
//
//	POST /checkout      {"item_id": "sku-keyboard", "quantity": 1}
//	PUT  /admin/fault   break it on purpose (see internal/fault)
package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/Eveliox/miniobs/internal/fault"
	"github.com/Eveliox/miniobs/internal/httpx"
	"github.com/Eveliox/miniobs/internal/telemetry"
)

const serviceName = "checkout"

var tracer = otel.Tracer(serviceName)

// Prices in cents. Money is never a float: 0.1 + 0.2 != 0.3 in floating point.
var prices = map[string]int{
	"sku-keyboard": 4999,
	"sku-mouse":    1999,
	"sku-monitor":  18999,
}

type checkoutRequest struct {
	ItemID   string `json:"item_id"`
	Quantity int    `json:"quantity"`
}

type checkoutResponse struct {
	OrderID     string `json:"order_id"`
	Status      string `json:"status"`
	AmountCents int    `json:"amount_cents"`
	PaymentID   string `json:"payment_id"`
	TraceID     string `json:"trace_id"` // handy: lets you find this request in logs
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
	paymentURL := envOr("PAYMENT_URL", "http://localhost:8082")

	mux := http.NewServeMux()
	mux.Handle("/admin/fault", faults.Handler())
	mux.HandleFunc("POST /checkout", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		traceID := trace.SpanContextFromContext(ctx).TraceID().String()
		w.Header().Set("X-Trace-Id", traceID)

		var req checkoutRequest
		if err := httpx.ReadJSON(w, r, &req); err != nil || req.Quantity <= 0 {
			httpx.WriteError(w, http.StatusBadRequest, errors.New(`send {"item_id": "sku-keyboard", "quantity": 1}`))
			return
		}
		orderID := fmt.Sprintf("ord-%08x", rand.Uint32())
		log.InfoContext(ctx, "checkout started", "order_id", orderID, "item_id", req.ItemID, "quantity", req.Quantity)

		amount, err := price(ctx, req, faults)
		if err != nil {
			log.WarnContext(ctx, "pricing failed", "error", err)
			status := http.StatusBadRequest
			if errors.Is(err, fault.ErrInjected) {
				status = http.StatusInternalServerError
			}
			httpx.WriteError(w, status, fmt.Errorf("checkout: %w", err))
			return
		}

		var pay struct {
			PaymentID string `json:"payment_id"`
		}
		err = httpx.PostJSON(ctx, client, paymentURL+"/charge", map[string]any{
			"order_id": orderID, "item_id": req.ItemID, "quantity": req.Quantity, "amount_cents": amount,
		}, &pay)
		if err != nil {
			var up *httpx.UpstreamError
			if errors.As(err, &up) && up.Status == http.StatusConflict {
				log.WarnContext(ctx, "order rejected: out of stock", "order_id", orderID)
				httpx.WriteError(w, http.StatusConflict, fmt.Errorf("checkout: %w", err))
				return
			}
			log.ErrorContext(ctx, "checkout failed", "order_id", orderID, "error", err)
			httpx.WriteError(w, http.StatusBadGateway, fmt.Errorf("checkout: charge: %w", err))
			return
		}

		log.InfoContext(ctx, "checkout complete", "order_id", orderID, "amount_cents", amount)
		httpx.WriteJSON(w, http.StatusOK, checkoutResponse{
			OrderID: orderID, Status: "confirmed", AmountCents: amount,
			PaymentID: pay.PaymentID, TraceID: traceID,
		})
	})

	httpx.Serve(serviceName, ":"+envOr("PORT", "8081"), mux, log, shutdown)
}

// price looks up the item price in its own span (imagine a pricing service).
func price(ctx context.Context, req checkoutRequest, faults *fault.Injector) (int, error) {
	ctx, span := tracer.Start(ctx, "pricing.lookup", trace.WithAttributes(
		attribute.String("item.id", req.ItemID),
	))
	defer span.End()

	if err := faults.Maybe(ctx); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return 0, err
	}
	unit, ok := prices[req.ItemID]
	if !ok {
		return 0, fmt.Errorf("unknown item %q", req.ItemID)
	}
	return unit * req.Quantity, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
