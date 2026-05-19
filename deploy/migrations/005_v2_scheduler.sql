-- Migration 005: Scheduler trigger checkpoint table for event deduplication.
-- Part of Forge V2 Scheduler (Phase V2-2).

CREATE TABLE IF NOT EXISTS trigger_checkpoints (
    trigger_name VARCHAR(128) NOT NULL,
    event_id     VARCHAR(256) NOT NULL,
    processed_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    PRIMARY KEY (trigger_name, event_id)
);

-- Index for GC: cleanup old entries periodically
CREATE INDEX IF NOT EXISTS idx_trigger_checkpoints_processed_at
    ON trigger_checkpoints (processed_at);

-- Optional: auto-cleanup entries older than 30 days
-- (Can be run via pg_cron or application-level GC)
-- DELETE FROM trigger_checkpoints WHERE processed_at < NOW() - INTERVAL '30 days';
