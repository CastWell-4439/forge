# Forge

**分布式任务调度引擎 + AI Agent 框架**

从 Bug 修复到代码生成，用声明式 YAML 编排复杂工作流，内建人工审批闸门（Human-in-the-Loop），让 AI 在可控范围内自主完成开发任务。

---

## 目录

- [核心能力](#核心能力)
- [系统架构](#系统架构)
- [目录结构](#目录结构)
- [技术细节](#技术细节)
  - [DAG 引擎](#dag-引擎)
  - [AI Agent 框架](#ai-agent-框架)
  - [Worker 体系](#worker-体系)
  - [HITL 人工闸门](#hitl-人工闸门)
  - [事件溯源与 Saga 补偿](#事件溯源与-saga-补偿)
  - [CDC 变更数据捕获](#cdc-变更数据捕获)
  - [Cron 调度与时间轮](#cron-调度与时间轮)
  - [Wasm 插件系统](#wasm-插件系统)
  - [可观测性](#可观测性)
  - [部署架构](#部署架构)
- [快速开始](#快速开始)
- [工作流定义示例](#工作流定义示例)
- [多语言 Worker SDK](#多语言-worker-sdk)
- [设计原则](#设计原则)
- [代码统计](#代码统计)
- [License](#license)

---

## 核心能力

| 能力 | 说明 |
|------|------|
| **DAG 工作流引擎** | YAML 声明式定义多步骤流水线，支持条件分支、循环、重试、超时 |
| **AI Agent 框架** | ReAct 推理循环、RAG 知识检索、MCP 工具协议、结构化输出、安全护栏 |
| **人工闸门 (HITL)** | 内建审批/通知/等待机制，通过飞书/Slack 推送，支持超时升级 |
| **可插拔 Worker** | AI、Git、Shell、Database、Code Review、Claude Code、MCP、HITL 等 8 种内建 Worker |
| **事件溯源** | 完整操作审计轨迹，支持 Replay 重放和 Saga 逆序补偿回滚 |
| **CDC 变更捕获** | PostgreSQL WAL 逻辑复制流式监听 + 轮询双通道 |
| **分布式协调** | etcd Leader 选举 + 服务发现 + NATS JetStream 消息总线 |
| **可观测性** | Prometheus 指标 + OpenTelemetry 链路追踪 + eBPF 内核追踪 + 连续 Profiling |
| **生产就绪部署** | Helm Chart + Docker 多阶段构建 + K8s Gateway API + Kueue GPU 调度 |
| **Admin Dashboard** | React 18 + D3.js DAG 可视化 + Ant Design 管理界面 |
| **多语言 SDK** | Go（原生）+ Python SDK + C++ SDK |

---

## 系统架构

```
┌──────────────────────────────────────────────────────────────────────┐
│                             接入层                                     │
│   CLI  │  Dashboard (React)  │  飞书 Bot  │  OpenClaw  │  Webhook     │
└───────────────────────────┬──────────────────────────────────────────┘
                            │ gRPC (:50051) / REST (:8081, grpc-gateway)
┌───────────────────────────▼──────────────────────────────────────────┐
│                          Coordinator                                   │
│                                                                        │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌────────────┐  │
│  │  Scheduler  │  │  DAG 执行器  │  │  HITL 管理器 │  │ 事件存储    │  │
│  │  Cron 触发   │  │  CEL 条件    │  │  审批/通知   │  │ 溯源+Replay │  │
│  │  轮询去重    │  │  路由/循环   │  │  超时升级    │  │ Saga 补偿   │  │
│  └─────────────┘  └─────────────┘  └─────────────┘  └────────────┘  │
│                                                                        │
│  ┌─────────────┐  ┌─────────────┐  ┌──────────────────────────────┐  │
│  │  Registry   │  │    CDC      │  │  分布式协调 (etcd + NATS)      │  │
│  │  YAML→DAG   │  │  WAL 流式   │  │  Leader 选举 + 服务发现        │  │
│  │  热加载      │  │  + 轮询回退  │  │  + JetStream 消息总线          │  │
│  └─────────────┘  └─────────────┘  └──────────────────────────────┘  │
└───────────────────────────┬──────────────────────────────────────────┘
                            │ gRPC 任务分发
┌───────────────────────────▼──────────────────────────────────────────┐
│                           Worker 集群                                  │
│                                                                        │
│  ┌──────┐ ┌──────┐ ┌──────┐ ┌────────┐ ┌──────┐ ┌───────────────┐  │
│  │  AI  │ │  Git │ │ Shell│ │Database│ │Review│ │  Claude Code  │  │
│  │      │ │      │ │      │ │(PG/Red)│ │      │ │               │  │
│  └──────┘ └──────┘ └──────┘ └────────┘ └──────┘ └───────────────┘  │
│  ┌──────┐ ┌──────┐ ┌────────────────────────────────────────────┐   │
│  │ MCP  │ │ HITL │ │    Agent Handlers (18 个领域工具处理器)       │   │
│  └──────┘ └──────┘ └────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────────────┘
                            │
┌───────────────────────────▼──────────────────────────────────────────┐
│                          存储层                                        │
│   PostgreSQL (元数据+事件+向量)  │  Redis (缓存+会话)  │  BoltDB (嵌入) │
└──────────────────────────────────────────────────────────────────────┘
```

---

## 目录结构

```
forge/
├── api/proto/              # Protobuf 定义 + grpc-gateway 生成代码
├── bpf/                    # eBPF 内核追踪程序 (CO-RE, kprobe)
├── build/                  # Dockerfile (coordinator / worker-go / worker-python / worker-cpp)
├── cmd/
│   ├── coordinator/        # Coordinator 主入口 (gRPC + REST + /metrics)
│   ├── worker/             # Worker 主入口
│   └── echo-plugin/        # Wasm 插件示例 (Go WASI → .wasm)
├── conf/                   # 配置模板 (agent.toml)
├── deploy/
│   ├── helm/forge/         # Helm Chart (StatefulSet + HPA + PDB + Gateway)
│   ├── kueue/              # Kueue GPU 调度配置 (A100/T4/CPU ResourceFlavor)
│   ├── migrations/         # PostgreSQL 迁移脚本 (001-006)
│   ├── prometheus.yml      # Prometheus 抓取配置
│   ├── grafana/            # Grafana Dashboard JSON (6 面板)
│   └── docker-compose.yml  # 本地开发一键启动
├── internal/
│   ├── agent/              # ===== AI Agent 框架 =====
│   │   ├── core/           #   公共类型 + 7 个模块接口 (零外部依赖)
│   │   ├── planning/       #   需求解析 + 任务规划 + DAG 生成 + DAG 校验
│   │   ├── session/        #   Session 状态机 + ForgeClient 桥接
│   │   ├── structured/     #   Go struct → JSON Schema + AgentResponse 校验重试
│   │   ├── harness/        #   ReAct 核心循环 + LLM Client + ToolRouter + Context 管理
│   │   ├── mcp/            #   MCP 协议实现 (JSON-RPC 2.0, stdio/HTTP 双传输)
│   │   ├── rag/            #   混合检索 (向量余弦 + BM25 + RRF 融合)
│   │   ├── memory/         #   短期记忆 (Redis) + 长期记忆 (pgvector 语义搜索)
│   │   ├── guardrails/     #   注入检测 + 内容过滤 + Token 预算
│   │   ├── checkpoint/     #   Agent 状态持久化 (Save/Load/Latest)
│   │   └── workers/        #   27 个领域工具处理器 (mock + real 双模式)
│   ├── bus/                # NATS JetStream 消息总线 (Publish/Subscribe/去重)
│   ├── cache/              # NATS KV Store Worker 心跳
│   ├── cdc/                # CDC 引擎 (WAL 逻辑复制 + 轮询回退 + Trigger YAML)
│   ├── coordinator/        # DAG 执行器 (CEL 条件 + 结果路由 + 循环 + DAG 缓存)
│   ├── discovery/          # etcd 服务发现 + Leader 选举
│   ├── event/              # 事件溯源存储 (Replay + ReplayUntil)
│   ├── hitl/               # HITL 管理器 + OpenClaw 回调 + 飞书消息格式化
│   ├── observability/      # 指标 + 追踪 + Profiling + eBPF + 结构化日志
│   ├── registry/           # Workflow YAML Schema → DAG 编译 + fsnotify 热加载
│   ├── saga/               # Saga 补偿器 (BuildPlan 逆序 + Execute)
│   ├── scheduler/          # 调度器 (Cron + Poll Trigger + 去重)
│   ├── storage/            # 存储后端 (BoltDB 嵌入式 + PostgreSQL)
│   ├── wasm/               # Wasm 运行时 (wazero 沙箱 + 插件注册 + SHA-256 校验)
│   ├── worker/             # Worker 管理器 + 4 层时间轮 + 任务执行
│   └── workers/            # V2 Worker 实现 (ai/git/shell/database/review/claudecode/mcp/hitl)
├── operator/               # Kubernetes CRD (ForgeCluster) + Controller 骨架
├── plugins/                # Wasm 插件示例
├── projects/               # 项目配置模板 (每个仓库一个 YAML)
├── scripts/                # 结构 lint 工具 (依赖方向检查 + 文件大小 + 命名)
├── sdk/
│   ├── python/             # Python Worker SDK (~350 行)
│   └── cpp/                # C++ Worker SDK (~700 行)
├── test/                   # 集成测试 (CDC WAL, 端到端)
├── web/                    # Admin Dashboard (React 18 + Vite 5 + Ant Design 5 + D3.js)
└── workflows/              # 工作流 YAML 定义 (bug_fix 等)
```

---

## 技术细节

### DAG 引擎

Forge 的核心调度单元是 DAG（有向无环图）。每个工作流被编译为 DAG，由 Coordinator 驱动执行。

**关键设计：**

| 特性 | 实现 |
|------|------|
| **拓扑排序执行** | `TopologicalOrder()` 返回 `iter.Seq[*Task]`（Go 1.22 range-over-func），按依赖顺序迭代 |
| **CEL 条件分支** | Google CEL 表达式引擎（`google/cel-go v0.28`），编译结果缓存，变量上下文包含 `results`/`vars`/`iteration`/`workflow_id` |
| **结果路由** | 4 种动作：`continue`（默认）/ `goto`（跳转）/ `abort`（中止）/ `skip`（跳过下游） |
| **循环支持** | `max_iterations`（默认 10，硬上限 100）+ `break_on` CEL 表达式 |
| **超时+重试** | 每个 Task 独立超时 + 指数退避重试（`max_attempts` + `initial_delay`） |
| **DAG 缓存** | Coordinator 维护 LRU DAG 缓存，自动淘汰（`evictDAGCache`），避免内存泄漏 |

**条件表达式示例：**
```yaml
condition: 'results.analyze_bug.risk_level != "low"'
loop:
  max_iterations: 3
  break_on: 'results.run_tests.exit_code == 0'
on_result:
  failure:
    action: goto
    target: write_fix
  rejected: abort
```

---

### AI Agent 框架

完整的 Agent 运行时，采用 **插件式架构**——通过 `WithXxx()` Option 注入模块，未注入的模块自动跳过。

#### ReAct 核心循环 (`internal/agent/harness/loop.go`)

```
┌─────────────────────────────────────────────────┐
│                  ReAct Loop                       │
│                                                   │
│  ┌─────────┐   ┌──────────┐   ┌──────────────┐  │
│  │  Think  │──▶│   Act    │──▶│   Observe    │  │
│  │(LLM推理)│   │(工具调用) │   │(结果→上下文) │  │
│  └─────────┘   └──────────┘   └──────────────┘  │
│       ▲                               │          │
│       └───────────────────────────────┘          │
│                                                   │
│  终止条件: Answer / maxSteps(20) / Budget 超限    │
└─────────────────────────────────────────────────┘
```

**运行流程：**
1. InputGuard 检查输入（注入检测）
2. 构建 System Prompt + 对话历史 → LLM
3. LLM 返回结构化 `AgentResponse`（Thought + Action 或 Answer）
4. 若为 Action → ToolRouter 调度工具 → 结果作为 Observation 追加
5. OutputGuard 过滤敏感信息
6. BudgetChecker 记录 token 消耗
7. Checkpoint 保存当前状态（可恢复）
8. Verifier 自检（Reflexion：错误时反馈给 LLM 重试）

#### 7 个可插拔模块

| 模块 | 接口 | 功能 |
|------|------|------|
| **M1 MCP** | `MCPManager` | Model Context Protocol 工具协议——JSON-RPC 2.0 双向通信，stdio/HTTP 双传输层，动态发现工具 |
| **M2 Harness** | `AgentLoop` | ReAct 循环 + LLM Client（OpenAI 兼容 + 重试 + TokenUsage 统计）+ Context Window 管理（超限时摘要压缩） |
| **M3 RAG** | `Retriever` | 混合检索——向量余弦相似度 + BM25 词频匹配 + RRF (Reciprocal Rank Fusion) 融合排序 |
| **M5 Memory** | `MemoryStore` | 短期记忆 (InMemory/Redis TTL) + 长期记忆 (pgvector 语义搜索，768 维向量) |
| **M6 Guardrails** | `InputGuard` / `OutputGuard` / `BudgetChecker` | 注入检测（正则+阈值）+ 敏感信息脱敏（内网地址/密码/Token）+ Session 级 Token 预算 |
| **M8 Structured** | `SchemaGenerator` / `Validator` | Go struct → JSON Schema（反射生成）+ AgentResponse 三选一校验 + ParseWithRetry（自动重试 2 次） |
| **M12 Checkpoint** | `CheckpointStore` | Agent 状态快照（Messages + StepIndex）→ 持久化 → 崩溃恢复（Load Latest → 续跑） |

#### 结构化输出 (`internal/agent/structured/`)

```go
// AgentResponse 要求 LLM 必须返回以下三选一：
type AgentResponse struct {
    Thought string      // 推理过程（必填）
    Action  *ToolAction // 工具调用（与 Answer 互斥）
    Answer  *string     // 最终答案（与 Action 互斥）
}
```

通过 JSON Schema 约束 LLM 输出格式，不合规时自动重试（最多 2 次），确保输出可解析。

---

### Worker 体系

Worker 通过 gRPC 连接 Coordinator，接收任务、执行、返回结果。支持多语言（Go/Python/C++）。

#### V2 内建 Worker

| Worker | 实现路径 | 核心能力 |
|--------|----------|----------|
| **AI Worker** | `internal/workers/ai/` | LLM 推理——Prompt 模板渲染 + JSON 输出解析 + 失败重试 |
| **Claude Code Worker** | `internal/workers/claudecode/` | 代码执行（`execute` action）+ 代码审查（`review` action） |
| **Git Worker** | `internal/workers/git/` | 5 读操作 (status/log/diff/show/blame) + 4 写操作 (branch/commit/push/mr) + 项目配置 |
| **Shell Worker** | `internal/workers/shell/` | **白名单命令**执行 + 超时控制 + 环境变量隔离 |
| **Database Worker** | `internal/workers/database/` | PostgreSQL **只读** SELECT + Redis GET/KEYS（硬编码禁写） |
| **Review Worker** | `internal/workers/review/` | Diff 分析 + Checklist 逐项校验 |
| **HITL Worker** | `internal/workers/hitl/` | 4 种动作：`notify` / `request_approval` / `request_input` / `notify_and_wait` |
| **MCP Worker** | `internal/workers/mcp/` | 7 种 MCP 协议操作（list_tools, call_tool, list_resources 等） |

#### 安全边界

- Shell Worker：仅执行白名单命令（`go build`/`go test`/`git status` 等），拒绝任意命令
- Database Worker：PostgreSQL 只允许 SELECT，Redis 只允许 GET/KEYS
- Git Worker：只写 feature/fix 分支，不触碰 master/develop
- 所有 Worker：执行超时硬限制

---

### HITL 人工闸门

HITL (Human-in-the-Loop) 是 Forge 的核心安全机制——确保 AI 的每一步关键操作都有人工确认。

**工作流程：**

```
Workflow 执行 → 到达 HITL 节点 → 暂停 DAG
        ↓
通知外部系统 (飞书/OpenClaw) → 推送审批消息给人类
        ↓
人类做决策 (approve/reject/modify) → 回调 Forge
        ↓
DAG 恢复执行 (根据决策走不同分支)
```

**关键设计：**
- `Manager` 维护 pending 请求池，支持持久化（PostgreSQL）防止崩溃丢失
- 可配置超时（默认 4h），超时自动升级或中止
- OpenClaw 集成：HTTP POST 发送通知，HTTP Handler 接收响应
- 飞书消息格式化：自动将工作流状态渲染为富文本卡片

---

### 事件溯源与 Saga 补偿

#### 事件溯源 (`internal/event/`)

所有状态变更记录为不可变事件流：

```go
type Event struct {
    ID        string
    Aggregate string // e.g. "workflow:123"
    Type      string // e.g. "TaskCompleted"
    Data      []byte // JSON payload
    CreatedAt time.Time
}
```

- `Replay()` — 从头重放所有事件，重建当前状态
- `ReplayUntil(t)` — 重放到指定时间点（调试/审计用）

#### Saga 补偿 (`internal/saga/`)

当多步工作流中间某步失败时，自动按**逆序**执行补偿操作：

```
Step1 ✅ → Step2 ✅ → Step3 ❌
                         ↓
        Compensate Step2 ← Compensate Step1 (逆序回滚)
```

- `BuildPlan(dag)` — 从 DAG 提取已完成步骤 + 补偿函数
- `Execute(plan)` — 逆序执行补偿，单步失败记录但继续（best-effort）
- 通过 `DAGView` 接口解耦，避免循环依赖

---

### CDC 变更数据捕获

双通道实现，确保可靠性：

#### WAL 逻辑复制 (`internal/cdc/wal.go`)

基于 `pglogrepl` 库，使用 PostgreSQL 逻辑复制协议（pgoutput v2）：

- 自动创建 Replication Slot + Publication
- 流式接收 INSERT/UPDATE/DELETE 事件
- Standby Status 心跳保持连接
- `isValidSlotName()` 防 SQL 注入

#### 轮询回退 (`internal/cdc/postgres.go`)

当 WAL 不可用时（权限不足/版本不支持），自动降级为轮询模式：

- 可配置轮询间隔
- 依赖注入 `WithQueryFunc` 便于测试
- 支持字段级过滤

#### Trigger YAML (`internal/cdc/trigger.go`)

CDC 事件 → YAML 匹配 → 触发工作流：
```yaml
trigger:
  table: issues
  events: [INSERT, UPDATE]
  condition: "{{.new.status}} == 'open' AND {{.new.priority}} IN ('P0','P1')"
  workflow: bug_fix
```

---

### Cron 调度与时间轮

#### Cron 调度器 (`internal/worker/cron.go`)

- 标准 5 字段表达式解析（分/时/日/月/周）
- `nextCronTime()` 精确计算下次触发时间
- 分布式锁去重（确保多副本只触发一次）

#### 4 层层级时间轮 (`internal/worker/timingwheel.go`)

```
Layer 0: 1ms  精度 (256 slots)
Layer 1: 1s   精度 (60 slots)
Layer 2: 1m   精度 (60 slots)
Layer 3: 1h   精度 (24 slots)
```

- O(1) 添加/取消定时器
- 自动级联降层（高层到期时向下层展开）
- 实测精度 ~10ms

---

### Wasm 插件系统

基于 [wazero](https://github.com/tetratelabs/wazero)（纯 Go，无 CGO）的安全沙箱：

**沙箱约束：**
- 内存限制（可配置上限）
- 执行超时（强制中止）
- 文件系统隔离（只能访问指定目录）
- 输出大小限制
- WASI stdin/stdout 协议通信

**插件管理：**
- `Registry`：注册/获取/版本管理/激活切换
- SHA-256 校验（防篡改）
- Pipeline 模式：多插件串行执行
- stub 回退：wazero 不可用时降级为内建实现

---

### 可观测性

#### Prometheus 指标 (`internal/observability/metrics.go`)

| 指标 | 类型 | 说明 |
|------|------|------|
| `forge_tasks_total` | Counter | 任务总数（按 status 分） |
| `forge_task_duration_seconds` | Histogram | 任务延迟 (p50/p95/p99) |
| `forge_active_workflows` | Gauge | 当前活跃工作流数 |
| `forge_workers_total` | Gauge | Worker 总数（按语言分） |
| `forge_retries_total` | Counter | 重试次数 |
| `forge_queue_depth` | Gauge | 等待队列深度 |

暴露 `/metrics` HTTP 端点，Prometheus 通过 K8s Service Discovery 自动抓取。

#### OpenTelemetry 追踪 (`internal/observability/tracing.go`)

- W3C `traceparent` 格式传播（跨服务、跨语言）
- Span 嵌套：Workflow → Task → Tool Call
- 支持 OTLP gRPC 导出到 Jaeger/Tempo

#### eBPF 内核追踪 (`bpf/tcp_latency.c` + `internal/observability/ebpf.go`)

- kprobe 挂载 `tcp_v4_connect` / `tcp_rcv_state_process`
- 测量 TCP 连接建立延迟（微秒级）
- cilium/ebpf 加载 + perf buffer 读取
- 非 Linux 平台编译为空实现（build tag: `linux,ebpf`）

#### 连续 Profiling (`internal/observability/profiling.go`)

- CPU / Heap / Goroutine / Mutex / Block 五种 Profile
- 后台周期采集（可配置间隔）
- `/debug/profile` HTTP 端点实时获取

#### 结构化日志 (`internal/observability/logger.go`)

- `slog` 标准库（Go 1.21+）
- 三后端：StdLog / JSON / Nop
- 自动附加 trace_id/span_id

#### Grafana Dashboard (`deploy/grafana/`)

预置 6 面板看板：任务吞吐量、延迟分位、活跃工作流、Worker 状态、重试趋势、队列深度。

---

### 部署架构

#### Docker 镜像 (`build/`)

多阶段构建，最终镜像基于 `scratch`（Coordinator/Worker-Go < 20MB）：

```dockerfile
FROM golang:1.22-alpine AS builder
RUN go build -ldflags="-s -w" -o /forge ./cmd/coordinator

FROM scratch
COPY --from=builder /forge /forge
ENTRYPOINT ["/forge"]
```

#### Helm Chart (`deploy/helm/forge/`)

- **Coordinator**: StatefulSet（有状态，需要持久化事件日志）+ Headless Service
- **Worker**: Deployment + HPA（按 CPU/自定义指标自动伸缩）
- **PodDisruptionBudget**: 保证升级时最少可用副本
- 独立 `values-dev.yaml` / `values-prod.yaml` 环境配置

#### Kubernetes Gateway API (`deploy/helm/forge/templates/gateway.yaml`)

- GRPCRoute: 内部 gRPC 流量路由
- HTTPRoute: REST API + Dashboard 静态资源
- 替代传统 Ingress，原生支持 gRPC

#### Kueue GPU 调度 (`deploy/kueue/`)

```yaml
resourceFlavors:
  - name: a100       # 高优先级 GPU 任务
  - name: t4         # 普通 GPU 任务
  - name: cpu-only   # 纯 CPU 任务
```

通过 ClusterQueue + LocalQueue 实现 GPU 任务排队和优先级抢占。

#### CI/CD (`.github/workflows/`)

- `ci.yml`: lint → test（含 PG + Redis service container）→ build
- `release.yml`: 多架构 Docker 构建 (amd64/arm64) + Helm chart 打包发布

---

## 快速开始

### 环境要求

| 组件 | 版本 | 用途 |
|------|------|------|
| Go | 1.22+ | 编译 |
| PostgreSQL | 15+ | 元数据存储、事件存储、CDC（需 `wal_level=logical`） |
| Redis | 7+ | 缓存、会话、心跳 |
| Node.js | 18+ | Dashboard 前端（可选） |
| etcd | 3.5+ | 分布式协调（可选，单机可用嵌入模式） |

### 编译

```bash
# 编译所有 Go 二进制
go build ./...

# 运行单元测试 (200+)
go test ./...

# 运行集成测试（需要运行中的 PostgreSQL）
go test -tags integration ./test/...

# 编译 Dashboard 前端
cd web && npm install && npm run build
```

### 本地运行

```bash
# 启动依赖（使用 docker-compose）
docker-compose up -d  # PostgreSQL + Redis + etcd

# 启动 Coordinator (gRPC :50051, REST :8081, Metrics :9090)
go run ./cmd/coordinator

# 启动 Worker
go run ./cmd/worker

# 访问 Dashboard
open http://localhost:8081
```

### 配置

```bash
# 本地配置覆盖（已 gitignore）
cp conf/agent.toml conf/agent.local.toml

# 项目接入配置
cp projects/avp_eds.yaml projects/my-project.yaml
```

敏感信息通过环境变量注入：

| 环境变量 | 说明 |
|----------|------|
| `FORGE_PG_PASSWORD` | PostgreSQL 密码 |
| `FORGE_REDIS_PASSWORD` | Redis 密码 |
| `FORGE_LLM_API_KEY` | LLM API 密钥（OpenAI 兼容格式） |
| `FORGE_CORS_ORIGINS` | Dashboard 允许的 CORS 源 |
| `FORGE_NATS_URL` | NATS 连接地址 |
| `FORGE_ETCD_ENDPOINTS` | etcd 节点地址 |

---

## 工作流定义示例

### Bug 修复全流程

```yaml
name: bug_fix
version: "2.0"
timeout: 2h

triggers:
  - type: cron
    schedule: "*/2 * * * *"    # 每 2 分钟轮询
    params:
      source: feishu_project
      filter: "status = 'open' AND priority IN ('P0', 'P1')"

  - type: webhook
    path: /api/workflows/bug_fix/trigger

tasks:
  # 1. AI 分析 Bug
  analyze_bug:
    handler: ai
    params:
      model: claude-sonnet-4-20250514
      system_prompt: |
        你是一个资深 Go 开发工程师。分析 Bug 报告，确定：
        1. 根因分析  2. 影响范围  3. 修复方案  4. 风险评估
      input: "{{ .trigger.bug_description }}"
      output_format: json
    timeout: 60s

  # 2. 人工审批修复方案（高风险时）
  approve_analysis:
    handler: hitl
    depends_on: [analyze_bug]
    params:
      action: request_approval
      message: "🐛 Bug 分析完成，请审批修复方案..."
      options: [approve, reject, modify]
      timeout: 4h
    condition: 'results.analyze_bug.risk_level != "low"'
    on_result:
      rejected: abort

  # 3. 创建修复分支
  create_branch:
    handler: git
    depends_on: [approve_analysis]
    params:
      action: create_branch
      base: develop
      name: "fix/{{ .trigger.bug_id }}"

  # 4. AI 编写修复代码
  write_fix:
    handler: claude_code
    depends_on: [create_branch]
    params:
      action: execute
      prompt: "根据分析结果修复 bug: {{ .results.analyze_bug | toJson }}"
    timeout: 10m
    retry: { max_attempts: 2, backoff: exponential }

  # 5. 运行测试（循环直到通过或 3 次失败）
  run_tests:
    handler: shell
    depends_on: [write_fix]
    params:
      command: "go test ./..."
    loop:
      max_iterations: 3
      break_on: 'results.run_tests.exit_code == 0'
    on_result:
      failure: { action: goto, target: write_fix }

  # 6. AI Code Review
  code_review:
    handler: ai
    depends_on: [run_tests]
    params:
      model: claude-sonnet-4-20250514
      system_prompt: "Review code changes for correctness, edge cases, performance..."
      input: "{{ .results.write_fix.diff }}"

  # 7. 最终人工确认
  final_approval:
    handler: hitl
    depends_on: [code_review]
    params:
      action: request_approval
      message: "✅ 修复完成，请最终审批"
      options: [approve, reject]

  # 8. 推送并创建 MR
  push_and_mr:
    handler: git
    depends_on: [final_approval]
    params:
      action: push_and_mr
      target: dev-offline
      title: "fix: {{ .trigger.bug_title }}"

  # 9. 完成通知
  notify_done:
    handler: hitl
    depends_on: [push_and_mr]
    params:
      action: notify
      message: "🎉 Bug 修复完成！MR: {{ .results.push_and_mr.mr_url }}"
```

---

## 多语言 Worker SDK

### Python Worker

```python
from forge_sdk import ForgeWorker, Task, TaskResult

worker = ForgeWorker(coordinator_addr="localhost:50051")

@worker.handler("data_analysis")
def handle(task: Task) -> TaskResult:
    # 你的业务逻辑
    result = analyze(task.params["input"])
    return TaskResult(output={"report": result})

worker.start()  # 注册到 Coordinator 并开始接收任务
```

### C++ Worker

```cpp
#include "forge_worker.h"

class VideoProcessor : public forge::TaskHandler {
    forge::TaskResult Execute(const forge::Task& task) override {
        auto input = task.GetParam("video_url");
        // 你的视频处理逻辑
        return forge::TaskResult::Success({{"output_url", processed_url}});
    }
};

int main() {
    forge::Worker worker("localhost:50051");
    worker.RegisterHandler("video_process", std::make_unique<VideoProcessor>());
    worker.Start();  // 阻塞，持续接收任务
}
```

---

## 设计原则

| 原则 | 说明 |
|------|------|
| **YAML 驱动** | 工作流是声明式的、可版本控制的、支持热加载——改 YAML 即改流程 |
| **人工兜底** | AI 永远不会自动合并代码或部署——关键节点必须人工确认 |
| **插件架构** | 添加新 Worker 不需要改核心引擎——实现 Handler 接口即可接入 |
| **安全第一** | Shell 白名单、DB 只读、文件保护列表、Token 预算、注入检测 |
| **优雅失败** | Saga 补偿回滚、指数退避重试、事件重放恢复、Checkpoint 断点续跑 |
| **可观测** | 每一步都有 Trace/Metric/Log，出问题能快速定位 |
| **Parse Don't Validate** | 类型系统保证正确性，非法数据在入口就拒绝，内部不做防御式检查 |

---

## 代码统计

| 指标 | 数值 |
|------|------|
| Go 源码（非测试、非生成） | ~21,000 行 |
| Go 测试代码 | ~9,000 行 |
| TypeScript (Dashboard) | ~1,500 行 |
| Python SDK | ~350 行 |
| C++ SDK | ~700 行 |
| Helm/Docker/K8s/CI 配置 | ~2,500 行 |
| 单元测试数量 | 200+ |
| Go Package 数量 | 30+ |
| 测试覆盖的包 | 100%（所有包均有测试） |

---

## License

MIT License — 详见 [LICENSE](LICENSE)

---


