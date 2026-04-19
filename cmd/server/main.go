/*
Package main is the entry point for the instrumentation-service.

This service collects telemetry events from clients via REST and gRPC APIs,
batches them in memory for efficiency, and flushes them to both Apache Kafka
(for downstream consumers like the billing pipeline) and PostgreSQL (for
durable storage and querying). It also exposes Prometheus metrics.

Go learning note: In Go, every executable must have a `package main` with
a `func main()`. This is the starting point of the program — like `public
static void main(String[] args)` in Java or `if __name__ == "__main__"` in Python.
*/
package main

import (
	// --- Standard library imports ---
	"context"   // context.Context is Go's way of passing cancellation signals, deadlines, and request-scoped values through the call chain
	"log/slog"  // slog is Go 1.21+'s structured logging package — outputs JSON logs with key-value pairs
	"net"       // net provides low-level network primitives; we use net.Listen for the gRPC TCP listener
	"net/http"  // net/http is Go's built-in HTTP server and client — no frameworks required!
	"os"        // os provides operating system functions like reading env vars and exiting
	"os/signal" // os/signal lets us listen for OS signals (SIGINT, SIGTERM) for graceful shutdown
	"syscall"   // syscall defines OS-level constants like SIGINT and SIGTERM
	"time"      // time provides duration types and time-based operations

	// --- Third-party imports ---
	"github.com/prometheus/client_golang/prometheus/promhttp" // promhttp exposes Prometheus metrics as an HTTP handler at /metrics
	"google.golang.org/grpc"                                  // grpc is Google's high-performance RPC framework for Go

	// --- Internal imports (our own packages within this project) ---
	// Go learning note: packages under "internal/" are ONLY importable by code
	// within this module. This is Go's built-in encapsulation mechanism.
	"github.com/cipheraxat/instrumentation-service/internal/api/grpcapi" // gRPC API handler for telemetry ingestion
	"github.com/cipheraxat/instrumentation-service/internal/api/rest"    // REST API handler for telemetry ingestion
	"github.com/cipheraxat/instrumentation-service/internal/batcher"     // in-memory event batcher with flush-on-size and flush-on-interval
	"github.com/cipheraxat/instrumentation-service/internal/config"      // configuration loading from environment variables
	"github.com/cipheraxat/instrumentation-service/internal/kafka"       // Kafka producer for publishing event batches
	"github.com/cipheraxat/instrumentation-service/internal/metrics"     // Prometheus metrics definitions
	"github.com/cipheraxat/instrumentation-service/internal/storage"     // PostgreSQL storage layer
)

// main is the application entry point. It wires together all dependencies,
// starts servers, and blocks until a shutdown signal is received.
//
// Go learning note: Go does NOT have dependency injection frameworks like
// Spring. Instead, we manually construct and wire dependencies in main().
// This is sometimes called "manual DI" or "composition root" — it keeps
// the dependency graph explicit and easy to follow.
func main() {
	// Go learning note: slog.New creates a structured logger. slog.NewJSONHandler
	// outputs logs as JSON objects (great for log aggregators like ELK or Datadog).
	// slog.SetDefault makes this the global logger used by slog.Info(), slog.Error(), etc.
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// Load all configuration from environment variables with sensible defaults.
	// Go learning note: Go functions often return (value, error) pairs.
	// The caller is expected to check `err != nil` immediately — this is the
	// idiomatic Go error handling pattern (no try/catch).
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		// Go learning note: os.Exit(1) terminates the process immediately
		// with a non-zero exit code. Unlike log.Fatal, it does NOT run deferred functions.
		os.Exit(1)
	}

	// --- dependencies ---
	// Create the Prometheus metrics registry. All counters, histograms, and
	// gauges are defined here and shared across the application.
	m := metrics.New()

	// Create the Kafka producer — events will be published here for downstream
	// consumers (e.g., the usage-billing-pipeline).
	producer, err := kafka.NewProducer(cfg.Kafka, m)
	if err != nil {
		slog.Error("failed to create kafka producer", "error", err)
		os.Exit(1)
	}
	// Go learning note: `defer` schedules a function call to run when the
	// enclosing function (main) returns. Defers run in LIFO (last-in-first-out)
	// order, so producer.Close() runs AFTER store.Close(). This is Go's
	// equivalent of try-with-resources in Java or "with" in Python.
	defer producer.Close()

	// Open a connection pool to PostgreSQL for durable event storage.
	store, err := storage.NewPostgres(cfg.Postgres)
	if err != nil {
		slog.Error("failed to connect to postgres", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	// The Batcher is the core of the ingestion pipeline:
	// - Events arrive one-at-a-time from REST/gRPC handlers
	// - The batcher buffers them in memory
	// - When MaxSize is reached OR FlushInterval elapses, it flushes the
	//   entire batch to Kafka and PostgreSQL in one go (much more efficient
	//   than writing each event individually).
	bat := batcher.New(batcher.Config{
		MaxSize:       cfg.Batcher.MaxSize,
		FlushInterval: cfg.Batcher.FlushInterval,
	}, producer, store, m)
	// Start() launches a background goroutine that flushes on a timer.
	bat.Start()
	// defer bat.Stop() ensures in-flight events are drained before shutdown.
	defer bat.Stop()

	// --- REST server ---
	// The REST server accepts JSON-encoded telemetry events via POST /v1/events.
	restServer := rest.NewServer(cfg.REST.Addr, bat, m)
	// Go learning note: `go func() { ... }()` launches a "goroutine" — a
	// lightweight concurrent thread managed by the Go runtime (not an OS thread).
	// We run each server in its own goroutine so they can all listen concurrently.
	// The `()` at the end immediately invokes the anonymous function.
	go func() {
		slog.Info("starting REST server", "addr", cfg.REST.Addr)
		// Go learning note: http.ErrServerClosed is returned when Shutdown()
		// is called — it's expected during graceful shutdown, not a real error.
		if err := restServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("REST server error", "error", err)
		}
	}()

	// --- gRPC server ---
	// Go learning note: gRPC requires a raw TCP listener (net.Listen) rather
	// than http.ListenAndServe. The gRPC framework manages its own HTTP/2
	// transport over this TCP connection.
	lis, err := net.Listen("tcp", cfg.GRPC.Addr)
	if err != nil {
		slog.Error("failed to listen for gRPC", "error", err)
		os.Exit(1)
	}
	// Go learning note: grpc.UnaryInterceptor is similar to HTTP middleware —
	// it wraps every gRPC call to add cross-cutting concerns (here: metrics).
	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(grpcapi.MetricsInterceptor(m)),
	)
	// Register wires our IngestService implementation into the gRPC server.
	grpcapi.Register(grpcServer, bat, m)
	go func() {
		slog.Info("starting gRPC server", "addr", cfg.GRPC.Addr)
		if err := grpcServer.Serve(lis); err != nil {
			slog.Error("gRPC server error", "error", err)
		}
	}()

	// --- metrics server ---
	// This is a dedicated HTTP server ONLY for Prometheus scraping.
	// Running it on a separate port (default :2112) means Prometheus can
	// scrape metrics without interfering with the main API traffic.
	metricsMux := http.NewServeMux()
	// promhttp.Handler() returns an http.Handler that serves all registered
	// Prometheus metrics in the text exposition format.
	metricsMux.Handle("/metrics", promhttp.Handler())
	metricsServer := &http.Server{Addr: cfg.Metrics.Addr, Handler: metricsMux}
	go func() {
		slog.Info("starting metrics server", "addr", cfg.Metrics.Addr)
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("metrics server error", "error", err)
		}
	}()

	// --- graceful shutdown ---
	// Go learning note: Channels are Go's primary mechanism for goroutine
	// communication. `make(chan os.Signal, 1)` creates a BUFFERED channel
	// with capacity 1. The buffer size of 1 is important — signal.Notify
	// won't block if nothing is reading yet.
	quit := make(chan os.Signal, 1)
	// signal.Notify registers this channel to receive SIGINT (Ctrl+C) and
	// SIGTERM (docker stop, Kubernetes pod termination).
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	// Go learning note: `<-quit` is a "blocking channel receive" — the main
	// goroutine sleeps here until a signal arrives. This is why the program
	// stays alive instead of exiting immediately after starting the servers.
	<-quit
	slog.Info("shutting down…")

	// Create a context with a 10-second deadline. If shutdown takes longer
	// than 10 seconds, the context will be cancelled and in-flight requests
	// will be forcibly terminated.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	// Go learning note: Always defer cancel() when using context.WithTimeout
	// or context.WithCancel to release resources associated with the context.
	defer cancel()

	// Shut down servers in order: stop accepting new requests first,
	// then drain in-flight work.
	grpcServer.GracefulStop()
	// Go learning note: `_ = restServer.Shutdown(ctx)` uses the blank
	// identifier `_` to explicitly discard the error. This signals to readers
	// that we know there's an error return but are intentionally ignoring it
	// (during shutdown, there's not much we can do if shutdown itself fails).
	_ = restServer.Shutdown(ctx)
	_ = metricsServer.Shutdown(ctx)

	// After this line, the deferred calls run in reverse order:
	// bat.Stop() → store.Close() → producer.Close()
	slog.Info("shutdown complete")
}
