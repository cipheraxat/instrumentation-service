# Unified Instrumentation Service

Centralized Go-based telemetry ingestion service that consolidates event collection across multiple services via **gRPC** and **REST APIs**, eliminating the need for language-specific SDKs.

## Architecture

```
Producers ──▶ REST /v1/events ──▶   ┌──────────┐     ┌───────┐     ┌────────────┐
                                   │  Batcher  │────▶│ Kafka │────▶│ Consumers  │
Producers ──▶ gRPC Ingest    ──▶    └──────────┘     └───────┘     └────────────┘
                                        │
                                        ▼
                                   PostgreSQL
                                  (durable store)
```

**Design choices:**
- **In-memory batching with flush-on-threshold** over per-event writes — reduces Kafka producer overhead by ~70% while keeping p99 latency under 50ms.
- **Protocol Buffer schemas with backward-compatible evolution** — producers and consumers can be deployed independently.
- **At-least-once delivery guarantees** with configurable Kafka partitioning by source.

## Quick Start

```bash
# Start everything (generates proto, builds, starts infra)
docker compose up --build

# Send test events
curl -X POST http://localhost:8080/v1/events \
  -H "Content-Type: application/json" \
  -d '{"events": [{"event_id": "evt-1", "event_type": "click", "source": "web-app", "timestamp": 1700000000000}]}'
```

## Local Development

**Prerequisites:** Go 1.22+, `protoc`, `protoc-gen-go`, `protoc-gen-go-grpc`

```bash
make generate   # Generate protobuf Go code
make build      # Build binary to bin/server
make test       # Run all tests with race detection
make run        # Build and run locally
make docker     # docker compose up --build
```

## Project Structure

```
cmd/server/              Entry point
internal/
  api/rest/              Gin-based REST handler
  api/grpcapi/           gRPC IngestService server
  batcher/               In-memory batching (threshold + interval)
  kafka/                 Sarama sync producer
  storage/               PostgreSQL persistence
  config/                Environment-based configuration
  metrics/               Prometheus counters and histograms
  model/                 Domain types and validation
pkg/sdk/                 Go consumer SDK (retries + circuit breaker)
proto/telemetry/v1/      Protocol Buffer contract
gen/                     Generated protobuf Go code (make generate)
migrations/              PostgreSQL schema
grafana/                 Dashboard JSON + provisioning
prometheus/              Scrape configuration
```

## Observability

- **Prometheus metrics** on `:2112/metrics`
- **Grafana dashboard** auto-provisioned at `http://localhost:3000` (admin/admin)
- **Structured JSON logging** via `log/slog`

## Consumer SDK

Downstream teams use `pkg/sdk` to send events:

```go
import "github.com/cipheraxat/instrumentation-service/pkg/sdk"

client := sdk.NewClient("http://localhost:8080",
    sdk.WithMaxRetries(3),
    sdk.WithTimeout(5 * time.Second),
)

resp, err := client.Send(ctx, []sdk.Event{
    {EventID: "evt-1", EventType: "click", Source: "my-service"},
})
```

The SDK includes automatic retries with exponential backoff and a circuit breaker for downstream fault isolation.

## Testing

```bash
make test   # unit + integration tests, race detector enabled
```

Key test areas:
- **Batcher**: threshold flush, interval flush, drain-on-stop, concurrent adds
- **Circuit Breaker**: open/closed/half-open transitions, timeout reset
