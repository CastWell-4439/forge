# Python Worker Example

A simple AI task worker built with the Forge Python SDK.

## Handlers

| Handler          | Description                     | Input                                          |
|------------------|---------------------------------|------------------------------------------------|
| `ai.generate`    | Mock AI text generation         | `{"prompt": "...", "model": "mock-llm"}`       |
| `ai.summarize`   | Mock text summarization         | `{"text": "...", "max_length": 100}`           |
| `ai.classify`    | Mock text classification        | `{"text": "...", "categories": ["a", "b"]}`    |

## Running

1. Start the Forge Coordinator:

```bash
# From the project root
./forge coordinator --embed-etcd --db=postgres://forge:forge@localhost:5432/forge --redis=redis://localhost:6379
```

2. Install the Python SDK and run the worker:

```bash
# Install the SDK (from project root)
pip install -e sdk/python

# Run the example worker
cd examples/python-worker
python main.py

# Or with a custom coordinator address:
FORGE_COORDINATOR=coordinator.example.com:8080 python main.py
```

The worker will:
- Register with the Coordinator advertising handlers `ai.generate`, `ai.summarize`, `ai.classify`
- Accept and execute tasks dispatched by the Coordinator
- Respond to heartbeat health checks
- Shut down gracefully on SIGTERM/SIGINT
