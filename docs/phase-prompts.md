# Forge — Phase Prompts

Each Phase below is a self-contained prompt. Feed ONE Phase at a time to Claude Code.

**Context injection**: Before each Phase, ensure these files are accessible in the project directory:
- `CLAUDE.md` (this project's coding conventions — should be auto-read)
- `分布式任务调度引擎-技术方案.md` (tech spec — inject as context or place in `docs/`)
- `Forge-AI编码实施方案.md` (implementation plan — inject as context or place in `docs/`)

---

## Phase 1A — Project Skeleton + Proto + DAG Engine

```
You are building Forge, a distributed task scheduling engine.

Read CLAUDE.md for project conventions and constraints.

This is Phase 1A. Complete these tasks IN ORDER:

TASK 1A.1: Initialize Go module and directory structure
- Run: go mod init github.com/castwell/forge
- Create the FULL directory tree from CLAUDE.md "Directory Structure" section
- Add .gitignore (Go + Node + Python + C++ patterns)
- git init + first commit

TASK 1A.2: Create Makefile
- Targets: build, test, lint, proto, clean
- "proto" target runs: buf generate
- "lint" target runs: golangci-lint run
- Add .golangci.yml with reasonable defaults

TASK 1A.3: Write common.proto
- Package: forge.v1
- Messages: TaskStatus enum (PENDING/READY/SCHEDULED/RUNNING/COMPLETED/FAILED/SKIPPED/COMPENSATING), WorkflowStatus enum (PENDING/RUNNING/COMPLETED/FAILED/CANCELLED/COMPENSATING), Error message
- Reference: tech spec section 5.4 for EventType and status values, section 6.1 for status values in SQL comments

TASK 1A.4: Write coordinator.proto
- Package: forge.v1
- Service CoordinatorService: SubmitWorkflow, GetWorkflow, ListWorkflows, CancelWorkflow
- Request/Response messages for each RPC
- Reference: tech spec section 4.3 for the workflow lifecycle

TASK 1A.5: Write worker.proto
- Package: forge.v1
- Service WorkerService: Register, ExecuteTask
- Bidirectional streaming RPC for Heartbeat (HeartbeatPing/HeartbeatPong)
- WorkerRegistration message with: ID, Addr, Labels (map), Capacity, Handlers (repeated string)
- Reference: tech spec section 5.3 for WorkerRegistration struct

TASK 1A.6: Configure buf and generate Go code
- Create buf.yaml (v2 config) and buf.gen.yaml
- Plugins: protoc-gen-go + protoc-gen-go-grpc
- Run buf generate, verify generated code compiles
- Run: go build ./...

TASK 1A.7: Implement DAG engine
- File: internal/coordinator/dag.go
- Structs: DAG, TaskDef, RetryPolicy — copy EXACTLY from tech spec section 5.1
- YAML parsing: support the video-production YAML example from section 5.1
- Validation: cycle detection (Kahn's algorithm from section 7.1), orphan node detection, timeout sanity check (task timeout < workflow timeout)
- TopologicalSort method returning ordered task names
- File: internal/coordinator/dag_test.go
- Tests: valid linear DAG, valid fan-out DAG, cyclic DAG error, orphan node error, YAML parse round-trip

After all tasks: run `go build ./...` and `go test ./...`. Both must pass.
Commit each task separately with message format: feat(module): description

Output the Phase 1A technical report per the template in Forge-AI编码实施方案.md.
```

---

## Phase 1B — Storage Layer + Basic Coordinator + Worker

```
You are continuing Forge development. Read CLAUDE.md.

This is Phase 1B. Prerequisites: Phase 1A is complete. Proto files exist, DAG engine works.

TASK 1B.1: Define Storage interface
- File: internal/storage/interface.go
- Copy the Storage interface EXACTLY from tech spec section 3.4
- Methods: SaveWorkflow, SaveTask, ClaimTask, UpdateTaskStatus, GetWorkflowHistory
- Add additional CRUD methods needed for workflow lifecycle: GetWorkflow, ListWorkflows, GetTask, ListTasksByWorkflow, SaveWorkflowDefinition

TASK 1B.2: Write SQL migration
- File: deploy/migrations/001_init.sql
- Copy ALL table definitions from tech spec section 6.1 EXACTLY: workflow_definitions, workflow_instances, task_instances, events, cron_triggers
- Include ALL indexes from section 6.1

TASK 1B.3: Implement PostgreSQL Storage
- File: internal/storage/postgres.go
- Use pgx/v5 driver
- Implement full Storage interface
- ClaimTask MUST use FOR UPDATE SKIP LOCKED — copy the exact query from tech spec section 7.3
- Include connection pool setup and graceful shutdown

TASK 1B.4: Implement BoltDB Storage
- File: internal/storage/boltdb.go
- Implement Storage interface using go.etcd.io/bbolt
- Bucket layout: "workflows", "tasks", "events"
- This is a lightweight alternative for standalone/dev mode

TASK 1B.5: Implement basic Coordinator
- File: internal/coordinator/coordinator.go
- Core loop: receive workflow submission → parse DAG → create task instances in DB → find ready tasks (in-degree 0) → assign to worker → on task completion, check successors → mark workflow complete when all tasks done
- Workflow state machine: PENDING → RUNNING → COMPLETED/FAILED
- Task state machine: PENDING → READY → SCHEDULED → RUNNING → COMPLETED/FAILED
- Reference: tech spec section 4.3 interaction flow

TASK 1B.6: Implement basic Go Worker
- Files: internal/worker/worker.go, executor.go, handler.go
- Worker connects to Coordinator via gRPC
- Handler registry: map[string]HandlerFunc where HandlerFunc = func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error)
- Worker pulls/receives tasks, executes matching handler, returns result
- Reference: tech spec section 5.3

TASK 1B.7: Implement CLI entry point
- File: cmd/forge/main.go
- Use cobra for CLI framework
- Subcommands: "coordinator" (start coordinator), "worker" (start worker with --coordinator flag), "standalone" (both in one process)
- Config flags: --db (postgres DSN), --redis (redis URL), --listen (gRPC port, default 8080)
- Reference: tech spec section 9.2

TASK 1B.8: Write integration test
- File: test/integration_test.go
- Test: start Coordinator + Worker in-process, submit a 3-task linear DAG (A→B→C) with simple echo handlers, verify all tasks complete in order, workflow status becomes COMPLETED
- Use testcontainers-go for PostgreSQL

After all tasks: `go build ./...` and `go test ./...` must pass.

Output the Phase 1B technical report. Include the end-to-end test result.
```

---

## Phase 2A — Service Discovery + Leader Election + Worker Management

```
You are continuing Forge development. Read CLAUDE.md.

This is Phase 2A. Prerequisites: Phase 1B complete. Single-node Coordinator + Worker works.

TASK 2A.1: Define discovery interface
- File: internal/discovery/interface.go
- Copy the Coordinator interface from tech spec section 3.3 EXACTLY:
  LeaderElect(ctx) (<-chan bool, error)
  Register(ctx, node NodeInfo) error
  Watch(ctx, prefix string) (<-chan Event, error)
  Lock(ctx, key string) (Unlock func(), error)

TASK 2A.2: Implement embedded etcd discovery
- File: internal/discovery/etcd.go
- Use go.etcd.io/etcd/server/v3/embed for embedded etcd
- Use go.etcd.io/etcd/client/v3 for client operations
- Implement all 4 interface methods
- etcd data dir configurable, defaults to temp dir for dev

TASK 2A.3: Implement Leader election
- File: internal/coordinator/leader.go
- Use etcd Election API (go.etcd.io/etcd/client/v3/concurrency)
- Only the Leader Coordinator processes workflows; Followers are hot standby
- On leader loss, release all scheduling and let new leader take over

TASK 2A.4: Implement Worker registration and discovery
- File: internal/coordinator/worker_manager.go
- Workers register to etcd on startup with WorkerRegistration info
- Coordinator watches etcd prefix "forge/workers/" for add/remove events
- Maintain in-memory worker list with status (ACTIVE/SUSPECT/DEAD)

TASK 2A.5: Implement gRPC bidirectional streaming heartbeat
- File: internal/worker/heartbeat.go
- Update worker.proto if needed (add HeartbeatPing/Pong messages with worker status payload)
- Worker opens bidirectional stream, responds to Coordinator pings with current status (active tasks, capacity, etc.)
- Heartbeat interval: 10 seconds
- Reference: tech spec section 5.3 heartbeat diagram

TASK 2A.6: Implement Worker failure detection
- In worker_manager.go:
- 3 missed pings (30s) → mark SUSPECT
- 60s no response → mark DEAD
- On DEAD: reassign all RUNNING tasks from dead worker to other workers
- Log all state transitions

After all tasks: `go build ./...` and `go test ./...` must pass.

Output the Phase 2A technical report.
```

---

## Phase 2B — Scheduling Algorithms + Retry + Timeout + Event Notification

```
You are continuing Forge development. Read CLAUDE.md.

This is Phase 2B. Prerequisites: Phase 2A complete. etcd, leader election, and heartbeat work.

TASK 2B.1: Define Scheduler interface
- File: internal/coordinator/scheduler.go
- Interface Scheduler { Schedule(task *Task, workers []*WorkerInfo) (*WorkerInfo, error) }
- Copy WorkerInfo struct from tech spec section 5.2 EXACTLY

TASK 2B.2: Implement Weighted Round Robin (WRR)
- File: internal/coordinator/scheduler_wrr.go
- Respect Worker.Weight field
- Skip workers at capacity or with wrong labels

TASK 2B.3: Implement Least Active scheduler
- File: internal/coordinator/scheduler_least.go
- Pick worker with lowest ActiveTasks count
- Tie-break by weight

TASK 2B.4: Implement Consistent Hash scheduler
- File: internal/coordinator/scheduler_hash.go
- Hash key = task handler name
- Same handler type tends to route to same worker (cache locality)
- Use jump consistent hash or hashring

TASK 2B.5: Implement Label Selector (task affinity)
- In scheduler.go: before calling any scheduler algorithm, filter workers by task's matchLabels
- If no worker matches labels, return error (not silently skip)
- Reference: tech spec section 5.2 YAML example with matchLabels

TASK 2B.6: Implement exponential backoff retry with Full Jitter
- File: internal/coordinator/retry.go
- Copy calculateBackoff function from tech spec section 7.2 EXACTLY
- Support RetryPolicy: FIXED, EXPONENTIAL, EXPONENTIAL_WITH_JITTER
- After max_attempts exhausted: mark task FAILED, check workflow failure policy
- Dead letter: log failed tasks for manual inspection

TASK 2B.7: Implement timeout control + PG LISTEN/NOTIFY
- File: internal/coordinator/timeout.go
  - Task-level timeout: context with deadline per task execution
  - Workflow-level timeout: background goroutine checks workflow timeout_at
  - On timeout: cancel task, trigger retry or fail
- File: internal/bus/pg_notify.go
  - Use PG LISTEN/NOTIFY on channel "forge_tasks_ready"
  - When new tasks become READY, NOTIFY
  - Coordinator listens and wakes up scheduler immediately (instead of polling)

After all tasks: `go build ./...` and `go test ./...` must pass.

Output the Phase 2B technical report.
```

---

## Phase 3A — Python Worker SDK

```
You are continuing Forge development. Read CLAUDE.md.

This is Phase 3A. Prerequisites: Phase 2B complete. Distributed Coordinator + Go Worker works.

TASK 3A.1: Configure buf for Python code generation
- Update buf.gen.yaml: add grpcio-tools plugin for Python
- Output to: sdk/python/forge_sdk/generated/
- Run buf generate, verify Python files are created

TASK 3A.2: Implement Python Worker class
- File: sdk/python/forge_sdk/worker.py
- Worker class: connects to Coordinator gRPC, auto-registers with labels/handlers/capacity, runs heartbeat loop in background thread
- Reference: tech spec section 3.1 Python Worker example

TASK 3A.3: Implement @task_handler decorator
- File: sdk/python/forge_sdk/decorators.py
- @task_handler("handler.name") registers a function as a handler
- Function signature: def handler(ctx, params: dict) -> dict
- Auto-serialize/deserialize params and results via protobuf
- Reference: tech spec section 3.1 Python example code

TASK 3A.4: Python SDK packaging
- File: sdk/python/pyproject.toml (use hatchling or setuptools)
- Package name: forge-sdk
- Dependencies: grpcio, protobuf
- File: sdk/python/README.md with quick-start example

TASK 3A.5: Python Worker example
- Directory: examples/python-worker/
- Simple AI task handler that takes a prompt and returns a response (mock, no real API call)
- README with run instructions

TASK 3A.6: Python SDK tests
- Directory: sdk/python/tests/
- Use pytest
- Tests: Worker instantiation, handler registration, task execution mock, error handling (exception → FAILED status)

After all tasks: Python tests pass (`cd sdk/python && pip install -e . && pytest`).
Go tests still pass (`go test ./...`).

Output the Phase 3A technical report.
```

---

## Phase 3B — C++ Worker SDK + Multi-Language Integration Test

```
You are continuing Forge development. Read CLAUDE.md.

This is Phase 3B. Prerequisites: Phase 3A complete. Python SDK works.

TASK 3B.1: Configure buf for C++ code generation
- Update buf.gen.yaml: add grpc_cpp_plugin
- Output to: sdk/cpp/generated/
- Run buf generate

TASK 3B.2: Implement C++ Worker SDK
- Files: sdk/cpp/include/forge/worker.h, task_handler.h, task_context.h
- Files: sdk/cpp/src/worker.cpp, grpc_client.cpp
- Worker class: connect to Coordinator, register, heartbeat thread, task receive loop
- TaskHandler abstract base class with virtual execute() method
- Reference: tech spec section 3.1 C++ Worker example and CMakeLists.txt

TASK 3B.3: C++ SDK CMake build + vcpkg
- File: sdk/cpp/CMakeLists.txt — find_package(gRPC), find_package(Protobuf)
- File: sdk/cpp/vcpkg.json — dependencies: grpc, protobuf
- Must compile with CMake 3.20+ and C++20

TASK 3B.4: C++ Worker example
- Directory: examples/cpp-worker/
- VideoRenderHandler that simulates a compute task (sleep + return)
- CMakeLists.txt + README with build and run instructions

TASK 3B.5: Go Worker SDK extraction
- Directory: sdk/go/
- Extract public Worker API from internal/worker/ into sdk/go/ as a separate Go module
- sdk/go/go.mod with module path github.com/castwell/forge/sdk/go
- Thin wrapper: keeps internal/ as the real implementation

TASK 3B.6: Multi-language integration test + docker-compose update
- File: test/multilang_test.go
  - Start Coordinator + Go Worker + Python Worker (subprocess) + C++ Worker (subprocess)
  - Submit 3 tasks: handler "general.echo" (Go), "ai.generate" (Python), "video.render" (C++)
  - Verify each routes to the correct language worker
- Update deploy/docker-compose.yml:
  - Add worker-python and worker-cpp services (reference tech spec section 9.1)

After all tasks: `go build ./...`, `go test ./...`, docker-compose builds all images.

Output the Phase 3B technical report.
```

---

## Phase 4A — Event Sourcing + Saga Compensation

```
You are continuing Forge development. Read CLAUDE.md.

This is Phase 4A. Prerequisites: Phase 3B complete.

TASK 4A.1: Implement Event store
- File: internal/event/store.go
- Event struct: copy EXACTLY from tech spec section 5.4
- EventType constants: copy ALL from section 5.4
- Methods: Append(event), ListByWorkflow(workflowID) ordered by sequence_num
- Store to PG events table (already created in Phase 1B migration)

TASK 4A.2: Implement Event replay
- File: internal/event/replay.go
- ReplayWorkflow function: copy from tech spec section 5.4
- Takes []Event, returns *WorkflowState by applying each event in order
- WorkflowState tracks: workflow status, each task's status, timestamps

TASK 4A.3: Refactor Coordinator to event-driven state changes
- Modify internal/coordinator/coordinator.go
- Every state transition (task scheduled, task started, task completed, etc.) now:
  1. Writes an Event to the event store
  2. Then updates the workflow/task instance status
- This ensures the event log is the source of truth

TASK 4A.4: Add compensate field to DAG definition
- Update internal/coordinator/dag.go
- TaskDef gains: Compensate string (handler name for compensation)
- Update YAML parsing to support "compensate: handler.name"
- Reference: tech spec section 5.5 YAML example

TASK 4A.5: Implement Saga compensator
- File: internal/saga/compensator.go
- On workflow failure: collect all COMPLETED tasks, sort in reverse topological order, execute each task's compensate handler
- Skip tasks with empty Compensate field
- Reference: tech spec section 5.5 — "A → B → C(fail!) → C.compensate → B.compensate → A.compensate"

TASK 4A.6: Write tests
- File: test/event_test.go — event write/read/replay consistency
- File: test/saga_test.go — A→B→C where C fails, verify B.compensate and A.compensate execute in reverse order

After all tasks: `go build ./...` and `go test ./...` must pass.

Output the Phase 4A technical report.
```

---

## Phase 4B — Cron Scheduling + Timing Wheel

```
You are continuing Forge development. Read CLAUDE.md.

This is Phase 4B. Prerequisites: Phase 4A complete.

TASK 4B.1: Implement CronTrigger data structure + persistence
- File: internal/coordinator/cron.go
- CronTrigger struct: copy from tech spec section 5.6 EXACTLY
- CRUD operations using the cron_triggers table (already exists from Phase 1B)

TASK 4B.2: Implement Cron expression parsing + trigger logic
- Use github.com/robfig/cron/v3 for expression parsing
- Trigger loop: check next_fire_at, submit workflow when due, update last_fire_at and next_fire_at
- Respect MaxConcurrent: don't trigger if already at limit
- Support MisfirePolicy: FIRE_ONCE (fire once on recovery), SKIP (skip missed), FIRE_ALL (fire all missed)

TASK 4B.3: Implement distributed Cron deduplication
- Use etcd Lock (from discovery interface) to ensure only one Coordinator fires each trigger
- Reference: tech spec section 5.6 — fire() function with lock pattern

TASK 4B.4: Implement TimingWheel
- File: internal/coordinator/timingwheel.go
- Copy TimingWheel struct from tech spec section 7.4
- Hierarchical: overflow to upper wheel for long delays
- Thread-safe (sync.Mutex)
- Used for task timeout tracking and delayed retry scheduling

TASK 4B.5: Write tests
- File: test/cron_test.go
  - Cron triggers at correct intervals
  - Deduplication: simulate 3 coordinators, verify single trigger per interval
  - MisfirePolicy behavior
- Timing wheel precision test: 100 timers, verify firing within 100ms tolerance

After all tasks: `go build ./...` and `go test ./...` must pass.

Output the Phase 4B technical report.
```

---

## Phase 4C — CDC Engine + Wasm Plugin System

```
You are continuing Forge development. Read CLAUDE.md.

This is Phase 4C. Prerequisites: Phase 4B complete.

TASK 4C.1: Define CDC Source interface
- File: internal/cdc/interface.go
- Interface CDCSource { Subscribe(ctx, handler func(CDCEvent)) error; Close() error }
- CDCEvent struct: Table, Operation (INSERT/UPDATE/DELETE), OldData, NewData
- Reference: tech spec section 5.9

TASK 4C.2: Implement PostgreSQL CDC
- File: internal/cdc/postgres.go
- Use pglogrepl for logical replication
- Create replication slot + publication
- Parse WAL messages into CDCEvent
- Reference: tech spec section 5.9 — PGCDCSource implementation

TASK 4C.3: Implement CDC trigger configuration
- File: internal/cdc/trigger.go
- YAML config: triggers with name, type (cdc), source (postgres/table/events/filter), workflow name, params_mapping
- On matching CDC event: auto-submit the configured workflow with mapped parameters
- Reference: tech spec section 5.9 YAML trigger example

TASK 4C.4: Integrate wazero runtime + sandbox
- File: internal/wasm/executor.go, sandbox.go
- Use github.com/tetratelabs/wazero (pure Go, zero CGO)
- Sandbox: no filesystem access, no network, memory limit, execution timeout (30s default)
- CompilationCache for repeated executions
- Reference: tech spec section 5.7

TASK 4C.5: Implement plugin registry
- File: internal/wasm/registry.go
- Store .wasm bytes in PG (or filesystem for dev)
- Version management: name + version → wasm bytes
- Upload, list, get, delete operations

TASK 4C.6: Support handler:wasm in DAG + example plugin
- Update internal/coordinator/dag.go: recognize handler "wasm" with wasm config block (module path, memory_limit, timeout, permissions)
- When scheduler encounters a wasm task: route to WasmExecutor instead of a Worker
- Example plugin: plugins/transform/ — Go source compiled with tinygo to .wasm, does simple JSON transform

TASK 4C.7: Write tests
- File: test/cdc_test.go — INSERT into PG table triggers workflow (use testcontainers)
- File: test/wasm_test.go — execute .wasm plugin, verify result; test sandbox isolation (no FS access); test timeout enforcement

After all tasks: `go build ./...` and `go test ./...` must pass.

Output the Phase 4C technical report.
```

---

## Phase 5A — Docker Images + Helm Chart

```
You are continuing Forge development. Read CLAUDE.md.

This is Phase 5A. Prerequisites: Phase 4C complete.

TASK 5A.1: Coordinator Dockerfile (multi-stage, <20MB)
- File: build/coordinator/Dockerfile
- Stage 1: golang:1.22-alpine, build with CGO_ENABLED=0
- Stage 2: FROM scratch, copy binary + ca-certificates
- Target: <20MB image

TASK 5A.2: Go Worker Dockerfile
- File: build/worker-go/Dockerfile
- Same pattern as Coordinator

TASK 5A.3: Python Worker Dockerfile
- File: build/worker-python/Dockerfile
- Base: python:3.12-slim
- Install forge-sdk from local sdk/python/

TASK 5A.4: C++ Worker Dockerfile
- File: build/worker-cpp/Dockerfile
- Stage 1: build with CMake + vcpkg
- Stage 2: minimal runtime image

TASK 5A.5: Helm Chart skeleton
- Directory: deploy/helm/forge/
- Chart.yaml, values.yaml (defaults), values-dev.yaml, values-prod.yaml
- _helpers.tpl with standard labels
- Reference: tech spec section 9.3.5

TASK 5A.6: Helm templates — Coordinator StatefulSet
- Files: deploy/helm/forge/templates/coordinator-statefulset.yaml, coordinator-service.yaml, coordinator-pdb.yaml
- Copy StatefulSet spec from tech spec section 9.3.2 and templatize with Helm values
- Headless service for etcd peer communication

TASK 5A.7: Helm templates — Worker Deployments + HPA
- Files: deploy/helm/forge/templates/worker-go-deployment.yaml, worker-python-deployment.yaml, worker-cpp-deployment.yaml, hpa.yaml
- Reference: tech spec section 9.3.3 for HPA with custom metrics
- Python worker: GPU resource request (optional via values)

After all tasks: all Dockerfiles build successfully, `helm lint` passes.

Output the Phase 5A technical report.
```

---

## Phase 5B — Gateway API + Kueue + CI/CD

```
You are continuing Forge development. Read CLAUDE.md.

This is Phase 5B. Prerequisites: Phase 5A complete.

TASK 5B.1: Gateway API configuration
- File: deploy/helm/forge/templates/gateway.yaml
- Gateway + GRPCRoute + HTTPRoute
- Copy from tech spec section 9.3.6 and templatize

TASK 5B.2: Kueue CRD configuration
- Directory: deploy/kueue/
- ResourceFlavor (gpu-a100, cpu-general), ClusterQueue (forge-queue), LocalQueue (forge-tasks)
- Copy from tech spec section 9.3.7

TASK 5B.3: Coordinator Kueue integration
- File: internal/coordinator/kueue.go
- When a task has label "execution: k8s-job" or requires GPU: submit as K8s Job with Kueue queue label
- Copy submitToKueue function from tech spec section 9.3.7
- Use client-go for K8s API calls

TASK 5B.4: GitHub Actions CI
- File: .github/workflows/ci.yml
- Trigger: push + PR to main
- Jobs: lint (golangci-lint), test (go test with PG service container), build (go build)

TASK 5B.5: GitHub Actions Release
- File: .github/workflows/release.yml
- Trigger: tag push v*
- Matrix build: coordinator, worker-go, worker-python, worker-cpp
- Multi-arch: linux/amd64 + linux/arm64
- Push to ghcr.io
- Reference: tech spec section 9.3.8

TASK 5B.6: Forge Operator scaffold
- Directory: operator/
- Use kubebuilder init pattern (or manual):
  - api/v1alpha1/forgecluster_types.go — ForgeCluster CRD spec from tech spec section 9.3.4
  - controllers/forgecluster_controller.go — empty Reconcile with TODO comments
  - Makefile, Dockerfile

After all tasks: CI workflow syntax valid, Kueue configs valid YAML, operator compiles.

Output the Phase 5B technical report.
```

---

## Phase 6A — Metrics + Tracing + Profiling

```
You are continuing Forge development. Read CLAUDE.md.

This is Phase 6A. Prerequisites: Phase 5B complete.

TASK 6A.1: Implement Prometheus metrics
- File: internal/observability/metrics.go
- Implement ALL 6 metrics from tech spec section 8.1 EXACTLY:
  forge_workflows_submitted_total, forge_workflow_duration_seconds, forge_task_duration_seconds,
  forge_active_workers, forge_task_queue_depth, forge_task_retries_total
- Use promauto for registration

TASK 6A.2: Expose /metrics endpoint
- Add HTTP server on port 9090 (configurable) to Coordinator and Worker
- Serve prometheus.Handler() on /metrics
- Add --metrics-port flag to CLI

TASK 6A.3: Prometheus config + Grafana dashboard
- File: deploy/prometheus.yml — scrape Coordinator and Worker metrics endpoints
- File: deploy/grafana/dashboards/forge.json — 6 panels from tech spec section 8.3
- Panels: workflow throughput, task queue depth, task duration P50/P95/P99, worker health, retry rate, failed workflows

TASK 6A.4: Integrate OpenTelemetry Tracing
- File: internal/observability/tracing.go
- Initialize OTel TracerProvider with OTLP exporter (Jaeger endpoint)
- Coordinator: create root span per workflow, child span per task
- Reference: tech spec section 8.2 code example

TASK 6A.5: Cross-language context propagation
- Inject trace context (W3C traceparent) into gRPC metadata when dispatching tasks to workers
- Go Worker: extract and continue trace
- Python SDK: extract traceparent from gRPC metadata, create child span (use opentelemetry-python)
- C++ SDK: extract traceparent from gRPC metadata (basic — store as attribute if full OTel C++ is too heavy)

TASK 6A.6: Integrate OTel Continuous Profiling
- File: internal/observability/profiling.go
- CPU, Heap, Goroutine, Mutex, Block profiles
- Export via OTLP to Grafana Pyroscope (or log profile availability)
- Reference: tech spec section 5.8 — initProfiling() code

After all tasks: `go build ./...` and `go test ./...` must pass.

Output the Phase 6A technical report.
```

---

## Phase 6B — eBPF + NATS + Go 1.23 Features

```
You are continuing Forge development. Read CLAUDE.md.

This is Phase 6B. Prerequisites: Phase 6A complete.

TASK 6B.1: Write eBPF TCP latency probe (C)
- File: bpf/tcp_latency.c
- Copy from tech spec section 5.8 — trace_tcp BPF program
- File: bpf/headers/ — include vmlinux.h stub or bpf_helpers.h
- File: bpf/Makefile — compile with clang to .o

TASK 6B.2: Go eBPF loader
- File: internal/observability/ebpf.go
- Use github.com/cilium/ebpf to load BPF program
- Attach to kprobe/tcp_rcv_established
- Read perf buffer events, export as Prometheus histogram (tcp_latency_microseconds)
- Reference: tech spec section 5.8

TASK 6B.3: eBPF conditional compilation
- Use build tag: //go:build ebpf
- Default build excludes eBPF (ebpf_stub.go with no-op implementations)
- Build with eBPF: go build -tags ebpf ./...

TASK 6B.4: NATS JetStream message bus
- File: internal/bus/nats.go
- Implement same bus interface as pg_notify.go
- PublishTask → JetStream publish with message ID dedup
- SubscribeTasks → durable consumer per worker
- Reference: tech spec section 3.6 — PublishTask and SubscribeTasks code

TASK 6B.5: NATS KV Store heartbeat backend
- File: internal/cache/nats_kv.go
- Use NATS KV bucket "forge-workers" with 60s TTL
- Put worker info, Watch for changes
- Reference: tech spec section 3.6 — WorkerHeartbeat code

TASK 6B.6: Go 1.23 features — iter.Seq + unique
- Refactor DAG TopologicalOrder to return iter.Seq[*TaskDef] — copy from tech spec section 7.5
- Refactor WorkerRegistry to use unique.Handle[string] for worker IDs — copy from tech spec section 7.5
- Update go.mod to require go 1.23

TASK 6B.7: log/slog adapter
- File: internal/observability/logger.go
- Implement a logger interface that can switch between zerolog and slog
- slog handler with JSON output and RFC3339Nano timestamps
- CLI flag: --logger=zerolog|slog (default: zerolog)
- Reference: tech spec section 7.5

After all tasks: `go build ./...` and `go test ./...` must pass.

Output the Phase 6B technical report.
```

---

## Phase 6C — Admin Dashboard

```
You are continuing Forge development. Read CLAUDE.md.

This is Phase 6C. Prerequisites: Phase 6B complete.

TASK 6C.1: Initialize React project
- Directory: web/
- Use Vite + React + TypeScript + Ant Design Pro (or Ant Design 5)
- package.json, tsconfig.json, vite.config.ts
- Proxy /api to Coordinator's gRPC-Gateway port (8081)

TASK 6C.2: Overview page
- File: web/src/pages/overview/
- Cards: Active Workflows, Workers Online, Success Rate, Queue Depth
- Fetch from Coordinator REST API (GET /api/v1/stats or similar)
- Reference: tech spec section 5.10 dashboard mockup

TASK 6C.3: Workflow list page
- File: web/src/pages/workflows/list.tsx
- Table with columns: ID, Name, Status (colored badge), Duration, Task Count, Created At
- Filters: status dropdown, search by name
- Pagination
- Click row → navigate to workflow detail

TASK 6C.4: DAG visualizer component
- File: web/src/components/dag-visualizer/
- Use D3.js (d3-dag or dagre-d3) to render DAG
- Nodes colored by status: gray=pending, blue=running, green=completed, red=failed
- Show on workflow detail page
- Reference: tech spec section 5.10

TASK 6C.5: Worker topology page
- File: web/src/pages/workers/
- Table/cards: Worker ID, Language (Go/Python/C++ with icon/badge), Active Tasks, Capacity, Status, Last Heartbeat
- Reference: tech spec section 5.10

TASK 6C.6: API service layer
- File: web/src/services/api.ts
- Typed API client using fetch or axios
- Endpoints: listWorkflows, getWorkflow, listWorkers, getStats
- Type definitions matching proto messages

After all tasks: `cd web && npm install && npm run build` succeeds. `npm run dev` starts dev server.

Output the Phase 6C technical report.
```

---

## Phase 7 — Documentation + Examples + Final Polish

```
You are completing Forge development. Read CLAUDE.md.

This is Phase 7 (FINAL). Prerequisites: Phase 6C complete.

TASK 7.1: README.md
- Project title + one-line description
- Architecture diagram (ASCII art from tech spec section 4.1)
- Feature list (from tech spec section 1.3 — all 12 bullet points)
- Quick Start (docker-compose up + submit example workflow)
- Tech stack table
- Badge placeholders: CI status, Go version, License

TASK 7.2: Getting Started guide
- File: docs/getting-started.md
- Prerequisites, installation, configuration
- Start standalone mode, submit first workflow via CLI or gRPC
- Start distributed mode with docker-compose

TASK 7.3: SDK documentation
- File: docs/sdk-python.md — install, usage, @task_handler API, example
- File: docs/sdk-cpp.md — build with CMake, TaskHandler API, example

TASK 7.4: Four complete examples
- examples/simple-workflow/ — linear A→B→C with echo handlers + README
- examples/video-production/ — multi-language workers (Go+Python+C++) + README
- examples/cdc-trigger/ — PG table change triggers workflow + README
- examples/wasm-plugin/ — custom .wasm transform plugin + README
- Each example: self-contained with its own README, runnable instructions

TASK 7.5: Architecture + API docs
- File: docs/architecture.md — system overview, component responsibilities, data flow
- File: docs/api-reference.md — all gRPC services and methods, request/response format

TASK 7.6: Code cleanup
- Remove all TODO/FIXME comments (resolve or document in issues)
- Run golangci-lint, fix all errors
- Ensure all exported types/functions have doc comments
- Consistent error wrapping format across codebase

TASK 7.7: Final tag
- Update CLAUDE.md with final build commands and project status
- git tag v1.0.0
- Final `go build ./...` and `go test ./...` — everything must pass

Output the FINAL technical report. Include: total code stats, test coverage, feature completeness checklist (all 12 core features from tech spec 1.3).
```

---

## Notes for the Operator (CastWell)

### How to feed these prompts

1. Start Claude Code in the project directory
2. Ensure `CLAUDE.md` is at project root (Claude Code reads it automatically)
3. Place tech spec and implementation plan in `docs/` or inject as context
4. Copy-paste ONE Phase prompt at a time
5. Wait for technical report
6. Review → approve or request changes
7. Move to next Phase

### When to inject the full tech spec

- **Phase 1A–1B**: Inject tech spec (AI needs data structures, SQL schema, interface definitions)
- **Phase 2A–2B**: Inject tech spec (heartbeat protocol, scheduler algorithms, retry logic)
- **Phase 3A–3B**: Inject tech spec section 3.1 only (SDK examples)
- **Phase 4A–4C**: Inject tech spec sections 5.4–5.9 (event sourcing, saga, cron, CDC, wasm)
- **Phase 5A–5B**: Inject tech spec section 9 (deployment)
- **Phase 6A–6C**: Inject tech spec sections 5.8, 8.1–8.3, 5.10 (observability, dashboard)
- **Phase 7**: Inject tech spec section 1.3 + 4.1 (features list, architecture diagram)
