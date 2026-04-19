/*
Package metrics defines all Prometheus metrics for the instrumentation service.

WHY this exists: Centralising metric definitions in one package ensures
consistent naming and makes it easy to see all observability signals at a glance.
The Metrics struct is created once in main() and injected into every component.

Go learning note: Prometheus metrics in Go use the prometheus/client_golang
library. There are four core metric types:
  - Counter: only goes up (e.g., total events ingested)
  - Gauge: goes up and down (e.g., current buffer size)
  - Histogram: tracks distributions with buckets (e.g., request latency)
  - Summary: like Histogram but calculates quantiles client-side
*/
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"          // prometheus is the core metrics library with Counter, Gauge, Histogram types
	"github.com/prometheus/client_golang/prometheus/promauto" // promauto automatically registers metrics with the default Prometheus registry
)

// Metrics holds all Prometheus metric instruments for the service.
// Each field is a different Prometheus metric type.
//
// Go learning note: Prometheus metric types are interfaces in Go:
//   - prometheus.Counter has Inc() and Add(float64)
//   - prometheus.Gauge has Set(float64), Inc(), Dec()
//   - prometheus.Histogram has Observe(float64)
type Metrics struct {
	EventsIngested      prometheus.Counter   // Total events received by REST and gRPC endpoints
	EventsFlushed       prometheus.Counter   // Total events successfully flushed to Kafka
	FlushDuration       prometheus.Histogram // How long each batch flush takes (seconds)
	FlushErrors         prometheus.Counter   // Number of failed flush attempts
	BufferSize          prometheus.Gauge     // Current number of events waiting in the batcher buffer
	KafkaMessagesSent   prometheus.Counter   // Successfully published Kafka messages
	KafkaSendErrors     prometheus.Counter   // Failed Kafka publishes
	SerializationErrors prometheus.Counter   // JSON serialization failures
	RESTRequestDuration prometheus.Histogram // REST endpoint latency distribution
	GRPCRequestDuration prometheus.Histogram // gRPC endpoint latency distribution
	ValidationErrors    prometheus.Counter   // Events that failed validation
}

// New creates and registers all Prometheus metrics.
//
// Go learning note: promauto.NewCounter (vs prometheus.NewCounter) automatically
// registers the metric with the global Prometheus registry. This means calling
// New() twice in the same process will panic due to duplicate registration.
// That's fine for production (called once in main), but be careful in tests —
// each test that calls metrics.New() gets fresh metrics only if the test
// binary is a separate process.
func New() *Metrics {
	return &Metrics{
		EventsIngested: promauto.NewCounter(prometheus.CounterOpts{
			Name: "instrumentation_events_ingested_total",
			Help: "Total number of events ingested",
		}),
		EventsFlushed: promauto.NewCounter(prometheus.CounterOpts{
			Name: "instrumentation_events_flushed_total",
			Help: "Total number of events flushed to Kafka",
		}),
		FlushDuration: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "instrumentation_flush_duration_seconds",
			Help:    "Time taken to flush a batch",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1},
		}),
		FlushErrors: promauto.NewCounter(prometheus.CounterOpts{
			Name: "instrumentation_flush_errors_total",
			Help: "Total number of batch flush errors",
		}),
		BufferSize: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "instrumentation_buffer_size",
			Help: "Current number of events in the in-memory buffer",
		}),
		KafkaMessagesSent: promauto.NewCounter(prometheus.CounterOpts{
			Name: "instrumentation_kafka_messages_sent_total",
			Help: "Total Kafka messages sent successfully",
		}),
		KafkaSendErrors: promauto.NewCounter(prometheus.CounterOpts{
			Name: "instrumentation_kafka_send_errors_total",
			Help: "Total Kafka send errors",
		}),
		SerializationErrors: promauto.NewCounter(prometheus.CounterOpts{
			Name: "instrumentation_serialization_errors_total",
			Help: "Total event serialization errors",
		}),
		RESTRequestDuration: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "instrumentation_rest_request_duration_seconds",
			Help:    "REST API request duration",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1},
		}),
		GRPCRequestDuration: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "instrumentation_grpc_request_duration_seconds",
			Help:    "gRPC request duration",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1},
		}),
		ValidationErrors: promauto.NewCounter(prometheus.CounterOpts{
			Name: "instrumentation_validation_errors_total",
			Help: "Total event validation errors",
		}),
	}
}
