# AGENTS.md — Forge Project Agent Guide

## What is Forge?

A distributed task scheduling engine written in Go, with DAG workflow orchestration, multi-language worker support (Go/Python/C++), and Kubernetes-native deployment. Think lightweight Temporal.

## Documents You Must Read

All source-of-truth documents live in `docs/`:

| File | What it is | Size |
|------|-----------|------|
| `docs/tech-spec.md` | Full technical specification — architecture, data structures, interfaces, algorithms, SQL schema, deployment | ~97KB |
| `docs/implementation-plan.md` | Phased implementation plan — 15 phases, 97 tasks, evaluation criteria, report template | ~28KB |
| `docs/phase-prompts.md` | Copy-paste prompts for each Phase (used with Claude Code) | ~33KB |

`CLAUDE.md` at the project root contains coding conventions, hard constraints, and directory structure. Read it first.

## How This Project is Built

This project is built **phase by phase** using Claude Code. Each Phase:

1. Has a defined task list (see `docs/implementation-plan.md`)
2. References specific sections of the tech spec
3. Must produce a technical report upon completion
4. Requires human review before proceeding to the next Phase

**Current phase**: Not yet started. Begin with Phase 1A.

## Execution Rules

1. **One Phase at a time.** Never start Phase N+1 before Phase N is reviewed and approved.
2. **Faithful implementation.** When a task references "tech spec section X.Y", implement exactly what that section describes. Same names, same signatures, same algorithms.
3. **No scope creep.** Only build what the current Phase specifies. No extras.
4. **Build must always pass.** `go build ./...` after every task. `go test ./...` after every Phase.
5. **Commit per task.** Format: `feat(module): description` or `test(module): description`.

## Key Technical Decisions (Do Not Change)

These are settled in the tech spec. Do not second-guess them:

- **Language**: Go 1.22+ (core), Python (AI worker SDK), C++ (HPC worker SDK)
- **Communication**: gRPC + Protobuf (buf for codegen)
- **Storage**: PostgreSQL (prod) / BoltDB (standalone) — dual-backend via interface
- **Coordination**: Embedded etcd for leader election + service discovery
- **Event bus**: PG LISTEN/NOTIFY (default), NATS JetStream (optional)
- **Cache**: Redis (default), NATS KV (optional)
- **Observability**: Prometheus + OpenTelemetry + zerolog + eBPF
- **Wasm runtime**: wazero (pure Go, zero CGO)
- **CDC**: PG logical replication
- **Deployment**: docker-compose (dev), Helm + K8s (prod)

## Directory Structure

See `CLAUDE.md` for the full tree. Key paths:

- `cmd/forge/` — CLI entry point (cobra)
- `internal/` — All core logic (coordinator, worker, storage, etc.)
- `api/proto/` — Protobuf definitions
- `sdk/` — Go, Python, C++ Worker SDKs
- `deploy/` — docker-compose, Helm charts, Prometheus config
- `test/` — Integration tests
- `docs/` — All reference documentation

## For Non-Claude-Code Agents

If you're not Claude Code but another AI coding agent:

1. Read `CLAUDE.md` for conventions — they apply to all agents
2. Read `docs/implementation-plan.md` for the Phase you're working on
3. Read the referenced sections of `docs/tech-spec.md`
4. Follow the same commit, build, and test discipline
5. Output a technical report when done (template in `docs/implementation-plan.md`)
