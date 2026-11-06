# miniobs — a mini observability platform

A small Datadog-style platform built as a portfolio and learning project.
Stack: Go backend, React frontend, OpenTelemetry, Kafka (Redpanda locally), ClickHouse, Postgres, Docker Compose.

## Working agreement

- **This is a learning project.** Explain every step in plain language as you build: what the piece is, why it exists, and how it fits. Assume no prior knowledge of observability, Kafka or ClickHouse.
- Each phase gets a short write-up in `docs/learn/NN-<topic>.md` covering the concepts it introduced.
- **The user does all git commits.** Never commit or push. When a phase is done, suggest a commit message.
- Work one phase at a time. Stop at the end of each phase so the user can review and commit.

## Architecture

```
 ┌──────────┐    ┌─────────┐    ┌───────────┐
 │ checkout │ -> │ payment │ -> │ inventory │     3 demo Go services, OTel SDK
 └────┬─────┘    └────┬────┘    └─────┬─────┘     (traces, logs, metrics)
      └───────────────┼───────────────┘
                      │  OTLP
                      v
               ┌─────────────┐
               │ ingest (Go) │   our own OTLP receiver (not the OTel Collector)
               └──────┬──────┘
                      v
               ┌─────────────┐
               │   Kafka     │   Redpanda locally; buffers bursts, decouples ingest from storage
               └──────┬──────┘
                      v
               ┌─────────────┐
               │ consumer    │   batch consumer (Go)
               └──────┬──────┘
                      v
               ┌─────────────┐        ┌──────────┐
               │ ClickHouse  │        │ Postgres │  users, dashboards, alert rules, SLOs
               └──────┬──────┘        └────┬─────┘
                      └───── API (Go) ─────┘
                               │
                         React frontend
```

- **Telemetry data** (spans, logs, metrics) goes to ClickHouse only.
- **Config and state** (users, dashboards, alert rules, SLOs) goes to Postgres only.

## Rules

1. **Every log carries `trace_id` and `span_id`.** In Go, always log with the context-aware methods (`log.InfoContext(ctx, ...)`, never `log.Info(...)`). The shared slog handler in `internal/telemetry` adds the IDs from `ctx`. An all-zero trace_id in output means someone dropped the context: that's a bug.
2. **ClickHouse inserts are always batched.** Flush when the batch reaches N rows *or* 1–2 seconds have passed, whichever comes first. Never insert row by row.
3. **Retention uses ClickHouse TTL** on the tables. No cron jobs that delete rows.
4. Propagate context across service calls with W3C `traceparent` headers (use `otelhttp` for servers and clients).
5. Config comes from environment variables, with defaults that work in docker compose.

## Layout

```
services/<name>/     demo services: checkout, payment, inventory
internal/telemetry/  OTel setup + trace-aware slog logger (shared)
internal/fault/      failure injection (shared by every demo service)
internal/httpx/      JSON helpers, traced HTTP client, graceful server
docs/learn/          one explainer per phase
```

One Go module at the repo root (`github.com/Eveliox/miniobs`). One `Dockerfile` builds any service via `--build-arg SERVICE=<name>`.

## Build phases

1. **Demo services + failure injection endpoint + docker-compose** ✅
2. Ingest -> Kafka -> ClickHouse for traces
3. React trace waterfall + root-cause finder (the deepest span that has its own error, not one inherited from a child)
4. Logs with trace correlation + search
5. Metrics + error grouping
6. Dashboards + SLO burn-rate alerts
7. Retention, load generator, benchmarks

## Commands

```
make up                 # build and start everything
make order              # place one order through checkout
make break-inventory    # inventory fails 100% of requests
make slow-inventory     # inventory adds 800ms latency
make heal-inventory     # clear the fault
make logs               # follow logs
make down               # stop everything
```

Go isn't installed on the host. Everything builds inside Docker. To run Go tooling:
`docker run --rm -v "$PWD":/src -w /src golang:1.26-alpine go <cmd>`
