-- Migration 007: loop iteration counter on tasks.
--
-- A task that declares `loop.max_iterations` may be revisited by an
-- `on_result: goto` route. The counter has to be durable: it is the only thing
-- standing between a goto and an unbounded loop, so holding it in memory would
-- mean a coordinator restart silently resets the budget.
--
-- Default 0 reads as "never revisited", which is what every task that predates
-- this column is.

ALTER TABLE task_instances
    ADD COLUMN IF NOT EXISTS loop_iteration INT NOT NULL DEFAULT 0;