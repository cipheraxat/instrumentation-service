/*
Package storage implements the PostgreSQL persistence layer for telemetry events.

WHY this exists: While Kafka is the primary data path for downstream consumers,
PostgreSQL serves as a durable store for querying historical events, debugging,
and providing a backup if Kafka messages need to be replayed.

Design note: We use ON CONFLICT (event_id) DO NOTHING for idempotent writes.
This means re-inserting the same event (e.g., on retry) is safe — it won't
create duplicates or error out.
*/
package storage

import (
	"database/sql"  // database/sql is Go's standard database interface (like JDBC in Java)
	"encoding/json" // json.Marshal converts Go maps to JSON for the properties column
	"fmt"           // fmt.Errorf for error wrapping

	// Go learning note: The underscore import `_ "github.com/lib/pq"` is a
	// "side-effect only" import. The pq package registers itself as a PostgreSQL
	// driver with database/sql via its init() function. We never call pq directly
	// — we only use the standard database/sql interface. This is Go's driver pattern.
	_ "github.com/lib/pq" // PostgreSQL driver — registers itself with database/sql via init()

	"github.com/cipheraxat/instrumentation-service/internal/config" // PostgresConfig with DSN
	"github.com/cipheraxat/instrumentation-service/internal/model"  // TelemetryEvent domain type
)

// Postgres wraps a *sql.DB connection pool.
//
// Go learning note: database/sql.DB is NOT a single connection — it's a
// CONNECTION POOL. It manages multiple connections internally, reusing idle
// ones and creating new ones as needed (up to SetMaxOpenConns).
type Postgres struct {
	db *sql.DB // Connection pool to PostgreSQL
}

// NewPostgres opens a connection pool to PostgreSQL, pings it to verify
// connectivity, and configures pool limits.
func NewPostgres(cfg config.PostgresConfig) (*Postgres, error) {
	// Go learning note: sql.Open does NOT actually connect to the database!
	// It only validates the DSN format. The actual TCP connection happens
	// on the first query or when we call db.Ping().
	db, err := sql.Open("postgres", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("opening postgres connection: %w", err)
	}

	// Ping actually establishes a connection and verifies the database is reachable.
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("pinging postgres: %w", err)
	}

	// Configure the connection pool. These are important production settings:
	db.SetMaxOpenConns(25) // Max simultaneous connections (prevents overwhelming the DB)
	db.SetMaxIdleConns(10) // Keep 10 connections warm for quick reuse

	return &Postgres{db: db}, nil
}

// InsertBatch persists a batch of events in a single transaction.
// Uses ON CONFLICT DO NOTHING for idempotent writes.
//
// Go learning note: A database TRANSACTION groups multiple operations so they
// all succeed or all fail together (atomicity). If any insert fails, we
// Rollback the entire batch. tx.Commit() makes all changes permanent.
func (p *Postgres) InsertBatch(events []model.TelemetryEvent) error {
	// Begin a new transaction — all inserts within will be atomic.
	tx, err := p.db.Begin()
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}

	// Go learning note: tx.Prepare creates a "prepared statement" — the SQL
	// is compiled once by the database and reused for each event. This is
	// more efficient than sending the full SQL text for every insert.
	// $1, $2, etc. are PostgreSQL parameter placeholders (prevents SQL injection).
	stmt, err := tx.Prepare(`
		INSERT INTO telemetry_events (event_id, event_type, source, timestamp, properties, session_id)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (event_id) DO NOTHING
	`)
	if err != nil {
		// Go learning note: `_ = tx.Rollback()` discards the Rollback error.
		// If Prepare failed, the transaction is likely already broken, so
		// there's not much we can do if Rollback also fails.
		_ = tx.Rollback()
		return fmt.Errorf("preparing statement: %w", err)
	}
	// Go learning note: `defer stmt.Close()` ensures the prepared statement's
	// resources are freed when InsertBatch returns.
	defer stmt.Close()

	// Insert each event using the prepared statement.
	for _, e := range events {
		// Convert the Properties map to a JSON string for the JSONB column.
		props, err := json.Marshal(e.Properties)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("marshaling properties for event %s: %w", e.EventID, err)
		}
		if _, err := stmt.Exec(e.EventID, e.EventType, e.Source, e.Timestamp, props, e.SessionID); err != nil {
			// Any single insert failure rolls back ALL inserts in the batch.
			_ = tx.Rollback()
			return fmt.Errorf("inserting event %s: %w", e.EventID, err)
		}
	}

	// Go learning note: tx.Commit() makes all the inserts permanent.
	// If the process crashes between Begin and Commit, all inserts are
	// automatically rolled back by PostgreSQL — this is transactional safety.
	return tx.Commit()
}

// Close releases the connection pool. Call this during shutdown (via defer).
func (p *Postgres) Close() error {
	return p.db.Close()
}
