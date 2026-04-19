/*
Package grpcapi implements the gRPC telemetry IngestService.

This server imports generated types from gen/telemetryv1.
Run `make generate` to produce them from proto/telemetry/v1/telemetry.proto.

WHY gRPC: gRPC uses Protocol Buffers (protobuf) for serialization, which is
~10x smaller and ~5x faster than JSON. For high-throughput internal services
that send millions of events, this efficiency matters. gRPC also provides
type-safe code generation, streaming, and HTTP/2 multiplexing out of the box.

Go learning note: In gRPC, you define your service in a .proto file, then
run `protoc` (the protobuf compiler) to generate Go types and interfaces.
You then implement the generated interface in your server code.
*/
package grpcapi

import (
	"context" // context.Context carries request-scoped metadata (deadlines, cancellation)
	"time"    // time.Now and time.Since for latency measurement

	"google.golang.org/grpc"        // grpc is Google's RPC framework; provides Server, interceptors, etc.
	"google.golang.org/grpc/codes"  // codes defines gRPC status codes (like HTTP status codes but for RPC)
	"google.golang.org/grpc/status" // status creates gRPC-specific error responses with codes

	telemetryv1 "github.com/cipheraxat/instrumentation-service/gen/telemetryv1" // Generated protobuf types for our telemetry service
	"github.com/cipheraxat/instrumentation-service/internal/batcher"            // shared event batcher
	"github.com/cipheraxat/instrumentation-service/internal/metrics"            // Prometheus metrics
	"github.com/cipheraxat/instrumentation-service/internal/model"              // domain types
)

// server implements the gRPC IngestServiceServer interface generated from protobuf.
//
// Go learning note: `telemetryv1.UnimplementedIngestServiceServer` is an EMBEDDED
// struct. Embedding in Go is similar to inheritance but it's composition-based.
// By embedding the "Unimplemented" type, our server automatically satisfies
// the full interface even if we only implement some methods. This is a forward-
// compatibility pattern — when new RPC methods are added to the proto file,
// existing servers won't break because the unimplemented methods return a
// "not implemented" error by default.
type server struct {
	telemetryv1.UnimplementedIngestServiceServer                  // Embedded for forward compatibility
	batcher                                      *batcher.Batcher // Where to send validated events
	metrics                                      *metrics.Metrics // Prometheus metrics
}

// Register wires the IngestService into the given gRPC server.
// This is called in main() to connect our implementation to the gRPC framework.
func Register(s *grpc.Server, bat *batcher.Batcher, m *metrics.Metrics) {
	telemetryv1.RegisterIngestServiceServer(s, &server{
		batcher: bat,
		metrics: m,
	})
}

// Ingest handles the IngestService.Ingest RPC call.
// It converts protobuf messages to domain models, validates them,
// and enqueues valid events into the batcher.
//
// Go learning note: Every gRPC handler receives a context.Context as its
// first parameter. This context carries deadlines, cancellation signals,
// and metadata (like authentication headers) from the client.
func (s *server) Ingest(ctx context.Context, req *telemetryv1.IngestRequest) (*telemetryv1.IngestResponse, error) {
	// Go learning note: GetEvents() is a generated method that safely returns
	// the events field (returns nil/empty if req is nil, avoiding nil panics).
	events := req.GetEvents()
	if len(events) == 0 {
		// Go learning note: status.Error creates a gRPC-specific error with
		// a status code. codes.InvalidArgument is the gRPC equivalent of
		// HTTP 400 Bad Request.
		return nil, status.Error(codes.InvalidArgument, "events array is empty")
	}

	// Convert each protobuf event to our domain model.
	// This separation keeps the domain model independent of protobuf.
	var accepted, rejected int32
	for _, e := range events {
		// Map from generated protobuf struct to our internal domain struct.
		evt := model.TelemetryEvent{
			EventID:    e.GetEventId(),
			EventType:  e.GetEventType(),
			Source:     e.GetSource(),
			Timestamp:  e.GetTimestamp(),
			Properties: e.GetProperties(),
			SessionID:  e.GetSessionId(),
		}
		if err := evt.Validate(); err != nil {
			s.metrics.ValidationErrors.Inc()
			rejected++
			continue
		}
		s.batcher.Add(evt)
		s.metrics.EventsIngested.Inc()
		accepted++
	}

	return &telemetryv1.IngestResponse{
		Accepted: accepted,
		Rejected: rejected,
	}, nil
}

// MetricsInterceptor records gRPC request duration.
//
// Go learning note: A gRPC "interceptor" is middleware for gRPC calls.
// grpc.UnaryServerInterceptor is a function type that wraps each RPC call.
// This is the gRPC equivalent of HTTP middleware. It receives the request,
// calls the actual handler, and can do work before/after the handler runs.
//
// The function signature is a Go pattern called "returning a closure" —
// MetricsInterceptor(m) returns a function that "closes over" the metrics m.
func MetricsInterceptor(m *metrics.Metrics) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		start := time.Now()
		// Call the actual RPC handler.
		resp, err := handler(ctx, req)
		// Record how long it took (runs even if handler returned an error).
		m.GRPCRequestDuration.Observe(time.Since(start).Seconds())
		return resp, err
	}
}
