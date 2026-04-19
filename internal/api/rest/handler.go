/*
Package rest implements the HTTP/JSON API for ingesting telemetry events.

This package uses the Gin web framework for routing and JSON binding.
It exposes two endpoints:
  - POST /v1/events — accepts a batch of telemetry events
  - GET  /health    — simple health check for load balancers

WHY REST: REST is the most widely supported protocol. Any language with an
HTTP client can send telemetry events. The gRPC endpoint exists for
high-throughput internal services that benefit from protobuf efficiency.
*/
package rest

import (
	"net/http" // net/http provides HTTP status codes and the http.Server type
	"time"     // time.Second for server timeout configuration

	"github.com/gin-gonic/gin" // gin is a popular HTTP web framework for Go with JSON binding, routing, and middleware

	"github.com/cipheraxat/instrumentation-service/internal/batcher" // the shared event batcher
	"github.com/cipheraxat/instrumentation-service/internal/metrics" // Prometheus metrics
	"github.com/cipheraxat/instrumentation-service/internal/model"   // request/response types
)

// NewServer creates and configures an HTTP server with all routes registered.
// It returns an *http.Server that the caller starts with ListenAndServe().
//
// Go learning note: Returning an *http.Server (from the standard library)
// rather than a custom type means the caller can use standard methods like
// Shutdown() for graceful termination. This is good API design.
func NewServer(addr string, bat *batcher.Batcher, m *metrics.Metrics) *http.Server {
	// Go learning note: gin.SetMode(gin.ReleaseMode) disables debug logging
	// and colourised console output. Always use ReleaseMode in production.
	gin.SetMode(gin.ReleaseMode)
	// gin.New() creates a bare router without any middleware.
	// gin.Default() would include Logger and Recovery middleware.
	r := gin.New()
	// gin.Recovery() middleware catches panics inside handlers and returns
	// a 500 response instead of crashing the server.
	r.Use(gin.Recovery())

	// Wire the handler with its dependencies.
	h := &handler{batcher: bat, metrics: m}
	// Register routes — POST for event ingestion, GET for health checks.
	r.POST("/v1/events", h.ingest)
	r.GET("/health", h.health)

	// Return a standard http.Server with production-ready timeout settings.
	// Go learning note: These timeouts prevent slow clients from holding
	// connections open forever (which would exhaust server resources).
	return &http.Server{
		Addr:         addr,
		Handler:      r,                // Gin's router implements http.Handler
		ReadTimeout:  5 * time.Second,  // Max time to read the entire request (including body)
		WriteTimeout: 10 * time.Second, // Max time to write the response
		IdleTimeout:  30 * time.Second, // Max time to keep a keep-alive connection open
	}
}

// handler holds dependencies for HTTP endpoint handlers.
// Go learning note: In Go, HTTP handlers are typically methods on a struct
// that holds their dependencies. This is the Go equivalent of constructor
// injection in other languages.
type handler struct {
	batcher *batcher.Batcher // Where to send validated events
	metrics *metrics.Metrics // Prometheus metrics for request duration and errors
}

// ingest handles POST /v1/events — receives a batch of telemetry events,
// validates each one, and enqueues valid events into the batcher.
//
// Go learning note: gin.Context wraps the http.Request and http.ResponseWriter.
// It provides convenience methods like ShouldBindJSON (parse JSON body) and
// c.JSON (write JSON response with status code).
func (h *handler) ingest(c *gin.Context) {
	// Record when we started processing this request.
	start := time.Now()
	// Go learning note: `defer func() { ... }()` schedules this anonymous
	// function to run when ingest() returns. This is a common pattern for
	// recording duration metrics — time.Since(start) captures the total
	// elapsed time regardless of how the function exits.
	defer func() {
		h.metrics.RESTRequestDuration.Observe(time.Since(start).Seconds())
	}()

	// Go learning note: ShouldBindJSON reads the request body and unmarshals
	// it into the struct. If the JSON is malformed, it returns an error.
	// `var req model.IngestRequest` declares a zero-valued struct — Go doesn't
	// require constructors; zero values are always valid.
	var req model.IngestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Go learning note: gin.H is shorthand for map[string]interface{} —
		// a convenient way to build JSON response objects inline.
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	if len(req.Events) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "events array is empty"})
		return
	}

	// Validate each event individually and enqueue valid ones.
	// Rejected events are counted but don't fail the whole request.
	accepted, rejected := 0, 0
	for i := range req.Events {
		// Go learning note: `for i := range req.Events` with `req.Events[i]`
		// (not `for _, e := range`) gives us a POINTER to the actual element,
		// so Validate() can modify it in place (e.g., setting default Timestamp).
		if err := req.Events[i].Validate(); err != nil {
			h.metrics.ValidationErrors.Inc()
			rejected++
			continue
		}
		h.batcher.Add(req.Events[i])
		h.metrics.EventsIngested.Inc()
		accepted++
	}

	// Return HTTP 202 Accepted (not 200 OK) because the events are enqueued
	// for asynchronous processing, not yet persisted to their final destination.
	c.JSON(http.StatusAccepted, model.IngestResponse{
		Accepted: accepted,
		Rejected: rejected,
	})
}

// health handles GET /health — a simple liveness probe for load balancers
// and container orchestrators (Docker, Kubernetes).
func (h *handler) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
