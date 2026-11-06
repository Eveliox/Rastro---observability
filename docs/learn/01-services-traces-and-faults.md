# 01: Services, traces and breaking things on purpose

## What problem are we solving?

When one request touches 3 programs and fails, each program writes its own logs, in its own place.
Without help, you'd open 3 log files and try to match up lines by timestamp. That breaks down fast once thousands of requests are interleaved.

**Observability** means being able to answer "what happened to *this* request?" from the outside, without adding new code.
The three kinds of data ("pillars") are:

| Pillar  | Answers                            | Example                                            |
|---------|------------------------------------|----------------------------------------------------|
| Traces  | *Where* did the time/error go?     | checkout 120ms → payment 95ms → inventory 6ms ❌   |
| Logs    | *What* exactly happened?           | `"reserve failed", error="injected fault"`         |
| Metrics | *How much / how often*, over time? | 3% of checkouts failed in the last 5 min           |

Phase 1 sets up traces and logs so that they're **connected**.

## Traces and spans

- A **span** is one unit of work with a start time, a duration, a name and a status (OK or error).
  For example: `POST /reserve took 6ms`.
- Spans nest. `stock.reserve` is a **child** of `POST /reserve` because it happened inside it.
- A **trace** is the tree of every span for one request. All spans in it share one **trace_id**.
  Each span has its own **span_id** and remembers its **parent's** span_id.

The trace for one checkout:

```
POST /checkout                         (checkout, server span)
├── pricing.lookup                     (checkout, our own span)
└── HTTP POST                          (checkout, client span: the call out)
    └── POST /charge                   (payment, server span)
        ├── card.authorize
        ├── HTTP POST                  (payment → inventory)
        │   └── POST /reserve          (inventory, server span)
        │       └── stock.reserve  ❌  ← the fault fires here
        └── card.capture
```

## How does the trace_id cross from one program to another?

**Context propagation.** When payment calls inventory, `otelhttp` adds an HTTP header:

```
traceparent: 00-f9975651c79095198f2c82349c3bcd04-7a22e859fea735b2-01
             │  └──────── trace_id ────────────┘ └─ parent span ─┘ └ sampled
             version
```

Inventory's `otelhttp` handler reads the header and says: "I'm continuing trace f997…, and my parent is span 7a22…".
That's all the magic there is. It's a standardized header (W3C Trace Context), so any language and any vendor understands it.

In Go, the current span lives inside `ctx` (a `context.Context`). That's why the code passes `ctx` everywhere.
**If you drop `ctx`, the trace breaks in two.**

## Why every log line has trace_id and span_id

`internal/telemetry/log.go` wraps Go's JSON logger. Before writing each line, it pulls the span out of `ctx` and adds its IDs:

```json
{"level":"ERROR","msg":"reserve failed","service":"inventory","error":"injected fault",
 "trace_id":"f9975651c79095198f2c82349c3bcd04","span_id":"134307bfbf891919"}
```

Now "show me everything about this failed request" is a single search: `trace_id = f997…`.
Phase 4 turns that search into a UI. Clicking a span in the waterfall will show the logs written during that exact span.

An all-zero trace_id means the code logged with `log.Info(...)` instead of `log.InfoContext(ctx, ...)`, so no context, no IDs.

## Fault injection

Every service has `/admin/fault`. Setting `{"error_rate": 1.0}` on inventory makes `stock.reserve` fail.
The error then **bubbles up**:

| Service   | Status | Why                                              |
|-----------|--------|--------------------------------------------------|
| inventory | 500    | Internal Server Error: *I* broke                 |
| payment   | 502    | Bad Gateway: *the thing behind me* broke         |
| checkout  | 502    | Bad Gateway: same                                |

All three spans end up red. But only `stock.reserve` has its **own** error (an `exception` event recorded with `span.RecordError`).
The others just inherited it. Phase 3's root-cause finder uses exactly this: *find the deepest span with its own error*.

Also note that "out of stock" is a **409 Conflict** and is *not* marked as a span error.
It's a normal business outcome, and paging an engineer at 3am because a monitor sold out would be wrong.
Deciding what counts as an "error" is a big part of real observability work.

## Docker pieces

- **Multi-stage Dockerfile.** Stage 1 has the Go compiler (~300MB) and builds the program.
  Stage 2 copies only the finished binary into `distroless` (~10MB: no shell, no package manager).
  The result is a smaller image with less for an attacker to use.
- **One Dockerfile, three services.** `--build-arg SERVICE=inventory` picks which `services/<name>` to compile.
- **Compose networking.** Compose puts the containers on a private network where each service's name is its hostname.
  That's why `PAYMENT_URL=http://payment:8082` works.
- **Ports `"8083:8083"`** = `host:container`. They let your Mac reach `localhost:8083`.

## Try it yourself

```bash
make up
make order                 # 200, note the X-Trace-Id header
make break-inventory
make order                 # 502, copy the X-Trace-Id
docker compose logs --no-log-prefix | grep <that trace id>
make flaky-inventory       # 30% failure. Run `make order` several times.
make slow-inventory        # every order now takes ~0.9s
make heal-inventory
```

**Exercise:** break *payment* instead (`curl -X PUT localhost:8082/admin/fault -d '{"error_rate":1}'`).
Which span is the root cause now? Does inventory get called at all? (Hint: look at the order of steps in `services/payment/main.go`.)

## Glossary

- **OTel / OpenTelemetry**: the vendor-neutral standard and SDKs for producing traces, logs and metrics.
- **OTLP**: OpenTelemetry's wire protocol for sending that data. Phase 2 builds a receiver for it.
- **Exporter**: the SDK piece that sends spans somewhere. Phase 1 has none, so spans get IDs but go nowhere.
- **Propagator**: the SDK piece that reads and writes `traceparent` headers.
- **Resource**: who produced the data (`service.name=inventory`).
