-- Migration 006: HITL (Human-In-The-Loop) requests table.
-- Part of Forge V2 HITL Manager (Phase V2-3).

CREATE TABLE IF NOT EXISTS hitl_requests (
    id          VARCHAR(128) PRIMARY KEY,
    workflow_id VARCHAR(128) NOT NULL,
    task_id     VARCHAR(128) NOT NULL,
    message     TEXT         NOT NULL,
    options     JSONB        NOT NULL DEFAULT '[]',
    status      VARCHAR(32)  NOT NULL DEFAULT 'pending',
    response    JSONB,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    timeout_at  TIMESTAMPTZ  NOT NULL,
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_hitl_requests_status ON hitl_requests (status);
CREATE INDEX IF NOT EXISTS idx_hitl_requests_workflow ON hitl_requests (workflow_id);
CREATE INDEX IF NOT EXISTS idx_hitl_requests_timeout ON hitl_requests (timeout_at)
    WHERE status = 'pending';
