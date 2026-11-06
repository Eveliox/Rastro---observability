# 03: HTTP and the three services

> **In plain words:** each service is a small program that waits for messages ("requests") on a numbered door
> ("port"), does some work, and sends back an answer ("response"). The messages use HTTP, the same thing your
> browser uses to load websites. Checkout calls payment, payment calls inventory.

---

## Part A: HTTP from zero

### Request and response

HTTP is a conversation with exactly two turns: the client asks, the server answers.

**A request** has:

| Part | Our example | What it is |
|---|---|---|
| Method | `POST` | The verb. `GET` = read, `POST` = do/create, `PUT` = set/replace, `DELETE` = remove |
| Path | `/checkout` | Which thing on the server |
| Headers | `Content-Type: application/json`, `traceparent: 00-…` | Labels on the envelope |
| Body | `{"item_id":"sku-keyboard","quantity":1}` | The letter inside |

**A response** has:

| Part | Our example |
|---|---|
| Status code | `200 OK` |
| Headers | `X-Trace-Id: 652ac6d062c3dda021d1a6a00adae57a` |
| Body | `{"order_id":"ord-9eee6ab0","status":"confirmed",...}` |

Here's a real one, from `make order`:

```
HTTP/1.1 200 OK
Content-Type: application/json
X-Trace-Id: 652ac6d062c3dda021d1a6a00adae57a
Content-Length: 147

{"order_id":"ord-9eee6ab0","status":"confirmed","amount_cents":4999,"payment_id":"pay-ord-9eee6ab0","trace_id":"652ac6d062c3dda021d1a6a00adae57a"}
```

**Headers are the important part for tracing.** The trace ID crosses from service to service in a header
called `traceparent`. More in [04](04-traces-spans-propagation.md).

### Status codes: the server's mood in three digits

The first digit is the family:

| Family | Meaning | Codes we use |
|---|---|---|
| **2xx** | Success | `200 OK` |
| **4xx** | **You** (the caller) did something wrong, or the request can't be done | `400 Bad Request` (bad JSON), `405 Method Not Allowed`, `409 Conflict` (out of stock) |
| **5xx** | **I** (the server) have a problem | `500 Internal Server Error` (I broke), `502 Bad Gateway` (someone *I depend on* broke) |

The difference between **500** and **502** is the key idea for finding root causes:

- `500` = "the problem is *inside me*".
- `502` = "I'm fine, but the service *behind me* gave me a bad answer".

So in a chain of failures, follow the 502s down until you hit a 500. That's where it broke.

### Ports

One computer can run many servers, so each one listens on a numbered **port**:

| Service | Port | From your Mac |
|---|---|---|
| checkout | 8081 | `localhost:8081` |
| payment | 8082 | `localhost:8082` |
| inventory | 8083 | `localhost:8083` |

Analogy: the computer is an apartment building; the port is the apartment number.

### JSON

JSON is the text format for the bodies: `{"key": value}`. Go turns JSON into structs and back using the tags
explained in [02](02-go-crash-course.md#4-structs-and-struct-tags).

---

## Part B: The shared HTTP helpers ([internal/httpx/httpx.go](../../../internal/httpx/httpx.go))

All three services need the same plumbing, so it lives in one shared package.

### `ReadJSON`: read the request body, strictly

```go
dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
dec.DisallowUnknownFields()
return dec.Decode(v)
```

- `MaxBytesReader(…, 1<<20)`: refuse bodies bigger than 1 MB. Protects the server from someone sending a huge body.
- `DisallowUnknownFields()`: if the JSON has a field our struct doesn't have, fail. So a typo like
  `{"item":"x"}` (instead of `item_id`) gets a clear `400` instead of being silently ignored. We tested it:

  ```
  HTTP/1.1 400 Bad Request
  {"error":"send {\"item_id\": \"sku-keyboard\", \"quantity\": 1}"}
  ```

### `WriteJSON` / `WriteError`: send a response

Sets `Content-Type: application/json`, writes the status code, then the body. Errors always come back as
`{"error": "..."}` so every service's errors look the same.

### `NewClient`: an HTTP client that spreads the trace

```go
return &http.Client{
    Transport: otelhttp.NewTransport(http.DefaultTransport),
    Timeout:   5 * time.Second,
}
```

- The **Transport** is the part of the client that actually sends bytes. We wrap it in `otelhttp.NewTransport`,
  which, for every outgoing request, (1) starts a **client span** and (2) writes the `traceparent` header.
  We never write that header by hand; the wrapper does it.
- **Timeout 5s**: if the other service doesn't answer within 5 seconds, give up. Without a timeout, a stuck
  service would make its callers wait forever too. (Lab 6 shows this firing.)

### `PostJSON`: call another service

```go
req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
```

`NewRequestWithContext(ctx, …)` attaches the backpack (ctx) to the outgoing request. The otelhttp transport
looks inside it to find the current span and puts its IDs into `traceparent`.
**This is the line that makes cross-service tracing work.** The comment above the function says it plainly:
*ctx MUST be the incoming request's context, or the trace breaks in two.*

If the other service answers with status ≥ 300, `PostJSON` returns an `*UpstreamError{Status, Message}`, so the
caller can see *which* status came back (that's how payment knows inventory said `409` vs `500`).

### `Serve`: run the server and shut down politely

```go
handler := otelhttp.NewHandler(mux, name, otelhttp.WithSpanNameFormatter(...))
```

This wraps **all** of the service's routes. For every incoming request it:

1. Reads `traceparent` from the headers (if present) to join the caller's trace.
2. Starts a **server span** named like `POST /reserve`.
3. Puts that span into `r.Context()`, so your handler code receives a backpack that already has it.
4. When your handler finishes, ends the span and records the status code.

Then **graceful shutdown**: `signal.NotifyContext` waits for Ctrl-C or `SIGTERM` (the signal `docker compose down`
sends). When it arrives:

- `srv.Shutdown`: stop accepting new requests, but let in-flight ones finish (up to 5 seconds).
- `shutdownTelemetry`: flush any spans still waiting to be sent (matters from Phase 2 on).

Without this, stopping a container mid-order would cut the request off and lose its last spans.

`lifecycleLog` logs "service started" / "shutting down" **inside a tiny span**, so even those lines obey
the rule "every log line has a trace_id". Each one gets its own new trace ID because it isn't part of any request.

---

## Part C: The three services

All three `main()` functions follow the same recipe:

```go
shutdown, err := telemetry.Setup(ctx, serviceName)  // 1. turn on tracing
log := telemetry.NewLogger(serviceName)             // 2. make the trace-aware logger
faults := fault.New(log)                            // 3. make the break-me switch
mux := http.NewServeMux()                           // 4. make a router
mux.Handle("/admin/fault", faults.Handler())        // 5. route: fault control
mux.HandleFunc("POST /<thing>", ...)                // 6. route: the real work
httpx.Serve(serviceName, ":"+port, mux, log, shutdown) // 7. run until stopped
```

A **mux** (multiplexer) is a router: it looks at method + path and picks which function runs.
`"POST /checkout"` means "only POST requests to /checkout"; a GET there gets `405 Method Not Allowed`.

### checkout ([services/checkout/main.go](../../../services/checkout/main.go)): the front door

What it does on `POST /checkout`:

1. Reads the trace ID from ctx and sends it back in the `X-Trace-Id` header. That's a convenience for you:
   it lets you copy the ID and search the logs.
2. Reads and validates the body (`quantity` must be > 0).
3. Makes an order ID like `ord-9eee6ab0`.
4. Logs `"checkout started"`.
5. **Prices it** in its own span `pricing.lookup` (the fault can fire here). Unknown item → `400`.
6. **Calls payment** `POST /charge` with order ID, item, quantity and amount.
7. Decides the response:
   - payment said `409` → out of stock → checkout returns `409`.
   - payment failed any other way → checkout returns `502` (the thing behind me broke).
   - success → `200` with the order details and `trace_id` in the body too.

### payment ([services/payment/main.go](../../../services/payment/main.go)): the middle

On `POST /charge`, three steps, in this exact order:

1. **`card.authorize`** (its own span, 10–40ms random sleep to imitate a card network). The fault can fire here.
   If it does, payment returns `500` and **inventory is never called**.
2. **Reserve stock**: `POST /reserve` to inventory, passing `ctx`.
   - inventory `409` → payment passes `409` back.
   - inventory failed another way → payment returns `502`.
3. **`card.capture`** (its own span). Called with `faults = nil`, so the fault never fires here.

Why authorize → reserve → capture? It's how real card payments work: first put a *hold* on the money
(authorize), then make sure you can actually ship (reserve), and only then take the money (capture).

### inventory ([services/inventory/main.go](../../../services/inventory/main.go)): the bottom

Holds a pretend database in memory: 1000 keyboards, 1000 mice, 50 monitors. (Restarting the container resets it.)

On `POST /reserve`:

1. Validates: `item_id` must be set, `quantity` > 0.
2. Calls `reserve()` which runs in its own span `stock.reserve`:
   - sleeps 5ms (pretend database trip),
   - **the fault fires here**,
   - locks the mutex, checks stock, subtracts.
3. Out of stock → `409` (and the span is **not** marked as an error; see [06](06-fault-injection-and-errors.md)).
4. Injected fault → `500`.
5. Success → `200` with `reservation_id` and how many are `remaining`.

### Every service also has `/admin/fault`

Covered fully in [06](06-fault-injection-and-errors.md). `GET` shows the fault, `PUT` sets it, `DELETE` clears it.

### Config from environment variables

```go
paymentURL := envOr("PAYMENT_URL", "http://localhost:8082")
```

`envOr` reads an environment variable, or uses a default if it's empty. Docker Compose sets
`PAYMENT_URL=http://payment:8082` (see [07](07-docker-compose-make.md)). Running outside Docker, the default
`localhost:8082` would work instead. This follows the project rule: *config from env vars, with defaults that work*.

## Check yourself

1. Inventory is broken. What status does payment return, and why not 500?
2. Why does `ReadJSON` reject unknown fields?
3. Payment's fault is on. Does inventory's stock go down? Why?
4. What would happen if `NewClient` had no `Timeout`, and inventory hung forever?
