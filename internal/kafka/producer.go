/*
Package kafka wraps the Sarama library to provide a high-level Kafka producer
for publishing batches of telemetry events.

WHY this exists: Rather than scattering Sarama configuration throughout the
codebase, this package encapsulates all Kafka-specific logic behind a clean
interface. The rest of the application only sees "SendBatch(events)".

Design choice: We use a SyncProducer (not AsyncProducer) because we need to
know immediately whether the batch was successfully published. The batcher
only considers a flush successful if Kafka acknowledged every message.
*/
package kafka

import (
	"encoding/json" // json.Marshal converts Go structs to JSON byte slices
	"fmt"           // fmt.Errorf for error wrapping

	"github.com/IBM/sarama"                                          // sarama is the most popular Go client library for Apache Kafka (maintained by IBM)
	"github.com/cipheraxat/instrumentation-service/internal/config"  // typed config for Kafka brokers and topic
	"github.com/cipheraxat/instrumentation-service/internal/metrics" // Prometheus metrics for observability
	"github.com/cipheraxat/instrumentation-service/internal/model"   // TelemetryEvent domain model
)

// Producer wraps a Sarama sync producer for reliable batch delivery.
// It sends events as JSON-encoded messages, partitioned by Source field.
type Producer struct {
	producer sarama.SyncProducer // The underlying Sarama sync producer — blocks until Kafka acknowledges
	topic    string              // The Kafka topic to publish to
	metrics  *metrics.Metrics    // Shared Prometheus metrics
}

// NewProducer creates a Kafka producer with production-ready settings.
// Returns an error if the connection to the Kafka brokers fails.
func NewProducer(cfg config.KafkaConfig, m *metrics.Metrics) (*Producer, error) {
	// Go learning note: Sarama uses a config object pattern. You create
	// a default config, then override the fields you care about.
	sc := sarama.NewConfig()
	sc.Producer.Return.Successes = true                 // Required for SyncProducer — blocks until ack
	sc.Producer.RequiredAcks = sarama.WaitForAll        // Wait for ALL in-sync replicas to acknowledge (strongest durability)
	sc.Producer.Retry.Max = 3                           // Retry up to 3 times on transient Kafka errors
	sc.Producer.Partitioner = sarama.NewHashPartitioner // Hash the message key to determine partition (ensures same Source goes to same partition)

	// Go learning note: sarama.NewSyncProducer establishes TCP connections to
	// the Kafka brokers. If brokers are unreachable, this returns an error.
	p, err := sarama.NewSyncProducer(cfg.Brokers, sc)
	if err != nil {
		// Go learning note: fmt.Errorf("context: %w", err) wraps the original
		// error with additional context while preserving the error chain.
		return nil, fmt.Errorf("creating kafka producer: %w", err)
	}

	return &Producer{producer: p, topic: cfg.Topic, metrics: m}, nil
}

// SendBatch publishes a slice of events to the configured Kafka topic.
// Events are partitioned by Source for ordered processing per service.
//
// Design note: We use SendMessages (batch API) instead of sending one message
// at a time. This is much more efficient because Sarama can pipeline multiple
// messages in a single network round-trip.
func (p *Producer) SendBatch(events []model.TelemetryEvent) error {
	// Go learning note: make(slice, 0, len) creates a slice with length 0
	// but pre-allocates capacity. This avoids repeated memory allocations
	// as we append to the slice in the loop.
	msgs := make([]*sarama.ProducerMessage, 0, len(events))

	for _, e := range events {
		// Go learning note: `for _, e := range events` iterates over the slice.
		// The first value (index) is discarded with `_`, and `e` is a COPY of
		// each element. If you need to modify elements in place, use `for i := range`.
		data, err := json.Marshal(e)
		if err != nil {
			// Log the serialization error and skip this event rather than
			// failing the entire batch — one bad event shouldn't block others.
			p.metrics.SerializationErrors.Inc()
			continue
		}
		// sarama.StringEncoder and ByteEncoder are adapter types that implement
		// the sarama.Encoder interface. The Key determines which partition the
		// message lands on (via hash partitioning).
		msgs = append(msgs, &sarama.ProducerMessage{
			Topic: p.topic,
			Key:   sarama.StringEncoder(e.Source),
			Value: sarama.ByteEncoder(data),
		})
	}

	if err := p.producer.SendMessages(msgs); err != nil {
		p.metrics.KafkaSendErrors.Inc()
		return fmt.Errorf("sending batch to kafka: %w", err)
	}

	p.metrics.KafkaMessagesSent.Add(float64(len(msgs)))
	return nil
}

// Close gracefully shuts down the Kafka producer, flushing any pending messages.
func (p *Producer) Close() error {
	return p.producer.Close()
}
