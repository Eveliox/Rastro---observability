# Study guide: start here

This folder is a slow, thorough walk through **everything built so far** (Phase 1).
It assumes you know nothing about Go, Docker, HTTP, or observability.

The short phase write-up lives at [../01-services-traces-and-faults.md](../01-services-traces-and-faults.md).
This folder is the long version: more detail, more analogies, real output, and exercises.

## Reading order

| #  | Guide | What you'll understand after |
|----|-------|------------------------------|
| 01 | [The big picture](01-big-picture.md) | What miniobs is, what "observability" means, and where Phase 1 fits |
| 02 | [Go crash course](02-go-crash-course.md) | Enough Go to read every line in this repo |
| 03 | [HTTP and the three services](03-http-and-the-three-services.md) | What a "service" is, and what checkout / payment / inventory each do |
| 04 | [Traces, spans and propagation](04-traces-spans-propagation.md) | How one request gets one ID across three programs |
| 05 | [Logs that know their trace](05-logs-with-trace-ids.md) | Why every log line has `trace_id` and `span_id`, and how |
| 06 | [Fault injection and errors](06-fault-injection-and-errors.md) | How we break things on purpose and how errors travel |
| 07 | [Docker, Compose and Make](07-docker-compose-make.md) | How the code becomes running containers |
| 08 | [One order, start to finish](08-one-order-start-to-finish.md) | Ties it all together: follow one real request through every line |
| 09 | [Hands-on labs](09-labs.md) | Break things yourself and predict what happens |
| 10 | [Quiz and glossary](10-quiz-and-glossary.md) | Check yourself; look up any word |

If you only have 20 minutes, read **01**, then **08**, then do lab 1 and lab 2 in **09**.

## How the guides are written

- **"In plain words"** boxes give the one-sentence version before the details.
- Every code snippet comes from the real repo. Links go to the exact file.
- Every output example (log lines, HTTP responses, trace IDs) was produced by running the real
  services on 2026-11-05. Your IDs will be different every time; the *shape* will be the same.

## The files in the repo, in one table

```
miniobs/
├── services/
│   ├── checkout/main.go     the front door: takes an order, prices it, calls payment
│   ├── payment/main.go      the middle: authorizes the card, calls inventory, captures money
│   └── inventory/main.go    the bottom: reserves stock in a pretend database
├── internal/                code shared by all three services
│   ├── telemetry/
│   │   ├── telemetry.go     turns on tracing (OpenTelemetry)
│   │   └── log.go           a logger that stamps trace_id/span_id on every line
│   ├── fault/fault.go       the "break me on purpose" switch (/admin/fault)
│   └── httpx/httpx.go       JSON helpers, a traced HTTP client, a server that shuts down cleanly
├── Dockerfile               recipe to turn ANY one service into a small container image
├── docker-compose.yml       starts all three containers and wires them together
├── Makefile                 short commands: make up, make order, make break-inventory...
├── go.mod / go.sum          the list of libraries we use (and their checksums)
├── .dockerignore            files Docker should not copy when building
└── docs/learn/              these guides
```
