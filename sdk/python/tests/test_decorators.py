"""Tests for the @task_handler decorator and TaskContext."""

import pytest

from forge_sdk.decorators import (
    TaskContext,
    clear_registry,
    get_registered_handlers,
    task_handler,
)


@pytest.fixture(autouse=True)
def _clean_registry():
    """Clear the handler registry before and after each test."""
    clear_registry()
    yield
    clear_registry()


class TestTaskHandler:
    """Tests for the @task_handler decorator."""

    def test_register_sync_handler(self):
        @task_handler("test.sync")
        def my_handler(ctx, params):
            return {"ok": True}

        handlers = get_registered_handlers()
        assert "test.sync" in handlers

    def test_register_async_handler(self):
        @task_handler("test.async")
        async def my_async_handler(ctx, params):
            return {"ok": True}

        handlers = get_registered_handlers()
        assert "test.async" in handlers

    def test_duplicate_handler_raises(self):
        @task_handler("test.dup")
        def handler_a(ctx, params):
            return {}

        with pytest.raises(ValueError, match="already registered"):
            @task_handler("test.dup")
            def handler_b(ctx, params):
                return {}

    def test_handler_execution_sync(self):
        @task_handler("test.exec")
        def my_handler(ctx, params):
            return {"doubled": params["value"] * 2}

        handlers = get_registered_handlers()
        ctx = TaskContext(
            task_id="t-1",
            workflow_id="w-1",
            task_name="test",
            handler="test.exec",
            timeout_ms=5000,
            worker_id="worker-1",
        )
        result = handlers["test.exec"](ctx, {"value": 21})
        assert result == {"doubled": 42}

    def test_handler_execution_async(self):
        @task_handler("test.async_exec")
        async def my_async_handler(ctx, params):
            return {"greeting": f"hello {params['name']}"}

        handlers = get_registered_handlers()
        ctx = TaskContext(
            task_id="t-2",
            workflow_id="w-2",
            task_name="test",
            handler="test.async_exec",
            timeout_ms=5000,
            worker_id="worker-1",
        )
        result = handlers["test.async_exec"](ctx, {"name": "forge"})
        assert result == {"greeting": "hello forge"}

    def test_handler_exception_propagates(self):
        @task_handler("test.fail")
        def my_handler(ctx, params):
            raise ValueError("intentional failure")

        handlers = get_registered_handlers()
        ctx = TaskContext(
            task_id="t-3",
            workflow_id="w-3",
            task_name="test",
            handler="test.fail",
            timeout_ms=5000,
            worker_id="worker-1",
        )
        with pytest.raises(ValueError, match="intentional failure"):
            handlers["test.fail"](ctx, {})

    def test_original_function_still_callable(self):
        """The decorator should return the original function, not the wrapper."""

        @task_handler("test.original")
        def my_handler(ctx, params):
            return {"direct": True}

        ctx = TaskContext(
            task_id="t-4",
            workflow_id="w-4",
            task_name="test",
            handler="test.original",
            timeout_ms=5000,
            worker_id="worker-1",
        )
        # Calling the original directly (not through the registry) should work
        result = my_handler(ctx, {})
        assert result == {"direct": True}

    def test_clear_registry(self):
        @task_handler("test.clear")
        def my_handler(ctx, params):
            return {}

        assert len(get_registered_handlers()) == 1
        clear_registry()
        assert len(get_registered_handlers()) == 0

    def test_multiple_handlers(self):
        @task_handler("handler.a")
        def a(ctx, params):
            return {"a": True}

        @task_handler("handler.b")
        def b(ctx, params):
            return {"b": True}

        @task_handler("handler.c")
        def c(ctx, params):
            return {"c": True}

        handlers = get_registered_handlers()
        assert set(handlers.keys()) == {"handler.a", "handler.b", "handler.c"}


class TestTaskContext:
    """Tests for the TaskContext class."""

    def test_properties(self):
        ctx = TaskContext(
            task_id="task-123",
            workflow_id="wf-456",
            task_name="my-task",
            handler="ai.generate",
            timeout_ms=30000,
            worker_id="worker-789",
        )
        assert ctx.task_id == "task-123"
        assert ctx.workflow_id == "wf-456"
        assert ctx.task_name == "my-task"
        assert ctx.handler == "ai.generate"
        assert ctx.timeout_ms == 30000
        assert ctx.timeout_sec == 30.0
        assert ctx.worker_id == "worker-789"

    def test_timeout_sec_precision(self):
        ctx = TaskContext(
            task_id="t", workflow_id="w", task_name="n",
            handler="h", timeout_ms=1500, worker_id="wk",
        )
        assert ctx.timeout_sec == 1.5

    def test_log_does_not_raise(self):
        """Logging should never raise even without a configured logger."""
        ctx = TaskContext(
            task_id="t", workflow_id="w", task_name="n",
            handler="h", timeout_ms=1000, worker_id="wk",
        )
        # Should not raise
        ctx.log("info", "test message", extra_key="value")
        ctx.log("debug", "debug message")
        ctx.log("error", "error message")

    def test_set_progress_does_not_raise(self):
        ctx = TaskContext(
            task_id="t", workflow_id="w", task_name="n",
            handler="h", timeout_ms=1000, worker_id="wk",
        )
        ctx.set_progress(0, "starting")
        ctx.set_progress(50, "halfway")
        ctx.set_progress(100, "done")
