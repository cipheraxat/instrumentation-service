/*
Package batcher contains unit tests for the Batcher.

Go learning note: Test files in Go MUST end with `_test.go`. They are only
compiled and run when you execute `go test`. The test file lives in the SAME
package as the code it tests, giving it access to unexported (lowercase) fields.

To run these tests:

	go test ./internal/batcher/        # run batcher tests
	go test ./internal/batcher/ -v     # verbose output showing each test
	go test ./...                       # run ALL tests in the project
*/
package batcher

import (
	"fmt"     // fmt.Sprintf for generating unique event IDs
	"sync"    // sync.Mutex for thread-safe mock; sync.WaitGroup for concurrent test
	"testing" // testing.T is Go's built-in test framework (no external dependencies!)
	"time"    // time.Sleep for waiting on async flush behaviour

	"github.com/cipheraxat/instrumentation-service/internal/metrics" // real metrics (each test creates its own)
	"github.com/cipheraxat/instrumentation-service/internal/model"   // TelemetryEvent domain type
)

// --- test doubles ---
//
// Go learning note: Go doesn't have mocking frameworks built in. Instead,
// you define interfaces (like Producer and Store in batcher.go) and create
// simple "test doubles" — structs that implement those interfaces with
// controllable behaviour. This is sometimes called a "fake" or "mock".

// mockProducer records all events that were "sent" so tests can assert on them.
// It uses a mutex because the batcher flushes from a background goroutine.
type mockProducer struct {
	mu     sync.Mutex             // Protects concurrent access from flush goroutine
	events []model.TelemetryEvent // All events received across all SendBatch calls
}

// SendBatch implements the Producer interface for testing.
// Go learning note: This method makes mockProducer satisfy the Producer
// interface automatically (no "implements" keyword needed).
func (m *mockProducer) SendBatch(events []model.TelemetryEvent) error {
	m.mu.Lock()
	// Go learning note: `defer m.mu.Unlock()` guarantees the mutex is
	// released even if append panics. This is a safety pattern.
	defer m.mu.Unlock()
	m.events = append(m.events, events...)
	return nil
}

// received returns the total number of events flushed so far (thread-safe).
func (m *mockProducer) received() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.events)
}

// mockStore is a no-op Store implementation — we don't need to verify
// database writes in these unit tests (that's for integration tests).
type mockStore struct{}

// InsertBatch is a no-op — satisfies the Store interface.
func (m *mockStore) InsertBatch(_ []model.TelemetryEvent) error { return nil }

// event is a test helper that creates a valid TelemetryEvent with the given ID.
// Go learning note: Helper functions in tests make test code more readable
// by hiding irrelevant details. The test only cares about the event ID;
// the other fields are just "noise" needed to pass validation.
func event(id string) model.TelemetryEvent {
	return model.TelemetryEvent{
		EventID:   id,
		EventType: "click",
		Source:    "test-service",
		Timestamp: time.Now().UnixMilli(),
	}
}

// --- tests ---
//
// Go learning note: Go test functions MUST:
//   1. Be in a file ending with _test.go
//   2. Start with "Test" followed by an uppercase letter
//   3. Accept exactly one parameter: *testing.T
//
// Run a specific test: go test -run TestBatcher_FlushOnThreshold ./internal/batcher/

// TestBatcher_FlushOnThreshold verifies that adding MaxSize events triggers
// an immediate flush without waiting for the interval timer.
func TestBatcher_FlushOnThreshold(t *testing.T) {
	prod := &mockProducer{}
	// MaxSize=5 means a flush happens after 5 events. FlushInterval is very
	// long (10s) to ensure the threshold triggers first, not the timer.
	b := New(Config{MaxSize: 5, FlushInterval: 10 * time.Second}, prod, &mockStore{}, metrics.New())
	b.Start()
	// Go learning note: `defer b.Stop()` ensures the batcher is cleaned up
	// even if the test fails (t.Errorf doesn't stop execution like t.Fatalf).
	defer b.Stop()

	// Add exactly 5 events — should hit the threshold.
	for i := 0; i < 5; i++ {
		b.Add(event(fmt.Sprintf("evt-%d", i)))
	}

	// Go learning note: time.Sleep in tests is a "smell" because it makes
	// tests slow and flaky. In production code, you'd use channels or
	// sync.Cond for synchronisation. But for testing async behaviour with
	// simple goroutines, a small sleep is pragmatic.
	time.Sleep(100 * time.Millisecond)

	// Go learning note: t.Errorf reports a test failure but continues
	// execution. Use t.Fatalf to stop the test immediately on failure.
	if got := prod.received(); got != 5 {
		t.Errorf("expected 5 events flushed on threshold, got %d", got)
	}
}

// TestBatcher_FlushOnInterval verifies that events are flushed after the
// interval timer fires, even when the buffer is well below MaxSize.
func TestBatcher_FlushOnInterval(t *testing.T) {
	prod := &mockProducer{}
	// MaxSize=100 (much larger than our 2 events), FlushInterval=200ms.
	// The timer should trigger a flush before the threshold is reached.
	b := New(Config{MaxSize: 100, FlushInterval: 200 * time.Millisecond}, prod, &mockStore{}, metrics.New())
	b.Start()
	defer b.Stop()

	b.Add(event("evt-1"))
	b.Add(event("evt-2"))

	time.Sleep(400 * time.Millisecond)

	if got := prod.received(); got != 2 {
		t.Errorf("expected 2 events flushed on interval, got %d", got)
	}
}

// TestBatcher_DrainOnStop verifies that calling Stop() flushes any remaining
// events in the buffer before the goroutine exits. This is critical for
// graceful shutdown — we don't want to lose in-flight events.
func TestBatcher_DrainOnStop(t *testing.T) {
	prod := &mockProducer{}
	b := New(Config{MaxSize: 100, FlushInterval: 10 * time.Second}, prod, &mockStore{}, metrics.New())
	b.Start()

	b.Add(event("evt-1"))
	b.Stop()

	if got := prod.received(); got != 1 {
		t.Errorf("expected 1 event drained on stop, got %d", got)
	}
}

// TestBatcher_ConcurrentAdds verifies that the batcher is safe for concurrent
// use. 100 goroutines each add one event simultaneously.
//
// Go learning note: This test exercises the mutex-protected Add() method.
// If the mutex were missing, the Go race detector (`go test -race`) would
// catch the data race. ALWAYS run `go test -race` during development.
func TestBatcher_ConcurrentAdds(t *testing.T) {
	prod := &mockProducer{}
	b := New(Config{MaxSize: 1000, FlushInterval: 100 * time.Millisecond}, prod, &mockStore{}, metrics.New())
	b.Start()

	// Go learning note: We use sync.WaitGroup to wait for all 100 goroutines
	// to finish before asserting. Without this, the test might check results
	// before all goroutines have called Add().
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		// Go learning note: `go func(id int) { ... }(i)` passes `i` as a
		// parameter to the goroutine. If we captured `i` directly by closure,
		// all goroutines might see the same value due to the loop variable
		// being shared. Passing it as a function argument creates a copy.
		go func(id int) {
			defer wg.Done()
			b.Add(event(fmt.Sprintf("evt-%d", id)))
		}(i)
	}
	wg.Wait()
	b.Stop()

	if got := prod.received(); got != 100 {
		t.Errorf("expected 100 events from concurrent adds, got %d", got)
	}
}
