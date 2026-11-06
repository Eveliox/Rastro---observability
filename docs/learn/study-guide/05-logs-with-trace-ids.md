# 05: Logs that know their trace

> **In plain words:** a log line is a note a program writes about what it's doing.
> We write ours as JSON, and we stamp every single one with the trace ID and span ID of the request
> it belongs to. That way, a trace (where it broke) links straight to the logs (what exactly happened).

---

## 1. Plain logs vs structured logs

A **plain** log line is a sentence:

```
2026-11-05 06:54:21 ERROR reserve failed for order ord-f256d588: injected fault
```

Readable by a human, painful for a computer. To find all errors for `sku-keyboard`, you'd write fragile
text-matching patterns.

A **structured** log line is data, usually JSON:

```json
{"time":"2026-11-05T06:54:21.676790336Z","level":"ERROR","msg":"reserve failed","service":"inventory",
 "error":"injected fault","trace_id":"6aa6137f81bd25de0d2c4be58cecdcbd","span_id":"5c08ffa50e47f57a"}
```

Every piece of information has a name. A computer can filter `level = ERROR AND service = inventory`
directly. Phase 4 will store these fields as columns in ClickHouse, which is only possible because they're structured.

### Log levels

| Level | Used for | Example in miniobs |
|---|---|---|
| `DEBUG` | Very detailed, usually hidden | (none yet; enabled so we can add some) |
| `INFO` | Normal things worth knowing | `checkout started`, `stock reserved` |
| `WARN` | Something odd, but handled | `out of stock`, `fault injection enabled` |
| `ERROR` | Something failed | `reserve failed`, `checkout failed` |

Notice: out of stock is `WARN`, not `ERROR`. Same reasoning as the span status: it's a normal business outcome.

## 2. Go's `slog`: Logger vs Handler

Go's standard library has `log/slog` ("structured log"). It splits logging into two jobs:

- **Logger**: what your code talks to. `log.InfoContext(ctx, "stock reserved", "remaining", 996)`.
  It packages the message and key/value pairs into a **Record**.
- **Handler**: decides what to do with a Record: format it (JSON? text?) and write it somewhere (stdout? file?).

```
your code ──► Logger ──Record──► Handler ──► bytes on stdout
```

Key/value pairs are passed as alternating arguments: `"order_id", orderID, "item_id", req.ItemID`
becomes `"order_id":"ord-…","item_id":"sku-…"`.

## 3. Our handler: [internal/telemetry/log.go](../../../internal/telemetry/log.go)

```go
func NewLogger(serviceName string) *slog.Logger {
    base := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
    return slog.New(traceHandler{base}).With("service", serviceName)
}
```

1. `base` is Go's built-in JSON handler, writing to **stdout** (standard output: the program's normal output stream,
   which Docker captures and shows in `docker compose logs`).
2. We wrap it in our own `traceHandler`.
3. `.With("service", serviceName)` permanently adds `"service":"inventory"` to every line from this logger.

### The wrapper (the decorator pattern)

```go
type traceHandler struct{ slog.Handler }   // embeds the real handler

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
    sc := trace.SpanContextFromContext(ctx)               // 1. open the backpack, find the span
    r.AddAttrs(
        slog.String("trace_id", sc.TraceID().String()),   // 2. add its IDs to the record
        slog.String("span_id", sc.SpanID().String()),
    )
    return h.Handler.Handle(ctx, r)                       // 3. pass it on to the real JSON handler
}
```

Analogy: a mail room clerk who **stamps every envelope** with a tracking number, then hands it to the regular mail
carrier. The carrier (JSON handler) doesn't know or care about tracking numbers; the clerk added them.

This is called the **decorator pattern**: same interface, one extra behavior, wrapped around the original.
Because `traceHandler` embeds `slog.Handler`, it gets all the other required methods for free and only overrides `Handle`.

### The two "boring" methods that prevent a sneaky bug

```go
func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
    return traceHandler{h.Handler.WithAttrs(attrs)}
}
```

When we call `.With("service", …)`, slog calls the handler's `WithAttrs`, which returns a *new* handler.
If we didn't override this, the embedded (inner) handler's `WithAttrs` would run and return a **plain JSON handler**:
our wrapper would silently fall off and every line would lose its trace IDs. Overriding it re-wraps the result.
`WithGroup` is the same idea.

## 4. Why `InfoContext(ctx, …)` and never `Info(…)`

Rule #1 in [CLAUDE.md](../../../CLAUDE.md). The handler can only find the span if it's given the `ctx`:

| Call | What the handler gets | Result |
|---|---|---|
| `log.InfoContext(ctx, "x")` | the request's backpack | real `trace_id` and `span_id` |
| `log.Info("x")` | an empty backpack (`context.Background()`) | `"trace_id":"00000000000000000000000000000000"` |

An **all-zero trace_id is a bug signal**: someone dropped the context. We checked every log line the services
had written during our test runs:

```bash
docker compose logs --no-log-prefix | grep -c '"trace_id":"00000000000000000000000000000000"'
0
```

Zero. Every line was linked to a trace.

## 5. Real logs for one failed order

From `make break-inventory` then `make order` (trace `6aa6137f…`), sorted by time, trimmed for width:

```
06:54:21.637  checkout   INFO   checkout started        span 442c55f5816491b6
06:54:21.638  payment    INFO   charging                span bac8505dc13205cc
06:54:21.671  inventory  INFO   reserving stock         span 5c08ffa50e47f57a
06:54:21.676  inventory  ERROR  reserve failed          span 5c08ffa50e47f57a   error="injected fault"
06:54:21.677  payment    ERROR  inventory call failed   span bac8505dc13205cc   error="upstream returned 500: inventory: injected fault"
06:54:21.677  checkout   ERROR  checkout failed         span 442c55f5816491b6   error="upstream returned 502: payment: reserve stock: upstream returned 500: inventory: injected fault"
```

Things to notice:

- **One trace_id, three services.** One `grep` found all of them.
- **Three ERROR lines, one real problem.** Each service honestly reports its own failure. The traces tell you
  which one is the origin (inventory's: the deepest, and the only one that says `injected fault` by itself).
- **The error message grows as it travels up**, because each layer wraps it with `%w`.
- **Each service has one span_id across its lines.** That's the *server span* (`POST /reserve`, `POST /charge`,
  `POST /checkout`), because the handler logs with the handler's `ctx`.

### A subtle detail: which span does a log belong to?

Inventory's `"reserve failed"` line has span `5c08ffa5…`, which is **`POST /reserve`**, not `stock.reserve`.
Why? Look at where it's logged:

```go
mux.HandleFunc("POST /reserve", func(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()                         // ← holds POST /reserve
    remaining, err := db.reserve(ctx, ...)     //   reserve() makes stock.reserve in ITS OWN ctx
    ...
    log.ErrorContext(ctx, "reserve failed", …) // ← this ctx still holds POST /reserve
```

`reserve()` created a child ctx inside itself, but that ctx never came back out. If you logged *inside* `reserve()`
with its `ctx`, the line would carry `stock.reserve`'s span_id. Neither is wrong; just know a log line belongs to
whichever span was in the ctx you passed.

### Why `docker compose logs` looks out of order

`docker compose logs` prints container by container, not by time. Our raw grep output showed payment lines,
then inventory, then checkout. To see the real story you sort by the `time` field. Phase 4 does that for you in the UI.

## 6. How this pays off later

- **Phase 2/3**: spans stored in ClickHouse with their trace_id and span_id.
- **Phase 4**: logs stored in ClickHouse with the *same* trace_id and span_id. Click a span in the waterfall →
  `SELECT … FROM logs WHERE trace_id = ? AND span_id = ?` → the exact log lines written during that span.

That one join is why rule #1 exists.

## Check yourself

1. What would `log.Error("reserve failed")` (no Context) print as its trace_id?
2. What goes wrong if `traceHandler` doesn't override `WithAttrs`?
3. Why is out-of-stock logged as WARN?
4. Where do the services' logs physically go, and how do you see them?
