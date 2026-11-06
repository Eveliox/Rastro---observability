# 04: Traces, spans and propagation

> **In plain words:** a **span** is a stopwatch reading for one piece of work ("reserving stock took 6ms, and it failed").
> A **trace** is all the stopwatch readings for one customer request, arranged as a family tree.
> **Propagation** is how the trace ID is passed from one service to the next: written into an HTTP header.

---

## 1. What a span is

A span records one unit of work. Think of it as one line in a stopwatch log, with some extra labels:

| Field | Example | Plain meaning |
|---|---|---|
| **trace_id** | `652ac6d062c3dda021d1a6a00adae57a` | Which request this belongs to (32 hex chars = 16 bytes) |
| **span_id** | `eb8c140ddb9de5e8` | This span's own ID (16 hex chars = 8 bytes) |
| **parent span_id** | `…` | Which span this one happened *inside* |
| **name** | `stock.reserve` | What the work was |
| **start / end time** | 06:54:21.576 → .582 | Gives the duration |
| **kind** | server / client / internal | Was this receiving a call, making a call, or just local work? |
| **status** | Unset / OK / Error | Did it fail? |
| **attributes** | `item.id = sku-keyboard` | Extra key/value labels for searching |
| **events** | `exception: injected fault` | Timestamped things that happened during the span |
| **resource** | `service.name = inventory` | Which program produced it |

"Hex" (hexadecimal) just means digits 0–9 plus a–f. It's a compact way to write random bytes.

## 2. What a trace is

All spans with the same trace_id, linked by their parent IDs, form a tree. Here's the tree for one
successful `make order`:

```
POST /checkout                     checkout   server    ← root span (no parent)
├── pricing.lookup                 checkout   internal
└── HTTP POST                      checkout   client    ← the call OUT to payment
    └── POST /charge               payment    server    ← payment receiving it
        ├── card.authorize         payment    internal
        ├── HTTP POST              payment    client    ← the call OUT to inventory
        │   └── POST /reserve      inventory  server
        │       └── stock.reserve  inventory  internal
        └── card.capture           payment    internal
```

Draw it as bars on a timeline and you get a **waterfall**, which is what Phase 3 builds in React:

```
time →     0ms        10        20        30        40        50       56
POST /checkout   ███████████████████████████████████████████████████████
 pricing.lookup  ▏
 HTTP POST        ██████████████████████████████████████████████████████
  POST /charge     █████████████████████████████████████████████████████
   card.authorize  ████████████████████
   HTTP POST                          ███████
    POST /reserve                     ██████
     stock.reserve                     █████
   card.capture                              ███████████████████████
```

(Bars are roughly to scale. Real numbers from our happy run: checkout 56ms total, payment 52ms, inventory 6ms.
`pricing.lookup` is a map lookup, so it's basically instant.)

**Reading a waterfall:** the widest bar that isn't explained by its children is where time went.
In the slow-inventory lab, `stock.reserve` becomes a huge bar, and every bar above it stretches to fit.

### The three kinds of span in our code

| Kind | Who creates it in miniobs | Example name |
|---|---|---|
| **server** | `otelhttp.NewHandler` in `httpx.Serve`, automatically, for each incoming request | `POST /reserve` |
| **client** | `otelhttp.NewTransport` in `httpx.NewClient`, automatically, for each outgoing request | `HTTP POST` |
| **internal** | **Us**, by calling `tracer.Start(...)` | `stock.reserve`, `card.authorize`, `pricing.lookup` |

Why both a client span *and* a server span for one call? Because they measure different things.
The client span (in payment) includes the network trip. The server span (in inventory) only covers
inventory's own work. If the client span is 500ms and the server span is 6ms, the network (or a queue)
ate 494ms. That's a real debugging technique.

## 3. Making our own spans

From [services/inventory/main.go](../../../services/inventory/main.go):

```go
ctx, span := tracer.Start(ctx, "stock.reserve", trace.WithAttributes(
    attribute.String("item.id", req.ItemID),
    attribute.Int("item.quantity", req.Quantity),
))
defer span.End()
```

Line by line:

- `tracer` was made once at the top: `var tracer = otel.Tracer(serviceName)`. A **tracer** is a span factory.
- `tracer.Start(ctx, name, …)` looks in `ctx` for the current span (here: `POST /reserve`), makes a **child**
  of it, and returns a **new ctx** holding the child. Assigning it back to `ctx` matters: anything we call
  afterwards with `ctx` will nest under `stock.reserve`.
- `trace.WithAttributes(…)` adds searchable labels. Later you'll be able to ask "show me slow spans where item.id = sku-monitor".
- `defer span.End()` stops the stopwatch when the function returns.

When something fails:

```go
span.RecordError(err)                    // adds an "exception" EVENT to the span (the error message, type)
span.SetStatus(codes.Error, err.Error()) // marks the span's STATUS as Error (makes it red)
```

These are two different things, and the difference matters a lot in Phase 3:

- **Status = Error** means "this span failed". It can be set by us *or* automatically by otelhttp from a status code.
- **An exception event** is only added where the error *actually happened* (where we called `RecordError`).

## 4. Which spans turn red? (the real rules)

otelhttp sets span status from the HTTP status code. We checked the library source (otelhttp v0.72.0):

- **Server spans** are Error only for **5xx**. (A 4xx means the *caller* messed up, not the server.)
- **Client spans** are Error for **4xx and 5xx**. (From the caller's view, "my call didn't work".)
- A network failure (connection refused, timeout) marks the client span Error too.
- otelhttp **never** adds an exception event for a status code. Only our `RecordError` calls do.

So here is the full picture for two scenarios:

**Inventory fault (`make break-inventory`):**

| Span | Status | Own exception event? | Why |
|---|---|---|---|
| POST /checkout | 🔴 Error | no | returned 502 |
| ├ pricing.lookup | ⚪ | no | worked |
| └ HTTP POST (checkout→payment) | 🔴 Error | no | got 502 |
| &nbsp;&nbsp;└ POST /charge | 🔴 Error | no | returned 502 |
| &nbsp;&nbsp;&nbsp;&nbsp;├ card.authorize | ⚪ | no | worked |
| &nbsp;&nbsp;&nbsp;&nbsp;└ HTTP POST (payment→inventory) | 🔴 Error | no | got 500 |
| &nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;└ POST /reserve | 🔴 Error | no | returned 500 |
| &nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;└ **stock.reserve** | 🔴 Error | **yes** | **the fault fired here** |

(`card.capture` never runs: payment returns early.)

Six red spans, one actual problem. **Root cause = the deepest red span that has its own exception event.**
That rule is exactly what Phase 3's root-cause finder will implement. Everything above it just *inherited* the failure.

**Out of stock (`quantity: 999` monitors):**

| Span | Status | Why |
|---|---|---|
| POST /checkout | ⚪ | returned 409 (4xx: server spans stay unset) |
| └ HTTP POST (checkout→payment) | 🔴 Error | client span got a 4xx |
| &nbsp;&nbsp;└ POST /charge | ⚪ | returned 409 |
| &nbsp;&nbsp;&nbsp;&nbsp;└ HTTP POST (payment→inventory) | 🔴 Error | client span got a 4xx |
| &nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;└ POST /reserve | ⚪ | returned 409 |
| &nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;└ stock.reserve | ⚪ | we deliberately don't mark it; we add `stock.insufficient = true` |

Note the nuance: our code treats out-of-stock as a normal business outcome, and the server spans agree.
But otelhttp's client spans still show red, because by convention any 4xx is a failed *call*.
No span has an exception event, so a root-cause finder using our rule finds nothing, which is correct.
**Keep this in mind for Phase 3**: "has status Error" alone would give false alarms here.

## 5. Propagation: how the trace ID crosses the network

Inside one program, the span travels in `ctx`. But `ctx` is a Go thing in memory; it can't cross the network.
So at the edge of each service, the trace context is **written into an HTTP header** and **read back out** on the other side.

### The `traceparent` header

This is a W3C (web standards body) standard, so every language and every vendor speaks it.

```
traceparent: 00-652ac6d062c3dda021d1a6a00adae57a-71c8a5fa23197c92-01
             ││ └──────────── trace_id ────────────┘ └── parent ───┘ └┤
             ││                                       span_id        flags: 01 = "sampled"
             └┴ version (always 00 today)                                  (please record this trace)
```

The step by step, for payment calling inventory:

```
PAYMENT                                              INVENTORY
ctx holds span "HTTP POST" (id X, trace T)
  │
  │ otelhttp.Transport: "inject"
  │   writes  traceparent: 00-T-X-01
  │
  └──────────── HTTP request over network ───────────►  otelhttp.Handler: "extract"
                                                          reads traceparent
                                                          → "I'm in trace T, my parent is X"
                                                          starts span "POST /reserve" (trace T, parent X)
                                                          puts it in r.Context()
```

"Inject" and "extract" are the official words: the **propagator** injects context into outgoing headers and
extracts it from incoming headers.

### Proof that it's really just a header

We sent inventory a request directly, with a **made-up** `traceparent` full of 1s:

```bash
curl -X POST localhost:8083/reserve \
  -H 'traceparent: 00-11111111111111111111111111111111-2222222222222222-01' \
  -d '{"order_id":"ord-handmade","item_id":"sku-mouse","quantity":1}'
```

Inventory's log:

```json
{"msg":"reserving stock","service":"inventory","order_id":"ord-handmade",
 "trace_id":"11111111111111111111111111111111","span_id":"c3d569e4be80d405"}
```

Inventory happily joined trace `1111…`. It has no idea who called it. It just trusts the header.
That's the whole "magic". (You'll do this yourself in lab 4.)

### What if there's no header?

Then `otelhttp.Handler` starts a **new trace** with a fresh random trace_id. That's what happens with
`make order`: curl sends no `traceparent`, so checkout becomes the root.

### Baggage

[telemetry.go](../../../internal/telemetry/telemetry.go) also enables `propagation.Baggage{}`. Baggage is a second header
for passing your *own* key/values along the chain (e.g. `customer_tier=gold`). We don't use it yet; it's on so
that it would just work if we did.

## 6. The setup code ([internal/telemetry/telemetry.go](../../../internal/telemetry/telemetry.go))

Every service calls `telemetry.Setup(ctx, "inventory")` first thing. It configures OpenTelemetry's
global pieces. Here's what each one is, with an analogy of a **film crew**:

| Piece | Code | Film crew analogy | What it does |
|---|---|---|---|
| **Resource** | `resource.New(… service.name …)` | The name on the clapperboard | Stamps every span with *who* made it |
| **TracerProvider** | `sdktrace.NewTracerProvider(opts...)` | The production office | Creates tracers, owns the config, decides where spans go |
| **Tracer** | `otel.Tracer("inventory")` | A camera operator | Starts spans |
| **Sampler** | (default) | The director deciding what to keep | Default = `ParentBased(AlwaysSample)`: keep everything, and follow the caller's decision (that `-01` flag) |
| **Exporter** | `otlptracehttp.New(ctx)` | The courier taking footage to the studio | Sends finished spans over the network using **OTLP** |
| **Batcher** | `sdktrace.WithBatcher(exp)` | Collecting footage in a box before the courier leaves | Buffers spans in memory, sends them in groups in the background |
| **Propagator** | `otel.SetTextMapPropagator(…TraceContext…)` | The note passed to the next crew | Reads/writes `traceparent` |

`otel.SetTracerProvider(tp)` and `otel.SetTextMapPropagator(...)` make these **global**, which is why
`otel.Tracer(...)` and `otelhttp` can find them anywhere without us passing them around.

### The important `if`: why no spans are sent yet

```go
if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
    exp, err := otlptracehttp.New(ctx)
    opts = append(opts, sdktrace.WithBatcher(exp))
}
```

In Phase 1 nothing sets `OTEL_EXPORTER_OTLP_ENDPOINT` (look at the commented-out line in
[docker-compose.yml](../../../docker-compose.yml)). So there's **no exporter**: spans are created, get real IDs,
those IDs propagate and appear in logs, and when a span ends... it's dropped.

Phase 2 builds the `ingest` service and sets `OTEL_EXPORTER_OTLP_ENDPOINT=http://ingest:4318`, and the same
code starts shipping spans. No code changes in the services. That's the payoff of following the standard.

### Why a batcher instead of sending each span immediately?

Sending one network request per span would make every request slower (you'd wait on the network) and would flood
the receiver. The batcher collects spans in memory and a background goroutine ships them every few seconds or
when the batch is full. The request never waits. You'll see the same "batch it" idea again in Phase 2 for
ClickHouse inserts (project rule #2).

That's also why `Setup` returns `tp.Shutdown`: on exit, flush whatever's still in the box, or those spans are lost.

## 7. OpenTelemetry, OTLP, otelhttp: which is which?

| Name | What it is |
|---|---|
| **OpenTelemetry (OTel)** | The open standard + libraries for producing traces, logs and metrics. Vendor-neutral. |
| **OTel SDK** (`go.opentelemetry.io/otel/sdk`) | The Go implementation: TracerProvider, batcher, sampler... |
| **OTel API** (`go.opentelemetry.io/otel`, `/trace`) | The small interface our code calls: `otel.Tracer`, `span.End()`... |
| **otelhttp** | A ready-made wrapper that adds spans + propagation to Go's `net/http` |
| **OTLP** | OpenTelemetry Protocol: the *format* for sending telemetry over the wire (protobuf over HTTP or gRPC) |
| **W3C Trace Context** | The standard behind the `traceparent` header |

## Check yourself

1. A client span takes 900ms, the server span it calls takes 8ms. Where did the time go?
2. Why does `tracer.Start` return a new `ctx`?
3. In the out-of-stock case, which spans are red, and why isn't that a real problem?
4. What single environment variable turns on span exporting?
5. What does the `-01` at the end of a traceparent mean?
