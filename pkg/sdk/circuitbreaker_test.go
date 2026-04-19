/*
Circuit breaker tests verify the three-state machine:

	Closed → Open → Half-Open → Closed (on success) or Open (on failure)

Go learning note: These tests follow the "Arrange-Act-Assert" pattern:
 1. Create a circuit breaker with known parameters
 2. Execute operations to drive state transitions
 3. Assert the expected outcome
*/
package sdk

import (
	"errors"  // errors.New for creating test error values; errors.Is for comparing
	"testing" // Go's built-in testing framework
	"time"    // time.Sleep to test timeout-based state transitions
)

// TestCircuitBreaker_ClosedOnSuccess verifies that a successful call keeps
// the breaker in the closed (healthy) state.
func TestCircuitBreaker_ClosedOnSuccess(t *testing.T) {
	cb := NewCircuitBreaker(3, time.Second)

	resp, err := cb.Execute(func() (*IngestResponse, error) {
		return &IngestResponse{Accepted: 1}, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Accepted != 1 {
		t.Fatalf("expected 1 accepted, got %d", resp.Accepted)
	}
}

// TestCircuitBreaker_OpensAfterThreshold verifies that the breaker trips
// to the open state after `threshold` consecutive failures.
func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	cb := NewCircuitBreaker(3, time.Second)
	fail := errors.New("downstream error")

	// Cause 3 failures to trip the breaker.
	for i := 0; i < 3; i++ {
		_, _ = cb.Execute(func() (*IngestResponse, error) {
			return nil, fail
		})
	}

	// The 4th call should be rejected immediately with ErrCircuitOpen,
	// even though the function would succeed.
	// Go learning note: errors.Is(err, ErrCircuitOpen) traverses the error
	// chain (in case of wrapped errors) to find a match. Use errors.Is
	// instead of `err == ErrCircuitOpen` for robustness.
	_, err := cb.Execute(func() (*IngestResponse, error) {
		return &IngestResponse{Accepted: 1}, nil
	})
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got %v", err)
	}
}

// TestCircuitBreaker_ResetsAfterTimeout verifies that after the reset timeout
// elapses, the breaker transitions to half-open and allows a test request.
// If that request succeeds, the breaker returns to closed (healthy).
func TestCircuitBreaker_ResetsAfterTimeout(t *testing.T) {
	cb := NewCircuitBreaker(2, 100*time.Millisecond)
	fail := errors.New("downstream error")

	// Trip the breaker with 2 failures.
	for i := 0; i < 2; i++ {
		_, _ = cb.Execute(func() (*IngestResponse, error) {
			return nil, fail
		})
	}

	// Wait longer than the reset timeout (100ms) for half-open transition.
	time.Sleep(150 * time.Millisecond)

	resp, err := cb.Execute(func() (*IngestResponse, error) {
		return &IngestResponse{Accepted: 5}, nil
	})
	if err != nil {
		t.Fatalf("unexpected error after reset: %v", err)
	}
	if resp.Accepted != 5 {
		t.Fatalf("expected 5 accepted, got %d", resp.Accepted)
	}
}

// TestCircuitBreaker_HalfOpenTripsAgain verifies that a failure during the
// half-open state sends the breaker back to open (not closed).
func TestCircuitBreaker_HalfOpenTripsAgain(t *testing.T) {
	cb := NewCircuitBreaker(1, 50*time.Millisecond)
	fail := errors.New("still failing")

	// Trip the breaker with 1 failure (threshold=1).
	_, _ = cb.Execute(func() (*IngestResponse, error) { return nil, fail })

	// Wait for half-open transition.
	time.Sleep(80 * time.Millisecond)

	// Fail again in half-open → should re-trip to open.
	_, _ = cb.Execute(func() (*IngestResponse, error) { return nil, fail })

	// Verify the breaker is open again.
	_, err := cb.Execute(func() (*IngestResponse, error) {
		return &IngestResponse{Accepted: 1}, nil
	})
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen after half-open failure, got %v", err)
	}
}
