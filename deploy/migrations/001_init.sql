-- Forge Phase 1B: Initial schema migration
-- Tables from tech spec section 6.1

-- 宸ヤ綔娴佸畾涔?
CREATE TABLE IF NOT EXISTS workflow_definitions (
    id          BIGSERIAL PRIMARY KEY,
    name        VARCHAR(255) NOT NULL,
    version     INT NOT NULL DEFAULT 1,
    dag_yaml    JSONB NOT NULL,
    created_at  TIMESTAMPTZ DEFAULT NOW(),
    updated_at  TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(name, version)
);

-- 宸ヤ綔娴佸疄渚嬶紙姣忔鎵ц涓€鏉¤褰曪級
CREATE TABLE IF NOT EXISTS workflow_instances (
    id          VARCHAR(36) PRIMARY KEY,
    def_id      BIGINT REFERENCES workflow_definitions(id),
    name        VARCHAR(255) NOT NULL DEFAULT '',
    status      VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    input       JSONB,
    output      JSONB,
    error_msg   TEXT,
    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ DEFAULT NOW(),
    timeout_at  TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_wf_status ON workflow_instances(status);
CREATE INDEX IF NOT EXISTS idx_wf_created ON workflow_instances(created_at);

-- 浠诲姟瀹炰緥
CREATE TABLE IF NOT EXISTS task_instances (
    id              VARCHAR(36) PRIMARY KEY,
    workflow_id     VARCHAR(36) REFERENCES workflow_instances(id),
    task_name       VARCHAR(255) NOT NULL,
    handler         VARCHAR(255) NOT NULL,
    status          VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    worker_id       VARCHAR(255),
    input           JSONB,
    output          JSONB,
    error_msg       TEXT,
    attempt         INT DEFAULT 0,
    max_attempts    INT DEFAULT 1,
    scheduled_at    TIMESTAMPTZ,
    started_at      TIMESTAMPTZ,
    finished_at     TIMESTAMPTZ,
    timeout_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_task_wf ON task_instances(workflow_id);
CREATE INDEX IF NOT EXISTS idx_task_status ON task_instances(status);
CREATE INDEX IF NOT EXISTS idx_task_worker ON task_instances(worker_id);
CREATE INDEX IF NOT EXISTS idx_task_claim ON task_instances(status, handler) WHERE status = 'READY';

-- 浜嬩欢鏃ュ織锛堜簨浠舵函婧愶級
CREATE TABLE IF NOT EXISTS events (
    id              BIGSERIAL PRIMARY KEY,
    workflow_id     VARCHAR(36) NOT NULL,
    task_id         VARCHAR(36),
    event_type      VARCHAR(50) NOT NULL,
    payload         JSONB,
    sequence_num    BIGINT NOT NULL,
    created_at      TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_event_wf ON events(workflow_id, sequence_num);

-- Cron 瑙﹀彂鍣?
CREATE TABLE IF NOT EXISTS cron_triggers (
    id              BIGSERIAL PRIMARY KEY,
    workflow_name   VARCHAR(255) NOT NULL,
    cron_expr       VARCHAR(100) NOT NULL,
    params          JSONB,
    max_concurrent  INT DEFAULT 1,
    enabled         BOOLEAN DEFAULT TRUE,
    last_fire_at    TIMESTAMPTZ,
    next_fire_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ DEFAULT NOW()
);
