-- Forge Phase 1B: Initial schema migration
-- Tables from tech spec section 6.1

-- 工作流定义
CREATE TABLE workflow_definitions (
    id          BIGSERIAL PRIMARY KEY,
    name        VARCHAR(255) NOT NULL,
    version     INT NOT NULL DEFAULT 1,
    dag_yaml    JSONB NOT NULL,
    created_at  TIMESTAMPTZ DEFAULT NOW(),
    updated_at  TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(name, version)
);

-- 工作流实例（每次执行一条记录）
CREATE TABLE workflow_instances (
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
CREATE INDEX idx_wf_status ON workflow_instances(status);
CREATE INDEX idx_wf_created ON workflow_instances(created_at);

-- 任务实例
CREATE TABLE task_instances (
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
CREATE INDEX idx_task_wf ON task_instances(workflow_id);
CREATE INDEX idx_task_status ON task_instances(status);
CREATE INDEX idx_task_worker ON task_instances(worker_id);
CREATE INDEX idx_task_claim ON task_instances(status, handler) WHERE status = 'READY';

-- 事件日志（事件溯源）
CREATE TABLE events (
    id              BIGSERIAL PRIMARY KEY,
    workflow_id     VARCHAR(36) NOT NULL,
    task_id         VARCHAR(36),
    event_type      VARCHAR(50) NOT NULL,
    payload         JSONB,
    sequence_num    BIGINT NOT NULL,
    created_at      TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX idx_event_wf ON events(workflow_id, sequence_num);

-- Cron 触发器
CREATE TABLE cron_triggers (
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
