# 09: Hands-on labs

> **How to do these:** for each lab, **write down your prediction first**, then run it, then open the answer.
> Being wrong and finding out why is where the learning happens.

Start with everything running and healthy:

```bash
make up
make heal-inventory
make fault-status      # all three should show {"error_rate":0,"latency_ms":0}
```

A helper you'll use a lot: find every log line for one trace, in time order.

```bash
docker compose logs --no-log-prefix | grep <TRACE_ID> | sort
```

(`sort` works because every line starts with `{"time":"…`, so alphabetical = chronological.)

---

## Lab 1: Follow one order

```bash
make order
```

Copy the `X-Trace-Id` value, then run the helper above with it.

**Predict:** how many log lines? From which services, in which order? How many different `span_id` values?

<details><summary>Answer</summary>

6 lines: checkout started → charging → reserving stock → stock reserved → charge complete → checkout complete.
3 distinct span_ids: one per service (each service's server span), because each handler logs with its own request `ctx`.
All share the trace_id.
</details>

---

## Lab 2: Break inventory, find the root cause

```bash
make break-inventory
make order
```

**Predict:** the HTTP status you get. The status each service returns. Which log lines are ERROR.

Then read the `error` message in the response from left to right and say out loud where the failure started.

<details><summary>Answer</summary>

You get `502`. inventory 500, payment 502, checkout 502.
ERROR lines: inventory "reserve failed", payment "inventory call failed", checkout "checkout failed".
The message ends with `inventory: injected fault`: inventory is the origin.
In span terms: `stock.reserve` is the deepest span with its own exception event.
</details>

```bash
make heal-inventory
```

---

## Lab 3: Break payment instead

```bash
curl -s -X PUT localhost:8082/admin/fault -d '{"error_rate":1}'
make order
```

**Predict:** does inventory log anything for this trace? Does stock go down? Status code?

<details><summary>Answer</summary>

Status `502` (payment returns 500, checkout turns it into 502).
Inventory logs **nothing** for this trace: payment's fault fires in `card.authorize`, which runs *before* the call to inventory.
So stock doesn't change. Root cause span: `card.authorize`.
Real response: `{"error":"checkout: charge: upstream returned 500: payment: injected fault"}`
</details>

```bash
curl -s -X DELETE localhost:8082/admin/fault
```

---

## Lab 4: Forge a trace ID (prove propagation is "just a header")

Call inventory **directly**, pretending to be part of a trace that doesn't exist:

```bash
curl -s -X POST localhost:8083/reserve \
  -H 'traceparent: 00-11111111111111111111111111111111-2222222222222222-01' \
  -d '{"order_id":"ord-handmade","item_id":"sku-mouse","quantity":1}'
docker compose logs --no-log-prefix inventory | grep 11111111
```

**Predict:** what trace_id appears in inventory's logs?

<details><summary>Answer</summary>

`11111111111111111111111111111111`. Inventory just trusts the header. The span_id is new (inventory's own server span);
its *parent* is `2222222222222222`, the fake span we claimed to be.

Now try it **without** the header: you get a random new trace_id. And try a broken header like `traceparent: banana`:
it's ignored and a new trace starts.
</details>

---

## Lab 5: Out of stock is not an error

```bash
curl -s -i -X POST localhost:8081/checkout -d '{"item_id":"sku-monitor","quantity":999}'
```

**Predict:** status code? Log levels? Is anything marked ERROR?

<details><summary>Answer</summary>

`409 Conflict`. inventory logs `WARN "out of stock"`, payment `WARN "inventory says out of stock"`,
checkout `WARN "order rejected: out of stock"`. No ERROR log lines.
`stock.reserve` gets `stock.insufficient=true` but not error status. Server spans stay unset (4xx).

The one catch (see [04](04-traces-spans-propagation.md#4-which-spans-turn-red-the-real-rules)): otelhttp's **client**
spans *are* marked Error, because by convention any 4xx response is a failed call from the caller's side.
No exception events, though, so no root cause. That's correct.

Message: `checkout: upstream returned 409: upstream returned 409: out of stock` ("upstream returned 409" twice:
payment passes inventory's UpstreamError back unchanged, and checkout wraps it again).
</details>

**Extra:** buy all 50 monitors one by one. On which order does it start failing?
`for i in $(seq 1 51); do ITEM=sku-monitor make order | head -1; done`
(Restart inventory afterwards to refill: `docker compose restart inventory`.)

---

## Lab 6: Timeouts and cancellation travel too

Make inventory slower than the 5-second client timeout:

```bash
curl -s -X PUT localhost:8083/admin/fault -d '{"latency_ms":6000}'
time make order
```

**Predict:** how long does it take? What status? What does *inventory* log?

<details><summary>Answer</summary>

About **5 seconds** (not 6), `502`. Real response:

```
{"error":"checkout: charge: Post \"http://payment:8082/charge\": net/http: request canceled (Client.Timeout exceeded while awaiting headers)"}
```

Checkout's client gave up first (it started its 5s timer first). That **cancelled** the request to payment, which
cancelled payment's `ctx`, which cancelled payment's request to inventory, which cancelled inventory's `ctx`.
Inventory was sitting in `Maybe`'s `select`, saw `ctx.Done()`, and stopped waiting. Real logs:

```
payment    ERROR "inventory call failed"  error="Post \"http://inventory:8083/reserve\": context canceled"
inventory  ERROR "reserve failed"         error="context canceled"
```

That's the `ctx` backpack carrying a **cancel signal** across three services. Without it, inventory would keep
working for a caller who already left.
</details>

```bash
make heal-inventory
```

---

## Lab 7: Flaky means random

```bash
make flaky-inventory
for i in $(seq 1 20); do curl -s -o /dev/null -w '%{http_code} ' -X POST localhost:8081/checkout -d '{"item_id":"sku-mouse","quantity":1}'; done; echo
make heal-inventory
```

**Predict:** how many 502s out of 20?

<details><summary>Answer</summary>

About 6 (30% of 20), but it varies. Our run got **8**:
`200 200 200 502 200 200 200 502 200 502 200 502 200 200 502 502 200 502 200 502`.
That randomness is why real error rates are measured over many requests (metrics, Phase 5), never one.
</details>

---

## Lab 8: Slow, but not broken

```bash
make slow-inventory
time make order
make heal-inventory
```

**Predict:** total time? Status? Any ERROR logs?

<details><summary>Answer</summary>

~0.9s (800ms injected + the normal ~60ms), status `200`, no errors. Everything "works", it's just slow.
Logs alone hardly show the problem: you'd have to compare timestamps by hand. A trace waterfall (Phase 3)
shows it instantly: `stock.reserve` becomes a huge bar. This is why traces exist alongside logs.
</details>

---

## Lab 9: Two faults at once

```bash
curl -s -X PUT localhost:8082/admin/fault -d '{"error_rate":0.5}'
curl -s -X PUT localhost:8083/admin/fault -d '{"error_rate":0.5}'
for i in $(seq 1 10); do make order 2>/dev/null | tail -1; echo; done
```

**Predict:** what different error messages will you see? Roughly what fraction of orders succeed?

<details><summary>Answer</summary>

Two kinds of error: `... payment: injected fault` (payment's authorize failed, inventory never called) and
`... inventory: injected fault` (authorize passed, inventory failed). About **25%** succeed: payment must pass (50%)
*and* inventory must pass (50%) → 0.5 × 0.5 = 0.25.
Each failed trace has exactly one root cause, but it differs from trace to trace. That's what Phase 5's
"error grouping" will summarize: "60% of errors come from payment, 40% from inventory".
</details>

```bash
curl -s -X DELETE localhost:8082/admin/fault; make heal-inventory
```

---

## Lab 10 (code change): See what a dropped context looks like

Open [services/inventory/main.go](../../../services/inventory/main.go) and change one line, temporarily:

```go
log.InfoContext(ctx, "reserving stock", ...)   // before
log.Info("reserving stock", ...)               // after: no ctx!
```

Rebuild only inventory and place an order:

```bash
docker compose up --build -d inventory
make order
docker compose logs --no-log-prefix inventory | tail -3
```

**Predict:** what trace_id does that line have?

<details><summary>Answer</summary>

`"trace_id":"00000000000000000000000000000000","span_id":"0000000000000000"`.
The line is now orphaned: no search by trace_id will ever find it. That's why rule #1 exists.

**Undo the change** (change `log.Info(` back to `log.InfoContext(ctx, `) and rebuild inventory again.
</details>

---

## Lab 11: Wrong method, bad input

```bash
curl -s -i localhost:8081/checkout                                # GET instead of POST
curl -s -i -X POST localhost:8081/checkout -d '{"item":"x"}'      # typo'd field
curl -s -X POST localhost:8081/checkout -d '{"item_id":"sku-banana","quantity":1}'
curl -s -X PUT localhost:8083/admin/fault -d '{"error_rate":2}'
```

**Predict** each status code.

<details><summary>Answer</summary>

1. `405 Method Not Allowed`: the route is `POST /checkout` only.
2. `400`: `DisallowUnknownFields` rejects `item`.
3. `400` `{"error":"checkout: unknown item \"sku-banana\""}`: payment is never called.
4. `400` `error_rate must be 0..1 and latency_ms >= 0`: the fault endpoint validates input.

None of these mark a server span as Error (all 4xx).
</details>
