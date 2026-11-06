# 08: One order, start to finish

> **In plain words:** this guide follows one real `make order` through every piece of code it touches,
> in order. If the other guides are the parts of a car, this is a test drive.

The real IDs below come from a run on 2026-11-05. Trace ID: **`652ac6d062c3dda021d1a6a00adae57a`**.

---

## Step 0: Before any request: startup

When `make up` starts the containers, each service's `main()` runs once:

1. `telemetry.Setup` creates the TracerProvider (no exporter yet) and the `traceparent` propagator, and makes them global.
2. `telemetry.NewLogger` creates the JSON logger wrapped in `traceHandler`.
3. `fault.New` creates an injector with an empty (healthy) config.
4. Routes are registered on the mux.
5. `httpx.Serve` wraps the mux with `otelhttp.NewHandler`, starts listening, and logs (inside a tiny span):

```json
{"level":"INFO","msg":"service started","service":"inventory","addr":":8083","trace_id":"4824dd8a…","span_id":"9871c287…"}
```

Now all three are waiting.

## Step 1: You type `make order`

Make runs:

```bash
curl -s -i -X POST localhost:8081/checkout -H 'Content-Type: application/json' -d '{"item_id":"sku-keyboard","quantity":1}'
```

curl sends an HTTP request to port 8081 on your Mac. Docker forwards it into the checkout container (`ports: "8081:8081"`).
There's **no `traceparent` header**: curl isn't traced.

## Step 2: checkout's otelhttp handler (server span #1)

`otelhttp.NewHandler` (in `httpx.Serve`) sees the request first:

- Looks for `traceparent`. None, so **this is a new trace**: generates trace ID `652ac6d0…`.
- Starts server span **`POST /checkout`** (span `86068d1bffe6f160`, no parent = root).
- Puts it in `r.Context()` and calls the mux, which picks our `POST /checkout` function.

## Step 3: checkout's handler code

[services/checkout/main.go](../../../services/checkout/main.go)

```go
ctx := r.Context()                                         // backpack: holds span 86068d1b…
traceID := trace.SpanContextFromContext(ctx).TraceID()...  // "652ac6d0…"
w.Header().Set("X-Trace-Id", traceID)                      // will appear in your response
```

- `httpx.ReadJSON` fills `req` → `{ItemID: "sku-keyboard", Quantity: 1}`.
- Order ID generated: `ord-9eee6ab0`.
- Log:
  ```json
  {"msg":"checkout started","service":"checkout","order_id":"ord-9eee6ab0","trace_id":"652ac6d0…","span_id":"86068d1bffe6f160"}
  ```
  `traceHandler` pulled those two IDs out of `ctx`.

## Step 4: pricing (internal span)

`price(ctx, req, faults)`:

- `tracer.Start(ctx, "pricing.lookup")` → child span of `POST /checkout`.
- `faults.Maybe(ctx)` → config is empty → returns `nil` immediately.
- `prices["sku-keyboard"]` = 4999. × 1 = **4999 cents**.
- `defer span.End()` closes the span. Took well under 1ms.

## Step 5: checkout calls payment (client span + propagation)

```go
httpx.PostJSON(ctx, client, "http://payment:8082/charge", {order_id, item_id, quantity, amount_cents: 4999}, &pay)
```

Inside:

1. `http.NewRequestWithContext(ctx, …)` attaches the backpack to the outgoing request.
2. `client.Do(req)` goes through `otelhttp.Transport`, which:
   - starts client span **`HTTP POST`** (child of `POST /checkout`),
   - **injects** `traceparent: 00-652ac6d0…-<HTTP POST's span id>-01` into the headers,
   - sends it.
3. Compose DNS turns `payment` into the payment container's address.

## Step 6: payment receives it (server span #2)

payment's `otelhttp.NewHandler`:

- **Extracts** `traceparent`: "trace `652ac6d0…`, my parent is checkout's `HTTP POST` span".
- Starts server span **`POST /charge`** (span `71c8a5fa23197c92`) **in the same trace**.

[services/payment/main.go](../../../services/payment/main.go) handler:

```json
{"msg":"charging","service":"payment","order_id":"ord-9eee6ab0","amount_cents":4999,"trace_id":"652ac6d0…","span_id":"71c8a5fa23197c92"}
```

Same trace_id as checkout. Different service. That's the whole point.

## Step 7: payment authorizes the card

`step(ctx, "card.authorize", req, faults)`:

- New internal span `card.authorize`, with attributes `order.id` and `payment.amount_cents`.
- Sleeps 10–40ms (pretend card network). It was about 20ms in this run.
- `faults.Maybe` → nil.

## Step 8: payment calls inventory

Same as step 5, one level down: client span `HTTP POST` in payment, `traceparent` injected, sent to `http://inventory:8083/reserve`.

## Step 9: inventory reserves stock (server span #3 + internal span)

inventory's `otelhttp.NewHandler` extracts → server span **`POST /reserve`** (span `eb8c140ddb9de5e8`), same trace.

[services/inventory/main.go](../../../services/inventory/main.go):

```json
{"msg":"reserving stock","service":"inventory","order_id":"ord-9eee6ab0","item_id":"sku-keyboard","quantity":1,"trace_id":"652ac6d0…","span_id":"eb8c140ddb9de5e8"}
```

`db.reserve(ctx, faults, req)`:

- Internal span **`stock.reserve`**.
- Sleeps 5ms (pretend database).
- `faults.Maybe` → nil. **(This is where `make break-inventory` would strike.)**
- Locks the mutex, `units["sku-keyboard"]` 997 → 996, unlocks.
- Span ends.

```json
{"msg":"stock reserved","service":"inventory","item_id":"sku-keyboard","remaining":996,"trace_id":"652ac6d0…","span_id":"eb8c140ddb9de5e8"}
```

Responds `200 {"reservation_id":"res-ord-9eee6ab0","remaining":996}`. otelhttp ends `POST /reserve` with status 200.
Inventory took about **6ms**.

## Step 10: back up through payment

- payment's client span `HTTP POST` ends (status 200 → not an error).
- `step(ctx, "card.capture", req, nil)`: another 10–40ms. `nil` faults, so it can't fail.
- Log `"charge complete"`, respond `200 {"payment_id":"pay-ord-9eee6ab0", ...}`.
- `POST /charge` ends. Payment took about **52ms**.

## Step 11: back up through checkout

- checkout's client span `HTTP POST` ends.
- Log `"checkout complete"`.
- Respond `200` with the order details and `trace_id`.
- `POST /checkout` ends. Checkout took about **56ms** total.

## Step 12: what you see

```
HTTP/1.1 200 OK
Content-Type: application/json
X-Trace-Id: 652ac6d062c3dda021d1a6a00adae57a

{"order_id":"ord-9eee6ab0","status":"confirmed","amount_cents":4999,"payment_id":"pay-ord-9eee6ab0","trace_id":"652ac6d062c3dda021d1a6a00adae57a"}
```

## Step 13: where did the spans go?

**Nowhere, yet.** 9 spans were created across 3 services. Each was dropped when it ended, because there's no exporter.
Only their IDs survive, inside the log lines. Phase 2 catches them.

## The whole thing on one page

```
you ──curl──► checkout ─────────────────────────────────────────────────────────► 200
              [POST /checkout  86068d1b…] root, new trace 652ac6d0…
               ├ pricing.lookup
               └ HTTP POST ──traceparent──► payment
                                            [POST /charge  71c8a5fa…]
                                             ├ card.authorize (~20ms)
                                             ├ HTTP POST ──traceparent──► inventory
                                             │                            [POST /reserve  eb8c140d…]
                                             │                             └ stock.reserve (5ms sleep, 997→996)
                                             └ card.capture (~25ms)

logs (all with trace_id 652ac6d0…):
  checkout  "checkout started"   span 86068d1b…
  payment   "charging"           span 71c8a5fa…
  inventory "reserving stock"    span eb8c140d…
  inventory "stock reserved"     span eb8c140d…
  payment   "charge complete"    span 71c8a5fa…
  checkout  "checkout complete"  span 86068d1b…
```

## Now the same order with inventory broken

Only step 9 changes: `faults.Maybe` returns `ErrInjected`. Then everything unwinds with errors instead:

| Step | What happens |
|---|---|
| inventory `stock.reserve` | `RecordError` + `SetStatus(Error)`; returns the error. **← root cause** |
| inventory handler | logs `ERROR "reserve failed"`, responds **500** |
| payment `HTTP POST` client span | status Error (got a 5xx) |
| payment handler | `PostJSON` returns `UpstreamError{500}`, logs `ERROR "inventory call failed"`, responds **502**, `card.capture` never runs |
| checkout `HTTP POST` client span | status Error |
| checkout handler | logs `ERROR "checkout failed"`, responds **502** |
| you | `502 {"error":"checkout: charge: upstream returned 502: payment: reserve stock: upstream returned 500: inventory: injected fault"}` |

And `docker compose logs --no-log-prefix | grep 6aa6137f81bd25de0d2c4be58cecdcbd` shows all six log lines from all three services.
