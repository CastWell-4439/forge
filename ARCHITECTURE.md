# ARCHITECTURE.md

> 最后更新：2026-05-13
> 用途：新贡献者（人类或 AI）快速理解项目结构，无需阅读全部源码

## 鸟瞰

Forge 是一个 **分布式 DAG 任务调度引擎 + AI Agent 引擎**，用 Go 实现。

```
┌─────────────────────────────────────────────────────┐
│                   Layer 3: 应用层                     │
│         （工作流 YAML、飞书集成、HITL）               │
├─────────────────────────────────────────────────────┤
│                   Layer 2: 自动化框架                 │
│      （Registry、Scheduler、Worker Pool）            │
├─────────────────────────────────────────────────────┤
│                   Layer 1: Agent 引擎                 │
│  （MCP 工具协议、RAG、Memory、Guardrails、ReAct）    │
├─────────────────────────────────────────────────────┤
│                   Layer 0: 基础设施                   │
│  （DAG 调度、存储、CDC、Wasm、NATS、可观测性）       │
└─────────────────────────────────────────────────────┘
```

**当前状态**：Layer 0 ✅ 完成 | Layer 1 ⬜ 进行中（AE-2~4）| Layer 2-3 ⬜ 待开始

## Codemap

```
D:\forge\
├── cmd/forge/           — 二进制入口（coordinator / worker / standalone）
├── api/proto/           — Protobuf 定义 + buf 生成的 Go 代码
├── internal/
│   ├── coordinator/     — 分布式调度核心：Leader 选举、DAG 推进、任务分发、Saga 补偿
│   ├── worker/          — Worker 框架：注册、心跳、任务执行
│   ├── storage/         — 存储抽象：PostgreSQL + BoltDB 双后端
│   ├── discovery/       — 服务发现：嵌入式 etcd
│   ├── event/           — 事件溯源：事件存储 + Replay
│   ├── saga/            — Saga 补偿器：逆序补偿链
│   ├── cdc/             — 变更数据捕获：PG WAL + Polling 双模式
│   ├── wasm/            — Wasm 插件沙箱：wazero 运行时
│   ├── bus/             — 消息总线：NATS JetStream
│   ├── cache/           — NATS KV 缓存（心跳等）
│   ├── observability/   — 可观测性：Prometheus + OTel + eBPF
│   ├── gateway/         — gRPC-Gateway REST API
│   ├── agent/           — 【Layer 1】AI Agent 引擎
│   │   ├── core/        — 零依赖：接口定义 + 公共类型（7 个模块接口）
│   │   ├── structured/  — JSON Schema 生成 + Structured Output 校验
│   │   ├── planning/    — 需求解析 → DAG 生成（3 策略）
│   │   ├── session/     — Agent 会话状态机
│   │   ├── harness/     — ReAct 循环：LLM 调用 + ToolRouter + Context 管理
│   │   ├── workers/     — 工具注册表 + 18 个视频 Handler + 通用工具
│   │   ├── mcp/         — 【AE-2】MCP 协议：JSON-RPC + Client + Manager + Bridge
│   │   ├── rag/         — 【AE-3】混合检索：向量 + BM25 + RRF
│   │   ├── memory/      — 【AE-3】短期/长期记忆
│   │   ├── guardrails/  — 【AE-4】安全护栏：注入检测 + 内容过滤 + 预算
│   │   └── checkpoint/  — 【AE-4】状态持久化 + 崩溃恢复
│   └── scheduler/       — 【Layer 2】Cron + 时间轮
├── sdk/                 — 多语言 Worker SDK（Python / C++）
├── deploy/              — Docker + Helm + Migrations
├── web/                 — Admin Dashboard（React / Vite / AntD / D3.js）
├── scripts/             — lint、构建、辅助脚本
├── test/                — 集成测试
├── workflows/           — 示例工作流 YAML
└── docs/                — 技术方案 + 实施计划 + 编码约定
```

## 架构不变量

1. **依赖方向（agent 内部）**：`core ← structured ← planning ← session ← harness ← workers`。左边不能 import 右边。
2. **core 零依赖**：`agent/core/` 只包含接口和类型定义，禁止 import 任何 `internal/` 子包。
3. **7 接口插拔**：Agent 通过 `core/interfaces.go` 的 7 个接口（InputGuard、OutputGuard、BudgetChecker、Retriever、MemoryStore、CheckpointStore、MCPManager）可选组装，缺少任一模块 Agent 仍能运行。
4. **存储可替换**：`storage.Interface` 有 PostgreSQL 和 BoltDB 两个实现，上层代码不感知具体后端。
5. **MCP 工具透明**：通过 MCPBridge 注册的外部工具与原生工具对 Agent Harness 完全一致，无特殊路径。

## 层间边界

| 边界 | 规则 |
|------|------|
| Layer 0 ↔ Layer 1 | Agent 通过 `storage.Interface` 读写持久化数据；通过 `bus.MessageBus` 发事件 |
| Layer 1 ↔ Layer 2 | Layer 2 的 Scheduler 通过 `Agent.Run()` 入口触发 Agent 执行 |
| 外部 ↔ Forge | gRPC（coordinator.proto / worker.proto）+ REST（gateway/）|
| Agent ↔ 外部工具 | MCP 协议（stdio JSON-RPC），由 MCPManager 管理生命周期 |

## 技术栈

- Go 1.26.1 / gRPC / Protobuf (buf 1.67.0)
- PostgreSQL（主存储 + pgvector）/ BoltDB（单机备选）
- NATS JetStream（消息总线）/ etcd（服务发现）
- wazero（Wasm 沙箱）/ cilium/ebpf（内核追踪）
- React + Vite + AntD + D3.js（Admin UI）
