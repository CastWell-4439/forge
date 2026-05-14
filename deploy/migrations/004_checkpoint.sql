-- 004_checkpoint.sql: Agent checkpoint persistence for crash recovery.

CREATE TABLE agent_checkpoints (
    id          TEXT PRIMARY KEY,
    session_id  TEXT NOT NULL,
    step_index  INTEGER NOT NULL,
    messages    JSONB NOT NULL,       -- complete conversation history
    metadata    JSONB DEFAULT '{}',   -- extra state (tool results, etc.)
    created_at  TIMESTAMPTZ DEFAULT now(),
    
    CONSTRAINT uq_checkpoint_session_step UNIQUE (session_id, step_index)
);

CREATE INDEX idx_checkpoints_session ON agent_checkpoints (session_id, step_index DESC);
