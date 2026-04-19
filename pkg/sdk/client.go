/*
Package sdk provides a lightweight Go client for the instrumentation
service's REST API with automatic retries and circuit breaking.

This file contains the Client implementation which:
  - Sends telemetry events to the instrumentation service over HTTP
  - Retries failed requests with exponential backoff
  - Uses a circuit breaker to fail fast when the service is down
  - Supports the "functional options" pattern for configuration

Usage:

	client := sdk.NewClient("http://localhost:8080",
		sdk.WithTimeout(10*time.Second),
		sdk.WithMaxRetries(5),
	)
	resp, err := client.Send(ctx, events)
*/
package sdk

import (
	"bytes"         // bytes.NewReader wraps byte slices into io.Reader for HTTP request bodies
	"context"       // context.Context for cancellation and deadline propagation
	"encoding/json" // JSON marshaling/unmarshaling for HTTP request and response bodies
	"fmt"           // fmt.Errorf for error wrapping
	"net/http"      // HTTP client and request/response types
	"time"          // time.Duration for timeouts and backoff intervals
)

// Client sends telemetry events to the instrumentation service.
// It includes retry logic and circuit breaking for resilience.
type Client struct {
	baseURL    string          // Base URL of the instrumentation service, e.g., "http://localhost:8080"
	httpClient *http.Client    // Underlying HTTP client (with configurable timeout)
	cb         *CircuitBreaker // Circuit breaker for fail-fast behaviour
	maxRetries int             // Maximum number of retry attempts on failure
}

// ClientOption is a function that configures a Client.
//
// Go learning note: This is the "Functional Options" pattern, a popular Go
// idiom for configuring structs. Instead of a massive constructor with many
// parameters, you pass option functions that each set one field.
// Benefits:
//   - New options can be added without breaking existing callers
//   - Options are self-documenting (WithTimeout, WithMaxRetries)
//   - Default values are set in the constructor, options override them
type ClientOption func(*Client)

// WithTimeout sets the HTTP client timeout.
// Go learning note: Each With* function returns a ClientOption (which is
// itself a function). The returned function captures `d` via closure.
func WithTimeout(d time.Duration) ClientOption {
	return func(c *Client) { c.httpClient.Timeout = d }
}

// WithMaxRetries sets the maximum number of retry attempts.
func WithMaxRetries(n int) ClientOption {
	return func(c *Client) { c.maxRetries = n }
}

// WithCircuitBreaker configures a custom circuit breaker.
func WithCircuitBreaker(threshold int, resetTimeout time.Duration) ClientOption {
	return func(c *Client) { c.cb = NewCircuitBreaker(threshold, resetTimeout) }
}

// NewClient creates a client pointing at the given base URL.
//
// Go learning note: The `opts ...ClientOption` parameter is a "variadic"
// parameter — it accepts zero or more ClientOption arguments. Inside the
// function, `opts` is a slice of ClientOption. This enables the clean API:
//
//	client := sdk.NewClient("http://...", sdk.WithTimeout(10*time.Second))
func NewClient(baseURL string, opts ...ClientOption) *Client {
	// Start with sensible defaults.
	c := &Client{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		cb:         NewCircuitBreaker(5, 30*time.Second),
		maxRetries: 3,
	}
	// Apply each option function to override defaults.
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Send publishes a batch of events. Retries with exponential backoff;
// fails fast when the circuit breaker is open.
//
// The circuit breaker wraps the retry logic: if too many requests fail,
// subsequent calls return ErrCircuitOpen immediately without even trying,
// giving the downstream service time to recover.
func (c *Client) Send(ctx context.Context, events []Event) (*IngestResponse, error) {
	// Go learning note: cb.Execute takes a function as a parameter (a "closure").
	// The circuit breaker decides whether to actually call this function or
	// short-circuit with ErrCircuitOpen. This is the "strategy" pattern.
	return c.cb.Execute(func() (*IngestResponse, error) {
		return c.sendWithRetry(ctx, events)
	})
}

// sendWithRetry attempts to send events, retrying with exponential backoff.
//
// Exponential backoff: wait 100ms, 200ms, 400ms, 800ms... between retries.
// This prevents thundering herd problems when a service recovers.
func (c *Client) sendWithRetry(ctx context.Context, events []Event) (*IngestResponse, error) {
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			// Go learning note: `1 << uint(attempt-1)` is a bit shift for
			// exponential calculation: 1<<0=1, 1<<1=2, 1<<2=4, etc.
			// So backoff = 100ms, 200ms, 400ms, ...
			backoff := time.Duration(1<<uint(attempt-1)) * 100 * time.Millisecond
			// Go learning note: `select` with two cases lets us wait for
			// EITHER the backoff timer OR context cancellation, whichever
			// happens first. This makes the retry loop cancellable.
			select {
			case <-time.After(backoff):
				// Backoff elapsed, continue to retry.
			case <-ctx.Done():
				// Context was cancelled (e.g., caller timed out). Stop retrying.
				return nil, ctx.Err()
			}
		}

		resp, err := c.doSend(ctx, events)
		if err == nil {
			return resp, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("all %d retries exhausted: %w", c.maxRetries, lastErr)
}

// doSend performs a single HTTP POST to the instrumentation service.
func (c *Client) doSend(ctx context.Context, events []Event) (*IngestResponse, error) {
	// Serialize the events to JSON.
	body, err := json.Marshal(ingestRequest{Events: events})
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	// Go learning note: http.NewRequestWithContext creates an HTTP request
	// that respects the context's deadline and cancellation. If the context
	// is cancelled while the request is in-flight, the HTTP call aborts.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/events", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	// Go learning note: `defer resp.Body.Close()` is CRITICAL. In Go, HTTP
	// response bodies MUST be closed, or you'll leak TCP connections. The
	// connection can only be reused by the connection pool once the body
	// is fully read and closed.
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var result IngestResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return &result, nil
}
