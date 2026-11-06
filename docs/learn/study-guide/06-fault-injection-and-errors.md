# 06: Fault injection and how errors travel

> **In plain words:** every service has a hidden "break me" switch at `/admin/fault`. You can tell it
> "fail 30% of requests" or "add 800ms of delay" while it's running. We need this because an observability
> platform is useless to test on a system that never breaks.

---

## 1. Why break things on purpose?

- **To have something to look at.** Phases 3–6 build a waterfall, root-cause finder, error grouping and alerts.
  You can't test any of them without failures.
- **To practice.** Real companies do this too. It's called **chaos engineering** (Netflix's "Chaos Monkey" randomly
  kills servers in production to make sure the system copes). Fire drills for software.
- **To make failures repeatable.** "It fails sometimes" is impossible to debug. "It fails 100% when I flip this switch" is easy.

Two kinds of breakage cover most real incidents:

| Fault | Setting | Real-world equivalent |
|---|---|---|
| **Errors** | `error_rate` 0.0–1.0 | a database is down, a bug throws, a disk is full |
| **Latency** | `latency_ms` | a slow query, an overloaded server, a bad network |

## 2. The API

Every service (checkout 8081, payment 8082, inventory 8083) has:

```
GET    /admin/fault                                    → show current fault
PUT    /admin/fault   {"error_rate":0.3,"latency_ms":200}  → set it
DELETE /admin/fault                                    → clear it
```

The Makefile shortcuts target inventory:

| Command | Sends |
|---|---|
| `make break-inventory` | `PUT {"error_rate":1.0}` |
| `make flaky-inventory` | `PUT {"error_rate":0.3}` |
| `make slow-inventory` | `PUT {"latency_ms":800}` |
| `make heal-inventory` | `DELETE` |
| `make fault-status` | `GET` on all three |

Note: `PUT` **replaces** the whole config. `make slow-inventory` after `make break-inventory` gives you
`{"error_rate":0,"latency_ms":800}`: the error rate is gone. To get both, send both: `-d '{"error_rate":0.5,"latency_ms":300}'`.

The fault lives in memory, so **restarting a container heals it**.

## 3. The code: [internal/fault/fault.go](../../../internal/fault/fault.go)

### The data

```go
type Config struct {
    ErrorRate float64 `json:"error_rate"` // 0.0 = never fail, 1.0 = always fail
    LatencyMS int     `json:"latency_ms"` // extra delay added to every request
}

type Injector struct {
    mu  sync.RWMutex
    cfg Config
    log *slog.Logger
}
```

The `Injector` holds the current `Config` behind a **read/write lock**. Why a lock? Hundreds of request goroutines
read the config at the same time you might be writing a new one with `PUT`. Without the lock that's a **data race**:
a reader could see a half-written value. `RWMutex` lets all readers in together and makes a writer wait for exclusive access.

`get()` and `set()` are tiny methods that take the lock, so nothing else ever touches `cfg` directly.

### `Maybe(ctx)`: the function that actually breaks things

```go
func (i *Injector) Maybe(ctx context.Context) error {
    c := i.get()
    if c.LatencyMS > 0 {
        select {
        case <-time.After(time.Duration(c.LatencyMS) * time.Millisecond):  // waited long enough
        case <-ctx.Done():                                                  // caller gave up
            return ctx.Err()
        }
    }
    if c.ErrorRate > 0 && rand.Float64() < c.ErrorRate {
        return ErrInjected
    }
    return nil
}
```

In plain words:

1. **Latency:** wait `latency_ms`... *unless* the request is cancelled first. `select` waits for whichever happens
   first. This is good manners: if the caller hung up, don't keep sleeping for nobody. (Lab 6 shows this:
   inventory logs `"error":"context canceled"` when checkout's timeout fires.)
2. **Error:** roll a random number between 0 and 1. If it's below `error_rate`, fail.
   With `0.3`, about 30% of rolls are below 0.3. With `1.0`, every roll is. With `0`, the check is skipped.

`ErrInjected` is a named error (`var ErrInjected = errors.New("injected fault")`) so callers can recognize it
with `errors.Is(err, fault.ErrInjected)`. Checkout uses that to return `500` (our fault) instead of `400` (your fault).

### `Handler()`: the HTTP side

A `switch` on the method:

- `GET`: do nothing, fall through to "write current config".
- `PUT`/`POST`: decode JSON, **validate** (`error_rate` must be 0..1, `latency_ms` ≥ 0, else `400`), save, log a `WARN`.
- `DELETE`: save an empty `Config{}` (all zeros = healthy), log `INFO`.
- anything else: `405 Method Not Allowed`.

Then reply with the current config as JSON, which is why `make break-inventory` prints `{"error_rate":1,"latency_ms":0}`.

The fault changes are logged with `ctx` too, so they have trace IDs:
`{"level":"WARN","msg":"fault injection enabled","service":"inventory","error_rate":1,…,"trace_id":"09be58ad…"}`.
Later, an incident timeline could show "fault enabled at 06:47:49" right next to the error spike.

## 4. Where each service calls `Maybe`

This decides **which span** becomes the root cause:

| Service | Where `Maybe` is called | If it fires, status returned | Downstream still called? |
|---|---|---|---|
| checkout | inside `pricing.lookup` | `500` | no, payment is never called |
| payment | inside `card.authorize` only (`card.capture` gets `nil`) | `500` | no, inventory is never called |
| inventory | inside `stock.reserve` | `500` | (it's the bottom) |

We tested breaking payment:

```
{"error":"checkout: charge: upstream returned 500: payment: injected fault"}
```

and payment's logs show `"card authorization failed"` with no `"reserving stock"` line from inventory for that trace.
Root cause: `card.authorize`.

Each place that calls `Maybe` does the same three things on failure:

```go
span.RecordError(err)                    // exception event: "the error happened HERE"
span.SetStatus(codes.Error, err.Error()) // status: red
return 0, err                            // hand it up to the caller
```

## 5. How an error travels up the chain

Let's follow `make break-inventory` + `make order` (the real messages):

```
inventory  stock.reserve:  Maybe() returns ErrInjected              "injected fault"
           handler wraps:  fmt.Errorf("inventory: %w", err)         "inventory: injected fault"
           responds 500   {"error":"inventory: injected fault"}
                │
payment    PostJSON sees 500 → *UpstreamError{500, "inventory: injected fault"}
           handler wraps:  "payment: reserve stock: upstream returned 500: inventory: injected fault"
           responds 502   (the thing behind me broke)
                │
checkout   PostJSON sees 502 → *UpstreamError{502, "..."}
           handler wraps:  "checkout: charge: upstream returned 502: payment: reserve stock: upstream returned 500: inventory: injected fault"
           responds 502
                │
you        HTTP/1.1 502 Bad Gateway
```

Read the final error message as a path: **checkout → charge → payment → reserve stock → inventory → injected fault**.
The last piece is the origin.

## 6. Choosing status codes: a design decision, not a detail

| Situation | Code | Who's at fault | Span error? |
|---|---|---|---|
| Malformed JSON, missing fields, unknown item | `400` | the caller | no |
| Out of stock | `409 Conflict` | nobody: it's a valid answer | no (business outcome) |
| Our own code / injected fault | `500` | me | **yes**, with exception event |
| A dependency failed | `502` | someone behind me | yes, status only (inherited) |
| Timeout calling a dependency | `502` | someone behind me | yes, status only |

**Why out-of-stock isn't an error** (this is a real-world lesson): if you count it as an error, then the day
a popular monitor sells out, your error rate jumps, alerts fire, and someone gets woken up at 3am for... a
successful sale. Deciding *what counts as an error* is one of the most important parts of observability work.
Phase 6's SLO alerts depend on getting this right.

## Check yourself

1. You run `make break-inventory` then `make slow-inventory`. Is inventory still failing?
2. Why does `Maybe` use `select` instead of just `time.Sleep`?
3. Payment's error_rate is 1.0. Which span is the root cause? Is `card.capture` in the trace?
4. Why does checkout return 502 and not 500 when inventory breaks?
