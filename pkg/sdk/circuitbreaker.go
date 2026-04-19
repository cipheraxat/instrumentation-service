/*
Package sdk — circuitbreaker.go implements the Circuit Breaker pattern.

The circuit breaker prevents cascading failures by failing fast when the
downstream service (instrumentation-service) is unhealthy. It has three states:

  - CLOSED: Normal operation. Requests flow through. Failures are counted.
  - OPEN: Too many failures. Requests are rejected immediately with ErrCircuitOpen.
  - HALF-OPEN: After a timeout, one request is allowed through to test recovery.
    If it succeeds → CLOSED. If it fails → OPEN again.

This pattern is essential in distributed systems to prevent one failing service
from bringing down all of its callers.
*/
package sdk

import (
	"errors" // errors.New for the sentinel ErrCircuitOpen error
	"sync"   // sync.Mutex for thread-safe state transitions
	"time"   // time.Duration for the reset timeout, time.Since for elapsed time
)

// ErrCircuitOpen is returned when the breaker is in the open state.
// Go learning note: This is a "sentinel error" — a package-level variable
// that callers can compare against: `if errors.Is(err, sdk.ErrCircuitOpen)`.
var ErrCircuitOpen = errors.New("circuit breaker is open")

// state represents the circuit breaker's current state.
// Go learning note: This is a type alias for int with named constants below.
// Go doesn't have enums, so `iota` with a custom type is the standard pattern.
type state int

const (
	stateClosed   state = iota // iota = 0: Normal operation, requests flow through
	stateOpen                  // iota = 1: Failing fast, rejecting all requests
	stateHalfOpen              // iota = 2: Testing recovery with a single request
)

// CircuitBreaker prevents cascading failures by failing fast when the
// downstream service is unhealthy.
//
// All fields are protected by mu because Execute() may be called from
// multiple goroutines concurrently.
type CircuitBreaker struct {
	mu           sync.Mutex    // Protects all mutable state below
	state        state         // Current state: closed, open, or half-open
	failures     int           // Number of consecutive failures
	threshold    int           // Number of failures before tripping to open
	resetTimeout time.Duration // How long to wait in open state before trying half-open
	lastFailure  time.Time     // When the last failure occurred (used to time the reset)
}

// NewCircuitBreaker creates a breaker that trips after `threshold` failures
// and waits `resetTimeout` before allowing a test request through.
func NewCircuitBreaker(threshold int, resetTimeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		state:        stateClosed,
		threshold:    threshold,
		resetTimeout: resetTimeout,
	}
}

// Execute runs fn if the circuit is closed or half-open. On success the
// breaker resets; on failure it increments the failure counter and may trip.
//
// Go learning note: The parameter `fn func() (*IngestResponse, error)` is
// a function value (first-class function). Go treats functions as values
// that can be passed around, stored in variables, and called later.
func (cb *CircuitBreaker) Execute(fn func() (*IngestResponse, error)) (*IngestResponse, error) {
	// --- Check state (with lock) ---
	cb.mu.Lock()
	if cb.state == stateOpen {
		// If enough time has passed since the last failure, transition to
		// half-open and allow ONE test request through.
		if time.Since(cb.lastFailure) > cb.resetTimeout {
			cb.state = stateHalfOpen
		} else {
			// Still in open state — reject immediately without calling fn.
			cb.mu.Unlock()
			return nil, ErrCircuitOpen
		}
	}
	// Release the lock BEFORE calling fn — fn might take a long time
	// (network I/O) and we don't want to block other goroutines.
	cb.mu.Unlock()

	// --- Execute the function ---
	resp, err := fn()

	// --- Update state based on result (with lock) ---
	cb.mu.Lock()
	// Go learning note: `defer cb.mu.Unlock()` ensures the mutex is released
	// when this function returns, even if there's a panic.
	defer cb.mu.Unlock()

	if err != nil {
		// Record the failure.
		cb.failures++
		cb.lastFailure = time.Now()
		// If we've hit the threshold, trip the breaker to open state.
		if cb.failures >= cb.threshold {
			cb.state = stateOpen
		}
		return nil, err
	}

	// Success! Reset the breaker to healthy state.
	cb.failures = 0
	cb.state = stateClosed
	return resp, nil
}
