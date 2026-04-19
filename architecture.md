# Instrumentation Service — Architecture Guide

## Overview

The **instrumentation-service** is a telemetry ingestion service that collects usage events from client applications via REST and gRPC APIs, batches them for throughput, and persists them to both Apache Kafka and PostgreSQL.

It also ships a **Go SDK** (`pkg/sdk/`) that client applications import to send events, complete with retry logic and a circuit breaker.

---

## Data Flow

```
                          ┌──────────────┐
  Client App ──HTTP──────▶│  REST API    │──┐
  (pkg/sdk)               │  (Gin)       │  │
                          └──────────────┘  │    ┌──────────┐     ┌───────────┐
                                            ├───▶│ Batcher  │────▶│  Kafka    │
                          ┌──────────────┐  │    │ (buffer  │     │ (Sarama   │
  gRPC Client ──gRPC─────▶│  gRPC API   │──┘    │  + flush)│     │  Producer)│
                          │  (protobuf)  │       └────┬─────┘     └───────────┘
                          └──────────────┘            │
                                                      ▼
                                                ┌───────────┐
                                                │ PostgreSQL │
                                                │ (ON CONFL- │
                                                │  ICT UPSRT)│
                                                └───────────┘

  Prometheus  ◀──scrape── /metrics (promhttp)
```

### Flow Summary
1. Events arrive via REST (`POST /v1/events`) or gRPC (`IngestEvent` / `IngestBatch`)
2. Both APIs push events into the **Batcher**
3. The Batcher buffers events and flushes when the buffer is full or a timer fires
4. On flush, events are sent to **Kafka** first; if Kafka fails, the batch still proceeds to **PostgreSQL** (fallback durable store) — no data is lost

---

## Package Breakdown

| Package | Path | Responsibility |
|---------|------|----------------|
| `main` | `cmd/server/main.go` | Wires all dependencies, starts servers, handles graceful shutdown |
| `config` | `internal/config/` | Loads configuration from environment variables (12-Factor App) |
| `model` | `internal/model/` | Domain types: `Event` struct, validation, sentinel errors |
| `metrics` | `internal/metrics/` | Prometheus metric definitions (counters, histograms) |
| `batcher` | `internal/batcher/` | Buffers events, flushes on size threshold or timer |
| `kafka` | `internal/kafka/` | Sarama SyncProducer wrapper for batch publishing |
| `storage` | `internal/storage/` | PostgreSQL persistence with `ON CONFLICT` upserts |
| `rest` | `internal/api/rest/` | Gin HTTP handler for `POST /v1/events` |
| `grpcapi` | `internal/api/grpcapi/` | gRPC server with interceptors for logging and metrics |
| `sdk/event` | `pkg/sdk/` | Public SDK event types (separate from internal model) |
| `sdk/client` | `pkg/sdk/` | HTTP client with functional options, retries, exponential backoff |
| `sdk/circuitbreaker` | `pkg/sdk/` | Circuit breaker (closed → open → half-open state machine) |

---

## Key Design Patterns

### 1. Batching (internal/batcher/)
Events are buffered in memory and flushed in bulk to reduce I/O pressure on Kafka and PostgreSQL. The batcher uses a **swap-and-release** technique: it swaps the full buffer with an empty one under a mutex lock, then releases the lock before performing I/O.

**Kafka-fallback-to-Postgres**: If the Kafka write fails during a flush, the batcher does **not** discard the batch. Instead it falls through to the PostgreSQL write, ensuring events are durably persisted even when Kafka is temporarily unavailable. This prevents data loss during broker outages.

**Race-safe metrics**: The buffer-size gauge is captured _before_ releasing the mutex, so concurrent `Add()` calls cannot skew the metric between `Unlock()` and the Prometheus update.

### 2. Circuit Breaker (pkg/sdk/)
The SDK includes a circuit breaker that prevents client applications from hammering the service when it's unhealthy:
- **Closed**: Requests flow normally
- **Open**: Requests are rejected immediately (fast-fail)
- **Half-Open**: One test request is allowed through to check recovery

### 3. Functional Options (pkg/sdk/)
The SDK client uses the functional options pattern (`WithTimeout`, `WithRetries`, `WithCircuitBreaker`) for flexible, self-documenting configuration.

### 4. Error Propagation in Storage
`json.Marshal` errors during PostgreSQL batch inserts are caught and returned immediately (with a transaction rollback), preventing corrupt JSON from reaching the database.

### 5. Dependency Injection
All components receive their dependencies via constructor parameters (not globals). This makes every component testable in isolation with mock implementations.

### 6. Graceful Shutdown
`main.go` listens for OS signals (SIGINT, SIGTERM), then shuts down components in reverse dependency order with a timeout context.

---

## Go Patterns You'll Encounter

| Pattern | Where | What It Teaches |
|---------|-------|-----------------|
| Goroutines + WaitGroup | batcher, main | Concurrent work with synchronisation |
| Channels + select | batcher (flush timer) | Communication between goroutines |
| Context cancellation | main, SDK client | Propagating shutdown signals |
| Interfaces | batcher (Flusher) | Decoupling, testability |
| Mutex + swap-and-release | batcher | Safe concurrent buffer access |
| Struct embedding | gRPC server | Satisfying interfaces with defaults |
| Sentinel errors | model | `errors.Is()` checks |
| Error wrapping (%w) | config, storage | Error chain preservation |
| Blank import (`_ "lib/pq"`) | storage | Driver registration via `init()` |
| Table-driven tests | batcher_test, circuitbreaker_test | Standard Go testing pattern |
| Functional options | SDK client | Flexible API design |

---

## Docker Compose Topology

The service depends on:
- **Kafka** (+ Zookeeper): Message broker for downstream consumers
- **PostgreSQL**: Persistent storage for events (default `sslmode=require`)
- **Prometheus**: Metrics scraping from `/metrics`
- **Grafana**: Dashboards (admin password injected via `GRAFANA_ADMIN_PASSWORD` env var)

---

## Suggested Reading Order

1. **`internal/model/event.go`** — Start here. Understand the core data type.
2. **`internal/config/config.go`** — See how configuration is loaded.
3. **`internal/batcher/batcher.go`** — The heart of the service: buffering and flushing.
4. **`internal/kafka/producer.go`** — How events reach Kafka.
5. **`internal/storage/postgres.go`** — How events reach PostgreSQL.
6. **`internal/api/rest/handler.go`** — The REST API entry point.
7. **`internal/api/grpcapi/server.go`** — The gRPC API entry point.
8. **`cmd/server/main.go`** — How everything is wired together.
9. **`pkg/sdk/client.go`** — The client SDK with retries and backoff.
10. **`pkg/sdk/circuitbreaker.go`** — The circuit breaker state machine.
11. **`internal/metrics/metrics.go`** — Prometheus instrumentation.
12. **Test files** — See how mocks, table-driven tests, and test doubles work.
