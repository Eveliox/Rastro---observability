# 01: The big picture

> **In plain words:** we're building a tiny version of Datadog. Datadog is a tool companies pay for
> so that when their software breaks, they can see *where* and *why* fast.
> Phase 1 built the "patient" (3 small programs that talk to each other) and a way to make the patient sick on command.

## The problem, as a story

Imagine an online store. When you click **Buy**, your click doesn't go to one program. It goes to several:

1. **checkout** takes your order and works out the price.
2. checkout asks **payment** to charge your card.
3. payment asks **inventory** to hold one keyboard for you.

Each of these is a separate program, often on a separate computer, often written by a separate team.
These separate programs are called **services** (or "microservices").

Now one day customers start complaining: "I click Buy and get an error."

- The checkout team looks at *their* logs: "payment returned an error. Not our fault."
- The payment team looks at *their* logs: "inventory returned an error. Not our fault."
- The inventory team has 50,000 log lines from the last hour. Which ones belong to the failed orders?

Without help, this is detective work across three log files, matching timestamps by hand.
With thousands of orders per minute all mixed together, it's practically impossible.

## The fix: give every request a tracking number

Think about shipping a package. The moment you ship it, it gets a **tracking number**.
Every warehouse, truck and sorting center scans that number and records what happened.
Later, type the number into a website and you see the whole journey:
"Left Miami 9:02 → arrived Atlanta 14:30 → **damaged in Atlanta sorting** → returned".

That's exactly what we did in Phase 1:

- Every incoming order gets a **trace ID** (the tracking number), e.g. `652ac6d062c3dda021d1a6a00adae57a`.
- Each service passes the tracking number along when it calls the next service.
- Every service writes the tracking number into every log line it writes.

Now "what happened to this order?" is one search: *show me every line with trace ID 652ac6…*.

## What "observability" means

**Observability** = being able to understand what's happening inside your software just by looking at
what it outputs, *without* having to add new code or attach a debugger after something breaks.

The industry splits that output into three kinds of data, called the **three pillars**:

| Pillar | Analogy | Answers | Example from our system |
|---|---|---|---|
| **Traces** | The package's tracking history | *Where* did the time go? *Which* step failed? | checkout 56ms → payment 52ms → inventory 6ms ❌ |
| **Logs** | The notes each warehouse worker writes | *What exactly* happened at that moment? | `"reserve failed", error="injected fault"` |
| **Metrics** | The dashboard on the warehouse wall | *How many / how often*, over time? | 40% of checkouts failed in the last 5 min |

Phase 1 sets up **traces** and **logs**, and makes them **connected** (logs carry the trace ID).
Metrics arrive in Phase 5.

## What miniobs is, piece by piece

The whole project when finished (from [CLAUDE.md](../../../CLAUDE.md)):

```
 ┌──────────┐    ┌─────────┐    ┌───────────┐
 │ checkout │ -> │ payment │ -> │ inventory │     ← PHASE 1 (done): the "patient"
 └────┬─────┘    └────┬────┘    └─────┬─────┘
      └───────────────┼───────────────┘
                      │  OTLP  (the telemetry data being sent)
                      v
               ┌─────────────┐
               │ ingest (Go) │   ← Phase 2: receives the data
               └──────┬──────┘
                      v
               ┌─────────────┐
               │   Kafka     │   ← Phase 2: a waiting room / buffer
               └──────┬──────┘
                      v
               ┌─────────────┐
               │ consumer    │   ← Phase 2: takes data out of the buffer in batches
               └──────┬──────┘
                      v
               ┌─────────────┐        ┌──────────┐
               │ ClickHouse  │        │ Postgres │   ← storage
               └──────┬──────┘        └────┬─────┘
                      └───── API (Go) ─────┘
                               │
                         React frontend           ← Phase 3+: the website you look at
```

A useful way to think about it: **two halves**.

- **The patient** (Phase 1): three demo services pretending to be a real store. They *produce* telemetry.
  In a real company, this would be the company's actual product.
- **The doctor** (Phases 2–7): the observability platform. It *collects, stores and displays* that telemetry.
  This is the part that's "our Datadog".

We built the patient first because a doctor with no patient has nothing to look at.

## What Phase 1 actually delivered

1. **Three Go programs** (checkout, payment, inventory) that call each other over HTTP.
2. **Tracing** with OpenTelemetry: every request gets a trace ID that flows across all three.
3. **Trace-aware logs**: every log line is JSON and includes `trace_id` and `span_id`.
4. **Fault injection**: an `/admin/fault` endpoint on every service to make it fail or slow down, on demand.
5. **Docker + Compose + Make**: one command (`make up`) builds and runs everything.

What Phase 1 **didn't** do yet: send the traces anywhere. Right now spans are created, get real IDs,
those IDs travel between services and show up in logs, and then the span itself is thrown away.
Phase 2 builds the pipeline that catches them.

## Why build our own instead of using the real tools?

There are ready-made tools for every box in that diagram (the OpenTelemetry Collector, Jaeger, Grafana…).
We build our own versions because:

- This is a learning project. Building the receiver teaches you what the data actually looks like.
- It's a portfolio piece. "I built an OTLP ingest pipeline into ClickHouse through Kafka" says much more than "I installed Jaeger".

But we use the **standards** (OpenTelemetry, OTLP, W3C traceparent), so the demo services would work
unchanged with a real vendor too. That matters: it means we're learning the real thing, not a toy.

## Check yourself

1. In one sentence, what is a trace ID for?
2. Which of the three pillars answers "which service was slow?"
3. In our diagram, which part is the "patient" and which part is the "doctor"?
4. What does Phase 1 do with spans after creating them?

Answers are in [10-quiz-and-glossary.md](10-quiz-and-glossary.md).
