# CLAUDE.md — Forge Project

## Project Overview

Forge is a distributed task scheduling engine with DAG workflow orchestration, multi-language workers (Go/Python/C++), and cloud-native deployment.

- **Primary language**: Go 1.22+ (Coordinator, Worker, CLI, SDK)
- **Secondary languages**: Python (AI/ML worker), C++ (high-performance worker), C (eBPF probes), TypeScript (Admin Dashboard)
- **Communication**: gRPC + Protobuf, buf for multi-language code generation
- **Storage**: PostgreSQL (production) / BoltDB (standalone)
- **Cache**: Redis
- **Coordination**: Embedded etcd (leader election, service discovery)
- **Observability**: Prometheus + OpenTelemetry (Jaeger) + zerolog + eBPF + OTel Profiling

## Reference Documents (MUST READ before coding)

Three documents define ALL technical decisions. They live in the project root:

| Document | Role | When to read |
|----------|------|-------------|
| `docs/tech-spec.md` | Architecture, data structures, interfaces, algorithms, deployment. **Source of truth for WHAT to build.** | Every Phase — look up the section referenced by each task |
| `docs/implementation-plan.md` | Phase breakdown, task lists, acceptance criteria. **Source of truth for HOW to build it.** | Start of each Phase — understand scope and evaluation criteria |
| `docs/phase-prompts.md` | Self-contained prompts per Phase (copy-paste ready). | Reference only — you'll receive the prompt directly |

When a task says "对应技术方案章节 5.1", open `docs/tech-spec.md` and implement exactly what section 5.1 describes — same struct names, same field names, same function signatures.

## Hard Constraints (NEVER violate)

### C1: Strict Scope
- Implement ONLY what the current Phase task list specifies. No extra features, no "nice to have" additions.
- If a task references a tech spec section, implement that section faithfully. Do not simplify, do not over-engineer.

### C2: Interface-First Development
- Define interfaces BEFORE implementations. Every major component has an interface in the tech spec (Storage, Scheduler, Coordinator discovery, CDC Source, etc.).
- Use the exact interface signatures from the tech spec. Copy them verbatim, then implement.

### C3: Naming Conventions
- Go packages: lowercase, single word when possible (`coordinator`, `storage`, `discovery`)
- Go files: snake_case (`worker_manager.go`, `scheduler_wrr.go`)
- Proto packages: `forge.v1`
- Proto messages: PascalCase (`TaskRequest`, `WorkflowStatus`)
- Database tables: snake_case (`workflow_instances`, `task_instances`)
- Use the exact names from the tech spec. Do not rename.

### C4: Error Handling
- All errors must be wrapped with context: `fmt.Errorf("schedule task %s: %w", taskID, err)`
- Never swallow errors silently
- Use structured logging (zerolog) for all error paths
- gRPC errors must use proper status codes (`codes.NotFound`, `codes.Internal`, etc.)

### C5: Testing Requirements
- Every new exported function needs a unit test
- Use `testify/assert` and `testify/require` for assertions
- Use `testcontainers-go` for integration tests requiring PG/Redis
- Test file naming: `xxx_test.go` in the same package
- Table-driven tests preferred for multiple cases

### C6: Build Must Pass
- After completing ANY task, run `go build ./...` — it must succeed
- After completing a Phase, run `go test ./...` — all tests must pass
- Never leave the project in an un-buildable state between tasks

### C7: Git Discipline
- Commit after each completed task (not each file)
- Commit message format: `feat(module): short description` or `test(module): short description`
- One logical change per commit

### C8: No Premature Dependencies
- Only add dependencies when the current task requires them
- Prefer standard library when the stdlib solution is within 20% of the library solution
- Every `go get` must be justified by a current task

### C9: Code Style
- `gofmt` + `goimports` on every file
- Max line length: 120 chars (soft limit)
- Comments on all exported types and functions
- Use Go 1.23 features (iter.Seq, unique, slog) only in Phase 6B where explicitly specified

### C10: Proto-to-Code Fidelity
- Proto definitions are the contract. Once defined in Phase 1A, they can only be extended (new fields/methods), never modified in breaking ways.
- All gRPC service implementations must satisfy the generated interface exactly.

## Directory Structure

```
forge/
├── cmd/forge/main.go              # CLI entry point
├── api/proto/                      # Protobuf definitions + buf config
├── internal/
│   ├── coordinator/                # Coordinator logic (DAG, scheduler, cron)
│   ├── worker/                     # Go Worker logic
│   ├── storage/                    # Storage interface + PG/BoltDB impl
│   ├── event/                      # Event sourcing
│   ├── saga/                       # Saga compensation
│   ├── cdc/                        # Change Data Capture
│   ├── wasm/                       # Wasm plugin executor
│   ├── discovery/                  # Service discovery interface + etcd
│   ├── bus/                        # Event bus (PG NOTIFY, NATS)
│   ├── cache/                      # Cache layer (Redis, NATS KV)
│   └── observability/              # Metrics, tracing, profiling, eBPF
├── sdk/
│   ├── go/                         # Go Worker SDK (public)
│   ├── python/                     # Python Worker SDK
│   └── cpp/                        # C++ Worker SDK
├── bpf/                            # eBPF C programs
├── plugins/                        # Wasm plugin examples
├── web/                            # Admin Dashboard (React)
├── deploy/                         # docker-compose, Helm, Prometheus, Grafana
├── operator/                       # K8s Operator
├── build/                          # Dockerfiles per component
├── test/                           # Integration tests
├── examples/                       # Usage examples
├── docs/                           # Documentation + reference specs
├── buf.yaml
├── buf.gen.yaml
├── Makefile
└── go.mod
```

## Build & Run Commands

```bash
# Build
make build              # or: go build ./...

# Generate proto
make proto              # or: buf generate

# Test
make test               # or: go test ./...
make test-integration   # integration tests (needs PG/Redis)

# Lint
make lint               # or: golangci-lint run

# Run (standalone mode)
./forge standalone --db=postgres://forge:forge@localhost:5432/forge --redis=redis://localhost:6379

# Run (distributed)
./forge coordinator --embed-etcd --db=postgres://... --redis=redis://...
./forge worker --coordinator=localhost:8080
```

## Phase Execution Protocol

When you receive a Phase task prompt:

1. **Read the task list** — understand every task item and its tech spec reference
2. **Read referenced tech spec sections** — open `docs/tech-spec.md` and find the exact section
3. **Implement in order** — tasks within a Phase may have dependencies; follow the numbering
4. **Commit per task** — `git commit` after each task is done
5. **Run build check** — `go build ./...` after each task
6. **Run full test suite** — `go test ./...` after the last task
7. **Output technical report** — use the template from `docs/implementation-plan.md`

## Key Data Structures (Quick Reference)

These are defined in the tech spec. Use them exactly:

- `DAG`, `TaskDef`, `RetryPolicy` — Section 5.1
- `Scheduler` interface, `WorkerInfo` — Section 5.2
- `WorkerRegistration` — Section 5.3
- `Event`, `EventType` constants — Section 5.4
- `CronTrigger` — Section 5.6
- `Storage` interface — Section 3.4
- `Coordinator` discovery interface — Section 3.3

## Important Algorithms

- DAG topological sort: Kahn's algorithm — Section 7.1
- Exponential backoff with full jitter — Section 7.2
- Task claiming: PG `FOR UPDATE SKIP LOCKED` — Section 7.3
- Timing wheel: hierarchical overflow — Section 7.4
