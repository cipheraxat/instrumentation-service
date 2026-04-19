/*
Package config loads application configuration from environment variables.

This package exists to centralise all configuration in one place. Rather than
scattering os.Getenv() calls throughout the codebase, every component receives
a strongly-typed config struct. This makes the application "12-Factor App"
compliant (https://12factor.net/config) — config comes from the environment,
not from files checked into source control.

Go learning note: Go does not have a built-in config framework like Viper or
Spring Boot's @Value. Many Go projects start with a simple pattern like this
and only add Viper if they need YAML/TOML file support.
*/
package config

import (
	"fmt"     // fmt.Errorf is used for wrapping errors with context
	"os"      // os.Getenv reads environment variables from the process
	"strconv" // strconv.Atoi converts string → int ("1000" → 1000)
	"strings" // strings.Split breaks comma-separated broker lists into slices
	"time"    // time.Duration represents time intervals (e.g., 5s, 100ms)
)

// Config is the root configuration struct for the entire application.
// Each nested struct groups related settings for a specific subsystem.
//
// Go learning note: In Go, struct fields that start with an uppercase letter
// are "exported" (public). Lowercase fields are unexported (private to the package).
type Config struct {
	REST     RESTConfig     // HTTP REST API settings
	GRPC     GRPCConfig     // gRPC API settings
	Kafka    KafkaConfig    // Kafka producer connection settings
	Postgres PostgresConfig // PostgreSQL connection settings
	Batcher  BatcherConfig  // Event batching behaviour
	Metrics  MetricsConfig  // Prometheus metrics endpoint settings
}

// RESTConfig configures the REST HTTP server.
type RESTConfig struct {
	Addr string // TCP address to bind to, e.g., ":8080" means all interfaces on port 8080
}

// GRPCConfig configures the gRPC server.
type GRPCConfig struct {
	Addr string // TCP address for the gRPC listener, e.g., ":9090"
}

// KafkaConfig holds Kafka producer connection details.
type KafkaConfig struct {
	Brokers []string // List of Kafka broker addresses, e.g., ["kafka:9092"]
	Topic   string   // The Kafka topic to publish telemetry events to
}

// PostgresConfig holds the PostgreSQL connection string.
type PostgresConfig struct {
	DSN string // Data Source Name — a connection URL like "postgres://user:pass@host:5432/db"
}

// BatcherConfig controls how the in-memory batcher flushes events.
type BatcherConfig struct {
	MaxSize       int           // Flush when the buffer reaches this many events (flush-on-threshold)
	FlushInterval time.Duration // Flush at least this often, even if the buffer isn't full (flush-on-interval)
}

// MetricsConfig configures the Prometheus metrics HTTP endpoint.
type MetricsConfig struct {
	Addr string // TCP address for the metrics server, e.g., ":2112"
}

// Load reads configuration from environment variables and returns a Config.
// Each variable has a sensible default for local development.
//
// Go learning note: Returning (*Config, error) is the standard Go pattern —
// if error is non-nil, the caller should NOT use the Config value.
func Load() (*Config, error) {
	// Go learning note: strconv.Atoi converts a string to int. It returns
	// (int, error) because the string might not be a valid number.
	maxSize, err := strconv.Atoi(getEnv("BATCHER_MAX_SIZE", "1000"))
	if err != nil {
		// Go learning note: fmt.Errorf with %w "wraps" the original error.
		// This lets callers use errors.Is() or errors.As() to inspect the
		// wrapped error chain. Always use %w (not %v) when wrapping errors.
		return nil, fmt.Errorf("invalid BATCHER_MAX_SIZE: %w", err)
	}

	flushMs, err := strconv.Atoi(getEnv("BATCHER_FLUSH_INTERVAL_MS", "5000"))
	if err != nil {
		return nil, fmt.Errorf("invalid BATCHER_FLUSH_INTERVAL_MS: %w", err)
	}

	// Go learning note: time.Duration(flushMs) * time.Millisecond converts
	// an integer millisecond value into Go's time.Duration type. Durations
	// in Go are stored as nanoseconds internally, and you multiply by a
	// unit constant to get the correct scale.
	return &Config{
		REST: RESTConfig{
			Addr: getEnv("REST_ADDR", ":8080"),
		},
		GRPC: GRPCConfig{
			Addr: getEnv("GRPC_ADDR", ":9090"),
		},
		Kafka: KafkaConfig{
			Brokers: strings.Split(getEnv("KAFKA_BROKERS", "localhost:9092"), ","),
			Topic:   getEnv("KAFKA_TOPIC", "telemetry-events"),
		},
		Postgres: PostgresConfig{
			DSN: getEnv("POSTGRES_DSN", "postgres://postgres:postgres@localhost:5432/instrumentation?sslmode=require"),
		},
		Batcher: BatcherConfig{
			MaxSize:       maxSize,
			FlushInterval: time.Duration(flushMs) * time.Millisecond,
		},
		Metrics: MetricsConfig{
			Addr: getEnv("METRICS_ADDR", ":2112"),
		},
	}, nil
}

// getEnv reads an environment variable, returning fallback if the variable
// is empty or unset. This is a common Go pattern for config defaults.
//
// Go learning note: `:=` is Go's short variable declaration — it infers the
// type from the right-hand side. `if v := ...; v != "" { }` is an "init
// statement" in an if block — v is scoped to the if/else block only.
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
