"""Forge Python Worker SDK.

Provides the Worker class and @task_handler decorator for building
Forge task workers in Python.

Usage::

    from forge_sdk import Worker, task_handler

    @task_handler("my.handler")
    def handle(ctx, params):
        return {"result": "ok"}

    worker = Worker(coordinator="localhost:8080")
    worker.start()
"""

from .decorators import TaskContext, task_handler
from .worker import Worker

__all__ = ["Worker", "task_handler", "TaskContext"]
__version__ = "0.1.0"
