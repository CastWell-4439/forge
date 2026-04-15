"""Decorator for registering task handlers with the Forge Worker."""

import asyncio
import functools
import inspect
from typing import Any, Callable, Dict

# Module-level handler registry. Populated by @task_handler at import time,
# then consumed by Worker.start() to build the dispatch table.
_handler_registry: Dict[str, Callable] = {}


class TaskContext:
    """Context object passed as the first argument to every task handler.

    Provides metadata about the current task execution and utility methods
    for structured logging and progress reporting.
    """

    def __init__(
        self,
        task_id: str,
        workflow_id: str,
        task_name: str,
        handler: str,
        timeout_ms: int,
        worker_id: str,
    ) -> None:
        self._task_id = task_id
        self._workflow_id = workflow_id
        self._task_name = task_name
        self._handler = handler
        self._timeout_ms = timeout_ms
        self._worker_id = worker_id

    @property
    def task_id(self) -> str:
        """Unique task identifier (UUID)."""
        return self._task_id

    @property
    def workflow_id(self) -> str:
        """Parent workflow identifier."""
        return self._workflow_id

    @property
    def task_name(self) -> str:
        """Human-readable task name."""
        return self._task_name

    @property
    def handler(self) -> str:
        """Handler name (e.g., 'ai.generate')."""
        return self._handler

    @property
    def timeout_ms(self) -> int:
        """Task timeout in milliseconds."""
        return self._timeout_ms

    @property
    def timeout_sec(self) -> float:
        """Task timeout in seconds (convenience property)."""
        return self._timeout_ms / 1000.0

    @property
    def worker_id(self) -> str:
        """Executing Worker's ID."""
        return self._worker_id

    def log(self, level: str, message: str, **kwargs: Any) -> None:
        """Structured logging with task context.

        Args:
            level: One of "debug", "info", "warning", "error".
            message: Log message.
            **kwargs: Additional fields included in the log output.
        """
        import logging

        logger = logging.getLogger("forge.worker")
        extra = {
            "task_id": self._task_id,
            "workflow_id": self._workflow_id,
            "handler": self._handler,
            **kwargs,
        }
        log_fn = getattr(logger, level, logger.info)
        log_fn("%s | %s", message, extra)

    def set_progress(self, percentage: int, message: str = "") -> None:
        """Report task progress (for long-running tasks).

        Args:
            percentage: 0-100.
            message: Human-readable progress message.
        """
        self.log("info", f"progress {percentage}%: {message}")


def task_handler(name: str) -> Callable:
    """Decorator to register a function as a Forge task handler.

    The decorated function is added to a module-level registry keyed by *name*.
    When ``Worker.start()`` is called, all registered handlers are collected and
    advertised to the Coordinator.

    Args:
        name: Handler identifier (e.g., ``"ai.generate"``).

    Usage::

        @task_handler("ai.generate")
        def handle_ai_generate(ctx: TaskContext, params: dict) -> dict:
            return {"output": "..."}

    The function signature must be ``(ctx: TaskContext, params: dict) -> dict``.
    Both sync and async functions are supported.
    """

    def decorator(func: Callable) -> Callable:
        if name in _handler_registry:
            raise ValueError(f"handler '{name}' is already registered")

        @functools.wraps(func)
        def wrapper(ctx: TaskContext, params: Dict[str, Any]) -> Dict[str, Any]:
            if inspect.iscoroutinefunction(func):
                return asyncio.run(func(ctx, params))
            return func(ctx, params)

        _handler_registry[name] = wrapper
        return func  # return the original so it stays usable without the SDK

    return decorator


def get_registered_handlers() -> Dict[str, Callable]:
    """Return a shallow copy of the module-level handler registry."""
    return dict(_handler_registry)


def clear_registry() -> None:
    """Clear all registered handlers. Primarily for testing."""
    _handler_registry.clear()
