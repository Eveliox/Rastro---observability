# 10: Quiz answers and glossary

## Answers to the "Check yourself" questions

<details><summary><b>01: The big picture</b></summary>

1. It's a tracking number shared by everything one request touches, so you can find all of it later.
2. Traces (they show where the time went, per step).
3. Patient: checkout, payment, inventory (they produce telemetry). Doctor: ingest, Kafka, consumer, ClickHouse, API, frontend.
4. Nothing: there's no exporter yet, so when a span ends it's dropped. Only its IDs survive inside log lines.
</details>

<details><summary><b>02: Go crash course</b></summary>

1. `tracer.Start` returns a *new* context containing the child span. Replacing `ctx` makes anything called afterward
   (like `faults.Maybe(ctx)`) nest under `stock.reserve`.
2. Two requests could read/write the map at the same moment: lost updates (two orders both see 50 monitors and both
   subtract), or Go crashes with "concurrent map writes".
3. `%w` wraps the original error so `errors.Is` / `errors.As` can still find it. `%v` only copies its text.
4. It has an `Error() string` method, and in Go anything with that method satisfies the `error` interface automatically.
</details>

<details><summary><b>03: HTTP and the three services</b></summary>

1. `502 Bad Gateway`: payment itself is fine; the service behind it failed. 500 would wrongly point at payment.
2. So typos fail loudly with a 400 instead of being silently ignored (e.g. `item` instead of `item_id` would
   otherwise just look like an empty item ID).
3. No. Payment's fault fires in `card.authorize`, before it ever calls inventory.
4. Payment would wait forever, checkout would wait forever on payment, and each stuck request would hold resources
   until the services ran out. Timeouts put a ceiling on how far one failure can spread.
</details>

<details><summary><b>04: Traces, spans and propagation</b></summary>

1. Outside the server's own work: the network, a connection queue, DNS, or the server being too busy to start the request.
2. Contexts can't be changed, only extended. The new ctx holds the child span so later calls nest under it.
3. The two otelhttp *client* spans (`HTTP POST`), because by convention a 4xx means "my call failed". It's not a real
   problem because no span has an exception event, and server spans stay unset. A root-cause finder must not rely on status alone.
4. `OTEL_EXPORTER_OTLP_ENDPOINT`.
5. Flags = `01` = "sampled": please record this trace. The receiver's default sampler (`ParentBased`) follows that decision.
</details>

<details><summary><b>05: Logs that know their trace</b></summary>

1. `00000000000000000000000000000000` (and span_id `0000000000000000`).
2. `.With("service", …)` would return the plain inner JSON handler; the wrapper disappears and no line gets trace IDs.
3. It's an expected business outcome, not a system failure. Logging it as ERROR would make error dashboards and alerts lie.
4. To stdout inside each container. Docker collects them; view with `make logs` or `docker compose logs`.
</details>

<details><summary><b>06: Fault injection</b></summary>

1. No. `PUT` replaces the whole config, so it's now `{"error_rate":0,"latency_ms":800}`: slow but not failing.
2. So it can stop early if the request is cancelled (caller timed out or hung up). `time.Sleep` can't be interrupted.
3. `card.authorize`. No, `card.capture` isn't there: payment returns right after authorize fails.
4. Checkout's own code worked; what failed was a service behind it. 502 means "bad answer from upstream".
</details>

<details><summary><b>07: Docker, Compose and Make</b></summary>

1. Layer caching: dependencies change rarely, code changes often. This order lets Docker reuse the downloaded libraries.
2. An image is the packaged, read-only template. A container is a running instance of it.
3. Compose's private network has DNS where each service name resolves to that container.
4. No. Only the start order. It doesn't wait for "ready". (Health checks, coming in Phase 2, do that.)
5. Distroless has nothing but what the binary needs. If someone broke into the program, there'd be no shell or tools to use.
</details>

---

## Glossary

**Attribute**: a key/value label on a span, e.g. `item.id = sku-keyboard`. Used for searching and grouping.

**Baggage**: a W3C header for passing your own key/values along a call chain. Enabled, not used yet.

**Batcher / batch span processor**: collects finished spans in memory and sends them in groups in the background,
so requests never wait on the network.

**Build arg (`ARG`)**: a variable passed into `docker build`. We use `SERVICE` to pick which service to compile.

**Chaos engineering**: breaking systems on purpose to learn how they fail and to practice responding.

**Client span**: a span for an *outgoing* call. Created by `otelhttp.NewTransport`. Named `HTTP POST`.

**Closure**: an inline function that uses variables from the function around it.

**Container**: a running instance of an image, isolated from other processes but sharing the host kernel.

**Context (`ctx`)**: Go's per-request "backpack": cancel signal, deadline, and values (including the current span).

**Data race**: two goroutines touching the same data at once with at least one writing. Prevented with a mutex.

**Decorator pattern**: wrapping something with the same interface to add one behavior. Our `traceHandler`.

**`depends_on`**: Compose start order. Not a readiness guarantee.

**Distroless**: a minimal container base image with no shell or package manager.

**DNS**: the system that turns names (`payment`) into network addresses.

**Event (span event)**: a timestamped note inside a span. `RecordError` adds an `exception` event.

**Exception event**: the event `span.RecordError(err)` adds. In our system, it marks where an error *originated*.

**Exporter**: the SDK piece that sends spans out (OTLP over HTTP for us). None configured in Phase 1.

**Extract / inject**: reading trace context out of incoming headers / writing it into outgoing headers.

**Fault injection**: deliberately adding errors or delay. Our `/admin/fault`.

**Goroutine**: a lightweight concurrent task in Go. Every HTTP request gets one.

**Graceful shutdown**: on stop, finish in-flight requests and flush telemetry before exiting.

**Handler (HTTP)**: code that answers a request (`http.Handler`).

**Handler (slog)**: code that formats and writes a log record (`slog.Handler`).

**Hex (hexadecimal)**: base-16 digits 0–9 a–f. Trace IDs are 32 hex chars, span IDs 16.

**Image**: a packaged, read-only filesystem + start command. Built from a Dockerfile.

**Interface**: a set of methods; any type with those methods satisfies it automatically.

**Internal span**: a span for local work, created by us with `tracer.Start`.

**Latency**: how long something takes.

**Layer caching**: Docker reusing a step's previous result when its inputs haven't changed.

**Log level**: DEBUG / INFO / WARN / ERROR: how important a log line is.

**Multi-stage build**: a Dockerfile that builds in one big image and copies only the result into a small one.

**Mutex / RWMutex**: a lock so only one goroutine (or many readers, one writer) touches data at a time.

**Mux (ServeMux)**: an HTTP router; matches method + path to a handler.

**Observability**: understanding a system's internal state from its outputs (traces, logs, metrics).

**OpenTelemetry (OTel)**: the vendor-neutral standard and libraries for producing telemetry.

**OTLP**: OpenTelemetry Protocol, the wire format for sending telemetry. Phase 2 builds a receiver.

**otelhttp**: OTel's wrapper for Go's `net/http` that creates server/client spans and handles propagation.

**Parent span**: the span another span happened inside. Linked by parent span_id.

**Port**: a numbered "door" on a machine that a server listens on. `host:container` in Compose.

**Propagation / propagator**: carrying trace context across process boundaries, via the `traceparent` header.

**Resource**: who produced telemetry, e.g. `service.name = inventory`.

**Root cause**: in our system, the deepest span that has its *own* exception event (not an inherited error).

**Root span**: the first span of a trace, with no parent. Ours is `POST /checkout`.

**Sampler / sampling**: deciding which traces to keep. Ours keeps all (`ParentBased(AlwaysSample)`).

**Server span**: a span for an *incoming* request. Created by `otelhttp.NewHandler`. Named like `POST /reserve`.

**Service**: a separately running program with its own job, talking to others over the network.

**slog**: Go's standard structured logging package (`log/slog`).

**Span**: one timed unit of work with name, IDs, status, attributes, events.

**Span status**: Unset / OK / Error. otelhttp sets Error for server 5xx and client 4xx/5xx.

**Status code**: HTTP's three-digit result. 2xx ok, 4xx caller's problem, 5xx server's problem; 502 = upstream broke.

**stdout**: a program's standard output stream. Docker captures it as the container's logs.

**Structured logging**: logs as named fields (JSON) instead of free text.

**Struct tag**: the backtick text after a field (`json:"item_id"`) telling libraries how to map it.

**Telemetry**: data a system emits about itself: traces, logs, metrics.

**Timeout**: a limit on how long to wait. Our HTTP client gives up after 5s.

**Trace**: all spans for one request, sharing a trace_id, forming a tree.

**traceparent**: the W3C header `00-<trace_id>-<parent span_id>-<flags>`.

**Tracer / TracerProvider**: a span factory / the object that creates tracers and holds the config (exporter, sampler, resource).

**Upstream / downstream**: here, "upstream" means a service you call (inventory is upstream of payment).
Usage varies between teams; in our code `UpstreamError` = "the service I called failed".

**Waterfall**: a timeline chart of a trace's spans as nested bars. Phase 3.

**Wrapping errors (`%w`)**: nesting one error inside another so the chain is preserved and inspectable.
