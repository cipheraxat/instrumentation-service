/*
Package model defines the domain types shared across the instrumentation service.

This package is intentionally free of infrastructure concerns (no database imports,
no HTTP imports). It only defines data structures and validation logic. This
follows the "clean architecture" principle: domain models live at the center and
have no outward dependencies.

Go learning note: Go doesn't have classes. Structs + methods are the closest
equivalent. Methods are defined outside the struct body using "receiver" syntax.
*/
package model

import (
	"errors" // errors.New creates simple sentinel error values
	"time"   // time.Now provides the current timestamp
)

// Go learning note: These are "sentinel errors" — package-level error values
// that callers can compare against using errors.Is(). By convention, sentinel
// errors in Go are named Err<Description> and defined as package-level vars.
// This pattern lets consumers of the API distinguish between different error
// cases without parsing error message strings.
var (
	ErrMissingEventID   = errors.New("event_id is required")
	ErrMissingEventType = errors.New("event_type is required")
	ErrMissingSource    = errors.New("source is required")
)

// TelemetryEvent represents a single telemetry data point from a client.
// This is the core domain object that flows through the entire pipeline.
//
// Go learning note: The `json:"event_id"` syntax is a "struct tag" — it's
// metadata attached to each field that tells encoding/json how to serialize
// and deserialize the field. Tags are read at runtime via reflection.
// The `omitempty` option means the field is omitted from JSON output when
// it has its zero value (empty string, nil map, etc.).
type TelemetryEvent struct {
	EventID    string            `json:"event_id"`             // Globally unique identifier for this event (used for idempotent writes)
	EventType  string            `json:"event_type"`           // What happened, e.g., "page_view", "click", "api_call"
	Source     string            `json:"source"`               // Which service or app emitted this event
	Timestamp  int64             `json:"timestamp"`            // Unix millisecond timestamp when the event occurred
	Properties map[string]string `json:"properties,omitempty"` // Arbitrary key-value metadata (flexible schema)
	SessionID  string            `json:"session_id,omitempty"` // Optional session correlation ID
}

// IngestRequest is the JSON body expected by the REST and gRPC ingest endpoints.
// It wraps a batch of events to allow clients to send multiple events in one call.
type IngestRequest struct {
	Events []TelemetryEvent `json:"events"` // Slice of events to ingest
}

// IngestResponse tells the client how many events were accepted vs rejected.
type IngestResponse struct {
	Accepted int `json:"accepted"` // Events that passed validation and were enqueued
	Rejected int `json:"rejected"` // Events that failed validation
}

// Validate checks that required fields are present and fills in defaults.
//
// Go learning note: The `(e *TelemetryEvent)` between `func` and `Validate`
// is called a "method receiver." This makes Validate a method ON TelemetryEvent.
// Using a *pointer* receiver (`*TelemetryEvent`) means we can modify the
// struct in place (e.g., setting Timestamp). A value receiver would get a copy.
func (e *TelemetryEvent) Validate() error {
	if e.EventID == "" {
		return ErrMissingEventID
	}
	if e.EventType == "" {
		return ErrMissingEventType
	}
	if e.Source == "" {
		return ErrMissingSource
	}
	// Default the timestamp to "now" if the client didn't send one.
	// Go learning note: 0 is the "zero value" for int64 in Go. Every type
	// has a zero value: 0 for numbers, "" for strings, nil for pointers/slices/maps.
	if e.Timestamp == 0 {
		e.Timestamp = time.Now().UnixMilli()
	}
	return nil
}
