/*
Package sdk provides a lightweight Go client for the instrumentation service.

This file defines the data types used by the SDK client. These types mirror
the server-side model.TelemetryEvent but are deliberately defined separately
in the SDK package so that:
 1. SDK users don't need to import internal server packages.
 2. The SDK and server can evolve independently.
 3. The SDK is a self-contained, publishable Go module.

Go learning note: Packages under `pkg/` are intended to be importable by
external consumers (unlike `internal/` which is restricted). This convention
isn't enforced by the Go compiler (only `internal/` is enforced), but it's
a widely followed community convention.
*/
package sdk

// Event represents a telemetry event to send via the SDK.
// This is the public-facing type that SDK consumers interact with.
type Event struct {
	EventID    string            `json:"event_id"`             // Unique ID for this event (caller should generate, e.g., UUID)
	EventType  string            `json:"event_type"`           // The type of event, e.g., "page_view", "api_call"
	Source     string            `json:"source"`               // Name of the emitting service
	Timestamp  int64             `json:"timestamp"`            // Unix millisecond timestamp
	Properties map[string]string `json:"properties,omitempty"` // Arbitrary key-value metadata
	SessionID  string            `json:"session_id,omitempty"` // Optional session correlation ID
}

// ingestRequest is the JSON payload sent to the REST API.
// It's unexported (lowercase) because SDK users don't need to see it —
// it's an implementation detail of how the SDK talks to the server.
type ingestRequest struct {
	Events []Event `json:"events"` // Batch of events to ingest
}

// IngestResponse is returned by the instrumentation service.
// It tells the caller how many events were accepted vs rejected.
type IngestResponse struct {
	Accepted int `json:"accepted"` // Events that passed server-side validation
	Rejected int `json:"rejected"` // Events that failed server-side validation
}
