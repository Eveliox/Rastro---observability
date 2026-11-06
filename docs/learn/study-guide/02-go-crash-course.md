# 02: Go crash course (just enough to read this repo)

> **In plain words:** Go is a programming language made at Google for writing servers.
> It compiles to a single file you can run, it's fast, and it's built to handle many requests at once.
> This guide teaches only the Go features that actually appear in miniobs, using our own code as examples.

You don't need to memorize this. Read it once, then come back when a piece of code confuses you.

---

## 1. Module, package, import

- A **module** is the whole project. Ours is named in [go.mod](../../../go.mod):
  `module github.com/Eveliox/miniobs`. It's named like a URL by convention, not because it must be online.
- A **package** is one folder of `.go` files. `internal/fault/` is the package `fault`.
- **`package main`** is special: it means "this folder is a runnable program", and `func main()` is where it starts.
  Each of `services/checkout`, `services/payment`, `services/inventory` is a `package main`, so we get 3 programs.
- **`import`** pulls in other packages:

```go
import (
    "net/http"                                        // from Go's standard library
    "go.opentelemetry.io/otel"                        // a third-party library (listed in go.mod)
    "github.com/Eveliox/miniobs/internal/fault"       // our own package, by module path + folder
)
```

**Why `internal/`?** Go has a rule: code in a folder named `internal` can only be imported by code in the same
module. So nobody outside miniobs can depend on it. It's a "private, for us only" sign.

**go.mod vs go.sum:** `go.mod` lists the libraries we want. `go.sum` stores a fingerprint (hash) of each one,
so if someone tampers with a library download, the build fails. The `// indirect` ones are libraries
*our* libraries need.

## 2. Variables: `:=` vs `=`

```go
paymentURL := envOr("PAYMENT_URL", "http://localhost:8082")  // create a NEW variable (type inferred)
err = httpx.PostJSON(...)                                     // assign to an EXISTING variable
```

`:=` means "declare and assign". `=` means "just assign". You'll see both constantly.

Top-level (outside any function) uses `var` and `const`:

```go
const serviceName = "checkout"         // never changes
var tracer = otel.Tracer(serviceName)  // created once when the program starts
```

## 3. Functions return errors, not exceptions

Many languages "throw" errors. Go doesn't. A function that can fail returns the error as an extra return value,
and the caller checks it right away:

```go
amount, err := price(ctx, req, faults)   // two return values
if err != nil {                          // nil = "nothing" = no error
    // handle it, then usually return
}
```

You'll see `if err != nil` everywhere. It's verbose on purpose: every failure is handled where it happens.

`_ = something()` means "I know this returns something, and I'm choosing to ignore it."

### Wrapping errors: `%w`, `errors.Is`, `errors.As`

When an error passes up through layers, each layer adds context:

```go
fmt.Errorf("inventory: %w", err)   // "inventory: injected fault"
```

`%w` means **wrap**: the new error *contains* the old one, like a Russian nesting doll.
That's why the final error you saw was a chain:

```
checkout: charge: upstream returned 502: payment: reserve stock: upstream returned 500: inventory: injected fault
```

Read it left to right as "checkout said → because payment said → because inventory said → **injected fault**".

- `errors.Is(err, fault.ErrInjected)` opens the dolls and asks "is *this exact error* anywhere inside?"
- `errors.As(err, &up)` asks "is there an error of *this type* (`*UpstreamError`) inside? If so, give it to me."
  Checkout uses that to read the status code payment returned.

## 4. Structs and struct tags

A **struct** is a bundle of named fields, like a row in a spreadsheet:

```go
type checkoutRequest struct {
    ItemID   string `json:"item_id"`
    Quantity int    `json:"quantity"`
}
```

The text in backticks is a **tag**. `json:"item_id"` tells the JSON library:
"in JSON this field is spelled `item_id`". So `{"item_id":"sku-keyboard","quantity":1}` fills this struct.

**Capital letter = public.** `ItemID` (capital I) is visible outside the package; `units` (lowercase) is private.
The JSON library can only fill public fields, which is why struct fields here start with a capital.

## 5. Maps

A **map** is a lookup table (a dictionary):

```go
var prices = map[string]int{        // key: string, value: int
    "sku-keyboard": 4999,
}
unit, ok := prices[req.ItemID]      // ok is false if the key isn't there
```

The `value, ok :=` pattern is how Go says "look it up, and tell me whether it existed".

Side note: prices are in **cents** (`4999` = $49.99). Money is never stored as a decimal number
because computers can't represent most decimals exactly (`0.1 + 0.2` is `0.30000000000000004`).

## 6. Pointers: `&` and `*`

- `&x` = "the address of x" (where it lives in memory).
- `*Thing` = "a pointer to a Thing".

You need pointers when a function must **change** something, or when copying would be wrong:

```go
var req checkoutRequest
httpx.ReadJSON(w, r, &req)   // pass the address so ReadJSON can fill in OUR req
```

`fault.New` returns `*Injector` (a pointer) so every handler shares the *same* injector.
If each got a copy, changing the fault in one copy wouldn't affect the others.

## 7. Methods

A **method** is a function attached to a type. The bit before the name is the **receiver**:

```go
func (i *Injector) Maybe(ctx context.Context) error { ... }
//    └─ receiver: "this function belongs to *Injector, and inside it the injector is called i"
```

Then you call it like `faults.Maybe(ctx)`.

## 8. Interfaces: "anything that can do X"

An **interface** is a list of methods. Any type that has those methods automatically counts as that interface.
No `implements` keyword needed.

Three interfaces matter in this repo:

| Interface | Required method | Meaning |
|---|---|---|
| `error` | `Error() string` | "I'm an error, and I can describe myself" |
| `http.Handler` | `ServeHTTP(w, r)` | "I can answer an HTTP request" |
| `slog.Handler` | `Handle(ctx, record)` + a few more | "I can write a log record somewhere" |

Example: [httpx.go](../../../internal/httpx/httpx.go) defines `UpstreamError` with an `Error()` method,
so it *is* an `error` and can be returned anywhere an error is expected.

### Embedding (used in log.go)

```go
type traceHandler struct{ slog.Handler }
```

This struct has one unnamed field of type `slog.Handler`. Go "promotes" all of that field's methods,
so `traceHandler` automatically has every method a handler needs. We then **override** only `Handle`
to add the trace IDs. It's like saying "I'm exactly like that other handler, except for this one thing."
More on this in [05-logs-with-trace-ids.md](05-logs-with-trace-ids.md).

## 9. Functions as values (closures)

In Go you can create a function inline and hand it to something else:

```go
mux.HandleFunc("POST /checkout", func(w http.ResponseWriter, r *http.Request) {
    // this runs on every request to POST /checkout
    log.InfoContext(ctx, ...)   // can use `log`, `faults`, `client` from main() — it "closes over" them
})
```

The inline function can see variables from the function around it (`log`, `faults`, `client`, `paymentURL`).
That's called a **closure**. It's how each handler gets access to shared things without global variables.

## 10. `defer`: "do this when the function ends"

```go
ctx, span := tracer.Start(ctx, "stock.reserve")
defer span.End()   // runs when reserve() returns, no matter which `return` line it hits
```

`defer` schedules a call for later. It's perfect for cleanup: closing files, unlocking locks, ending spans.
Because spans measure "start until End()", `defer span.End()` makes the span cover the whole function.

## 11. Goroutines and channels (concurrency)

A **goroutine** is a lightweight thread: a piece of code running *at the same time* as other code.
Start one with `go`:

```go
go func() { errCh <- srv.ListenAndServe() }()
```

Go's HTTP server runs **every incoming request in its own goroutine**. 100 orders at once = 100 goroutines.
That's why shared data (like the fault config or the stock map) needs protection (see mutex below).

A **channel** is a pipe goroutines use to pass values: `errCh <- x` sends, `<-errCh` receives.

**`select`** waits on several channels and runs whichever is ready first:

```go
select {
case <-ctx.Done():          // someone pressed Ctrl-C / docker sent SIGTERM
case err := <-errCh:        // the server crashed on its own
}
```

## 12. Mutex: one at a time

If two goroutines change the same map at the same moment, the data can get corrupted (Go will even crash on purpose
to tell you). A **mutex** ("mutual exclusion") is a lock:

```go
s.mu.Lock()           // wait until nobody else holds the lock, then take it
defer s.mu.Unlock()   // give it back when done
have := s.units[req.ItemID]
s.units[req.ItemID] = have - req.Quantity
```

Analogy: a single-person bathroom with a key. Only the person holding the key can be inside.

`sync.RWMutex` (used in [fault.go](../../../internal/fault/fault.go)) is smarter: **many readers at once**
(`RLock`) *or* **one writer alone** (`Lock`). Faults are read on every request but changed rarely, so it fits perfectly.

## 13. `context.Context`: the backpack (most important one!)

This is the single most important Go idea for observability, so read slowly.

A `context.Context` (always named `ctx`) is a small **backpack** that gets handed from function to function,
and even from service to service, for the life of one request. It carries three things:

1. **A cancel signal.** "Stop, the caller gave up." (`ctx.Done()` closes.)
2. **A deadline.** "Stop at 14:05:03."
3. **Values.** Most importantly for us: **the current span** (which holds the trace ID).

```
HTTP request arrives
   └─ r.Context()  ← the backpack, already holding the server span (otelhttp put it there)
        └─ price(ctx, ...)            reads the span from the backpack, adds a child span
        └─ log.InfoContext(ctx, ...)  reads the trace_id out of the backpack
        └─ httpx.PostJSON(ctx, ...)   copies the trace_id into the outgoing HTTP header
```

Rules that follow from this:

- **Always pass `ctx` along.** If a function forgets it and uses `context.Background()` (an empty backpack),
  the trace breaks: logs get all-zero IDs and the next service starts a brand new trace.
- **Contexts are never modified, only extended.** `tracer.Start(ctx, ...)` returns a *new* ctx with the child
  span inside. That's why the code writes `ctx, span := tracer.Start(ctx, ...)`: it replaces the local `ctx`
  with the new, fuller backpack.

You'll see cancellation in action in lab 6: when checkout's 5-second timeout fires, the cancel signal travels
through payment all the way to inventory, which logs `"error":"context canceled"`.

## 14. Small stuff you'll bump into

| Code | Meaning |
|---|---|
| `any` | "a value of any type" (old name: `interface{}`) |
| `1<<20` | 1 shifted left 20 bits = 1,048,576 = 1 MB |
| `rand.Float64()` | random number between 0.0 and 1.0 |
| `rand.IntN(30)` | random whole number from 0 to 29 |
| `time.Duration(800) * time.Millisecond` | 800ms as a Duration value |
| `fmt.Sprintf("ord-%08x", n)` | format `n` as 8 hex digits, padded with zeros → `ord-9eee6ab0` |
| `switch { case A: ... case B: ... }` | a cleaner chain of if / else if |
| `os.Getenv("PORT")` | read an environment variable (empty string if missing) |

## Check yourself

1. Why does `stock.reserve` start with `ctx, span := tracer.Start(ctx, ...)` and not just `span := ...`?
2. What would go wrong if inventory's stock map had no mutex?
3. What does `%w` do that `%v` doesn't?
4. Why is `UpstreamError` usable as an `error`?
