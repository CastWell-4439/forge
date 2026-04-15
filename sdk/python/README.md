# Forge Python Worker SDK

Python SDK for building [Forge](https://github.com/castwell/forge) task workers.

## Installation

```bash
pip install forge-sdk
```

Or install from source:

```bash
cd sdk/python
pip install -e .
```

## Quick Start

```python
from forge_sdk import Worker, task_handler

@task_handler("ai.generate")
def handle_ai_generate(ctx, params):
    """Handle an AI generation task."""
    prompt = params["prompt"]
    # Your AI logic here...
    return {"output": f"Response to: {prompt}"}

@task_handler("data.process")
def handle_data_process(ctx, params):
    """Handle a data processing task."""
    ctx.log("info", "Processing data", rows=params.get("rows", 0))
    return {"processed": True}

# Create and start the worker
worker = Worker(
    coordinator="localhost:8080",
    capacity=5,
    labels={"type": "ai", "gpu": "true"},
)
worker.start()
```

## API Reference

### `Worker`

Main entry point. Connects to a Forge Coordinator, registers available handlers,
and serves task execution requests via gRPC.

```python
Worker(
    coordinator="host:port",   # Coordinator gRPC address
    worker_id=None,            # Auto-generated UUID if omitted
    capacity=5,                # Max concurrent tasks
    labels={},                 # Scheduling labels
    listen_port=0,             # 0 = auto-pick
)
```

**Methods:**
- `start()` — Start the worker (blocking). Registers with Coordinator and serves RPCs.
- `stop()` — Signal graceful shutdown.
- `register_handler(name, func)` — Manually register a handler (alternative to decorator).

### `@task_handler(name)`

Decorator that registers a function as a Forge task handler.

```python
@task_handler("handler.name")
def my_handler(ctx, params):
    # ctx: TaskContext — task metadata and utilities
    # params: dict — decoded JSON input
    return {"key": "value"}  # returned as JSON output
```

### `TaskContext`

Passed as the first argument to every handler. Provides task metadata.

**Properties:** `task_id`, `workflow_id`, `task_name`, `handler`, `timeout_ms`, `timeout_sec`, `worker_id`

**Methods:**
- `log(level, message, **kwargs)` — Structured logging with task context.
- `set_progress(percentage, message)` — Report progress (0-100).

## Development

```bash
# Install with test dependencies
pip install -e '.[test]'

# Run tests
pytest
```
