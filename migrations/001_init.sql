CREATE TABLE IF NOT EXISTS telemetry_events (
    event_id    TEXT PRIMARY KEY,
    event_type  TEXT NOT NULL,
    source      TEXT NOT NULL,
    timestamp   BIGINT NOT NULL,
    properties  JSONB DEFAULT '{}',
    session_id  TEXT,
    created_at  TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_telemetry_source    ON telemetry_events (source);
CREATE INDEX idx_telemetry_type      ON telemetry_events (event_type);
CREATE INDEX idx_telemetry_timestamp ON telemetry_events (timestamp);
