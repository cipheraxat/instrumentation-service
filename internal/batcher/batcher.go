/*
Package batcher implements an in-memory event buffer that flushes to downstream
sinks (Kafka and PostgreSQL) using two strategies:

 1. Flush-on-threshold: when the buffer reaches MaxSize events
 2. Flush-on-interval: when FlushInterval elapses, regardless of buffer size

Whichever condition is met first triggers a flush. This is a classic throughput
optimisation pattern: instead of writing every event individually (high latency,
high I/O), we batch them and write them all at once.

WHY this exists: Writing each event to Kafka and PostgreSQL one-by-one would
be ~100x slower due to network round-trip overhead. Batching amortises that
cost across many events.

Concurrency: The batcher is safe for concurrent use. Multiple goroutines
(REST handler, gRPC handler) can call Add() simultaneously, protected by a mutex.
*/
package batcher

import (
	"log/slog" // structured logging for batch flush events
	"sync"     // sync.Mutex for thread-safe buffer access; sync.WaitGroup for shutdown coordination
	"time"     // time.NewTicker creates the periodic flush timer

	"github.com/cipheraxat/instrumentation-service/internal/metrics" // Prometheus metrics for observability
	"github.com/cipheraxat/instrumentation-service/internal/model"   // TelemetryEvent domain type
)

// Producer sends a batch of events to a downstream sink (e.g. Kafka).
//
// Go learning note: This is a Go INTERFACE. Interfaces in Go are satisfied
// IMPLICITLY — any type that has a SendBatch method with this exact signature
// automatically implements this interface. No "implements" keyword needed!
// This is called "structural typing" or "duck typing" and is one of Go's
// most powerful features. It enables easy mocking in tests.
type Producer interface {
	SendBatch(events []model.TelemetryEvent) error
}

// Store persists a batch of events durably (e.g. PostgreSQL).
// Like Producer, this is an interface so we can swap in test doubles.
type Store interface {
	InsertBatch(events []model.TelemetryEvent) error
}

// Config controls flush-on-threshold and flush-on-interval behaviour.
type Config struct {
	MaxSize       int
	FlushInterval time.Duration
}

// Batcher accumulates events in memory and flushes them when either the
// buffer reaches MaxSize or FlushInterval elapses — whichever comes first.
//
// Go learning note: This struct has both exported (Config) and unexported
// (mu, buffer, done, wg) fields. Unexported fields are private to this
// package — outside code cannot access them directly.
type Batcher struct {
	cfg      Config           // Flush configuration (max size and interval)
	producer Producer         // Where to send batched events (Kafka)
	store    Store            // Where to persist batched events (PostgreSQL)
	metrics  *metrics.Metrics // Prometheus metrics

	// Go learning note: sync.Mutex is a mutual exclusion lock. You call
	// mu.Lock() before accessing shared data and mu.Unlock() after.
	// This prevents race conditions when multiple goroutines access the buffer.
	mu     sync.Mutex
	buffer []model.TelemetryEvent // In-memory event buffer, protected by mu

	// Go learning note: `done` is a "signal channel" — it's used to tell
	// the background goroutine to stop. We never send values on it; instead,
	// we close it. Closing a channel causes ALL receivers to unblock immediately.
	done chan struct{}

	// Go learning note: sync.WaitGroup tracks how many goroutines are still
	// running. wg.Add(1) increments the count, wg.Done() decrements it,
	// and wg.Wait() blocks until the count reaches zero. It's the standard
	// way to wait for goroutines to finish during shutdown.
	wg sync.WaitGroup
}

// New creates a Batcher with a pre-allocated buffer.
//
// Go learning note: The function name is `New` (not `NewBatcher`) because
// in Go the package name provides context: callers write `batcher.New()`.
// This is a Go naming convention — avoid stuttering like `batcher.NewBatcher`.
func New(cfg Config, producer Producer, store Store, m *metrics.Metrics) *Batcher {
	return &Batcher{
		cfg:      cfg,
		producer: producer,
		store:    store,
		metrics:  m,
		// Go learning note: make([]T, 0, capacity) creates a slice with
		// length 0 and the given capacity. Pre-allocating avoids repeated
		// memory allocations as events are appended.
		buffer: make([]model.TelemetryEvent, 0, cfg.MaxSize),
		// Go learning note: make(chan struct{}) creates an UNBUFFERED channel.
		// struct{} (empty struct) takes zero bytes of memory — it's the
		// idiomatic type for signal-only channels where no data is transferred.
		done: make(chan struct{}),
	}
}

// Add enqueues an event. If the buffer reaches MaxSize it triggers an
// immediate flush — this is the "flush-on-threshold" path.
//
// Thread safety: Add is safe to call from multiple goroutines concurrently.
func (b *Batcher) Add(event model.TelemetryEvent) {
	// Lock the mutex to safely read and write the shared buffer.
	b.mu.Lock()
	b.buffer = append(b.buffer, event)
	shouldFlush := len(b.buffer) >= b.cfg.MaxSize
	// Capture buffer size while still holding the lock to avoid a race
	// condition — another goroutine could modify the buffer between Unlock
	// and the metric read.
	bufLen := len(b.buffer)
	// Unlock BEFORE flushing — we don't want to hold the lock during the
	// potentially slow Kafka/Postgres writes. This is a critical performance
	// optimisation: other goroutines can continue adding events while the
	// flush is happening.
	b.mu.Unlock()

	// Go learning note: Gauge.Set updates the metric to an absolute value
	// (unlike Counter.Inc which only goes up).
	b.metrics.BufferSize.Set(float64(bufLen))

	if shouldFlush {
		b.flush()
	}
}

// Start begins the periodic flush-on-interval goroutine.
// The goroutine runs until Stop() is called.
func (b *Batcher) Start() {
	// Go learning note: wg.Add(1) tells the WaitGroup "one more goroutine
	// is running." This MUST be called BEFORE launching the goroutine,
	// not inside it, to avoid a race condition.
	b.wg.Add(1)
	// Go learning note: `go func() { ... }()` launches an anonymous function
	// as a new goroutine. Goroutines are extremely lightweight (~2KB stack),
	// so creating one here is cheap.
	go func() {
		// Go learning note: `defer b.wg.Done()` decrements the WaitGroup
		// counter when this goroutine exits, regardless of how it exits.
		defer b.wg.Done()
		// Go learning note: time.NewTicker creates a channel that receives
		// the current time at regular intervals. We use it for periodic flushing.
		ticker := time.NewTicker(b.cfg.FlushInterval)
		// Always stop the ticker to free its internal timer goroutine.
		defer ticker.Stop()

		// Go learning note: `for { select { ... } }` is the standard pattern
		// for a goroutine that waits on multiple channels. `select` blocks
		// until one of its cases can proceed. If multiple cases are ready,
		// Go picks one at random.
		for {
			select {
			case <-ticker.C:
				// Periodic flush — ensures events don't sit in the buffer forever
				// during low-traffic periods.
				b.flush()
			case <-b.done:
				// Shutdown signal received — flush any remaining events
				// before the goroutine exits (drain).
				b.flush() // drain remaining events on shutdown
				return
			}
		}
	}()
}

// Stop signals the flush goroutine to drain and exit, then blocks until done.
//
// Go learning note: close(b.done) unblocks ALL goroutines waiting on <-b.done.
// b.wg.Wait() then blocks until the goroutine has finished its final flush.
// This two-step pattern (signal + wait) ensures clean shutdown.
func (b *Batcher) Stop() {
	close(b.done)
	b.wg.Wait()
}

// flush atomically swaps the buffer with an empty one, then writes the
// batch to Kafka and PostgreSQL outside the lock.
//
// Go learning note: This is the "swap and release" pattern — the lock is
// held only for the time it takes to swap the buffer reference, not during
// the slow I/O operations. This maximises concurrency.
func (b *Batcher) flush() {
	b.mu.Lock()
	if len(b.buffer) == 0 {
		// Nothing to flush — release the lock and return early.
		b.mu.Unlock()
		return
	}
	// Swap the current buffer with a fresh, empty one.
	// After this, new Add() calls write to the new buffer while we
	// process the old batch concurrently.
	batch := b.buffer
	b.buffer = make([]model.TelemetryEvent, 0, b.cfg.MaxSize)
	b.mu.Unlock()

	start := time.Now()

	if err := b.producer.SendBatch(batch); err != nil {
		slog.Error("failed to send batch to kafka", "error", err, "batch_size", len(batch))
		b.metrics.FlushErrors.Inc()
		// Still attempt PostgreSQL write as a fallback durable store.
		// This prevents data loss when Kafka is temporarily unavailable.
	}

	if err := b.store.InsertBatch(batch); err != nil {
		slog.Error("failed to persist batch to postgres", "error", err, "batch_size", len(batch))
	}

	elapsed := time.Since(start)
	b.metrics.FlushDuration.Observe(elapsed.Seconds())
	b.metrics.EventsFlushed.Add(float64(len(batch)))

	slog.Info("flushed batch", "size", len(batch), "duration", elapsed)
}
