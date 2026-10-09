# Forge

[![CI](https://github.com/CastWell-4439/forge/actions/workflows/ci.yml/badge.svg)](https://github.com/CastWell-4439/forge/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**声明式工作流引擎 + AI Agent 运行时 + Agent 控制面。**

用一份 YAML 描述一条多步骤流水线，由 Coordinator 编译成 DAG 并调度一组异构 Worker 执行——其中既可以是普通函数，也可以是 Claude Code 这样的编码 Agent。每一步都有事件溯源、状态机和人工审批闸门，因此整个执行过程可审计、可回放、可评测、可沉淀。

---

## 目录

- [这个系统做什么](#这个系统做什么)
- [核心能力](#核心能力)
- [系统架构](#系统架构)
- [目录结构](#目录结构)
- [技术细节](#技术细节)
  - [DAG 引擎](#dag-引擎)
  - [AI Agent 运行时](#ai-agent-运行时)
  - [Worker 体系](#worker-体系)
  - [ForgeX：Agent 控制面](#forgexagent-控制面)
  - [HITL 人工闸门](#hitl-人工闸门)
  - [事件溯源与 Saga 补偿](#事件溯源与-saga-补偿)
  - [CDC 变更数据捕获](#cdc-变更数据捕获)
  - [Cron 调度与时间轮](#cron-调度与时间轮)
  - [Wasm 插件系统](#wasm-插件系统)
  - [可观测性](#可观测性)
  - [部署架构](#部署架构)
- [快速开始](#快速开始)
- [工作流定义](#工作流定义)
- [多语言 Worker SDK](#多语言-worker-sdk)
- [设计原则](#设计原则)
- [项目规模](#项目规模)

---

## 这个系统做什么

它把「反复出现、需要人来盯的工程杂活」变成一条可声明的流水线。以一条自动修复缺陷的流水线为例：

```
① 定时轮询外部需求系统（轮询间隔与去重键都在 YAML 里声明）
        ↓
② 按去重键过滤已处理过的条目（幂等）
        ↓
③ 外部编排器把需求渲染成一份工作流 YAML，投递给 Forge
        ↓
④ 校验：schema 合法 + DAG 无环 → 编译成 DAG
        ↓
⑤ Coordinator 以 orchestrator 模式调度：节点之间不直接通信，
   任务由 Coordinator 下发、结果由 Coordinator 汇总
        ↓
⑥ Worker 认领执行（AI / Claude Code / Git / Shell / Database / Review / MCP / HITL）
        ↓
⑦ 每个节点状态变更写入事件流；节点输出与产物索引回传
        ↓
⑧ 关键节点插入人工审批闸门（HITL），批准后才继续
        ↓
⑨ 流程结束后由 ForgeX 评测：确定性规则 + 轨迹断言 → 出沉淀草稿
        ↓
⑩ 人工 review 通过后进入 case 库，可回放、可作为回归基线
```

支撑它的三条设计不变量：

| 不变量 | 含义 |
|---|---|
| **orchestrator 模式** | 节点之间不直接通信，一切经 Coordinator 下发与回收——这让每一步都能被记录、被中断、被审批 |
| **一切可声明** | 触发方式、依赖、条件、循环、重试、超时、审批点全部在 YAML 里，流程即版本化资产 |
| **模型之外强制约束** | 审批、权限、停止条件、评测都发生在确定性控制面，不依赖模型自觉 |

---

## 核心能力

| 能力 | 说明 |
|------|------|
| **DAG 工作流引擎** | YAML 声明式定义，支持条件分支（CEL）、结果路由、循环、独立超时与指数退避重试 |
| **AI Agent 运行时** | ReAct 推理循环、RAG 混合检索、MCP 工具协议、结构化输出校验、安全护栏 |
| **ForgeX 控制面** | 工具契约与策略、停止仲裁、失败指纹、追加式 run 工件、确定性评测与回归闭环 |
| **人工闸门 (HITL)** | 内建审批/通知/等待机制，可经外部系统推送审批卡片，支持超时升级 |
| **可插拔 Worker** | AI、Git、Shell、Database、Review、Claude Code、MCP、HITL、Wasm、Agent 共 10 种内建 Worker |
| **事件溯源** | 完整操作审计轨迹，支持 `Replay` / `ReplayUntil` 重建任意时点状态 |
| **Saga 补偿** | 多步流程中途失败时按逆序执行补偿回滚 |
| **CDC 变更捕获** | PostgreSQL WAL 逻辑复制流式监听 + 轮询回退双通道 |
| **分布式协调** | etcd Leader 选举 + 服务发现 + NATS JetStream 消息总线 |
| **可观测性** | Prometheus 指标 + OpenTelemetry 链路追踪 + eBPF 内核追踪 + 连续 Profiling |
| **多语言 SDK** | Go（原生）+ Python SDK + C++ SDK |
| **Admin Dashboard** | React + Vite + Ant Design + D3.js，DAG 可视化与管理界面 |
| **生产就绪部署** | Helm Chart + Docker 多阶段构建 + K8s Gateway API + Kueue GPU 调度 |

---

## 系统架构

```
┌──────────────────────────────────────────────────────────────────────┐
│                             接入层                                     │
│   CLI  │  Admin Dashboard  │  Chat Bot  │  MCP 客户端  │  Webhook     │
└───────────────────────────┬──────────────────────────────────────────┘
                            │ gRPC (:50051) / REST (:8081, grpc-gateway)
┌───────────────────────────▼──────────────────────────────────────────┐
│                          Coordinator                                   │
│                                                                        │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌────────────┐  │
│  │  Scheduler  │  │  DAG 执行器  │  │  HITL 管理器 │  │  事件存储   │  │
│  │  Cron 触发   │  │  CEL 条件    │  │  审批/通知   │  │ 溯源+Replay │  │
│  │  轮询去重    │  │  路由/循环   │  │  超时升级    │  │ Saga 补偿   │  │
│  └─────────────┘  └─────────────┘  └─────────────┘  └────────────┘  │
│                                                                        │
│  ┌─────────────┐  ┌─────────────┐  ┌──────────────────────────────┐  │
│  │  Registry   │  │    CDC      │  │   分布式协调 (etcd + NATS)     │  │
│  │  YAML→DAG   │  │  WAL 流式    │  │  Leader 选举 + 服务发现        │  │
│  │  热加载      │  │  + 轮询回退  │  │  + JetStream 消息总线          │  │
│  └─────────────┘  └─────────────┘  └──────────────────────────────┘  │
└───────────────────────────┬──────────────────────────────────────────┘
                            │ gRPC 任务分发
┌───────────────────────────▼──────────────────────────────────────────┐
│                           Worker 集群                                  │
│                                                                        │
│  ┌──────┐ ┌──────┐ ┌──────┐ ┌────────┐ ┌──────┐ ┌───────────────┐  │
│  │  AI  │ │  Git │ │Shell │ │Database│ │Review│ │  Claude Code  │  │
│  │      │ │      │ │      │ │(PG/Red)│ │      │ │               │  │
│  └──────┘ └──────┘ └──────┘ └────────┘ └──────┘ └───────────────┘  │
│  ┌──────┐ ┌──────┐ ┌────────────────────────────────────────────┐   │
│  │ MCP  │ │ HITL │ │   Agent 工具处理器 (23 常驻 + subagent 按需)   │   │
│  └──────┘ └──────┘ └────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────────────┘
                            │
┌───────────────────────────▼──────────────────────────────────────────┐
│                          存储层                                        │
│   PostgreSQL (元数据+事件+向量)  │  NATS  (消息+心跳)  │  BoltDB (嵌入) │
└──────────────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────────────┐
│  ForgeX（控制面，与运行时同进程、零 LLM 依赖）                          │
│   Run/Trace 工件 · 工具契约与策略 · 停止仲裁 · 失败指纹 · 评测与回归    │
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
│   ├── worker/             # Worker 主入口 (8 种内建 Worker 的派发)
│   ├── forge/              # CLI (standalone / coordinator / worker 子命令)
│   ├── forgex/             # ForgeX CLI (run-demo / eval / cases / policy / serve)
│   └── echo-plugin/        # Wasm 插件示例 (Go WASI → .wasm)
├── conf/                   # 配置模板 (agent.toml)
├── configs/forgex/         # ForgeX 配置：策略、工具契约、评测规则、失败分类学
├── deploy/
│   ├── helm/forge/         # Helm Chart (StatefulSet + HPA + PDB + Gateway)
│   ├── kueue/              # Kueue GPU 调度配置 (A100/T4/CPU ResourceFlavor)
│   ├── migrations/         # PostgreSQL 迁移脚本 (5 个)
│   ├── prometheus.yml      # Prometheus 抓取配置
│   ├── grafana/            # Grafana Dashboard JSON
│   └── docker-compose.yml  # 本地开发一键启动
├── examples/forgex/        # ForgeX 示例 task packet
├── internal/
│   ├── agent/              # ===== AI Agent 运行时 =====
│   │   ├── core/           #   公共类型 + 模块接口 (零外部依赖)
│   │   ├── planning/       #   需求解析 + 任务规划 + DAG 生成 + 四层 DAG 校验
│   │   ├── session/        #   Session 状态机 + ForgeClient 桥接
│   │   ├── structured/     #   Go struct → JSON Schema + 响应校验重试
│   │   ├── harness/        #   ReAct 核心循环 + LLM Client + ToolRouter + Context 管理
│   │   ├── mcp/            #   MCP 协议实现 (JSON-RPC 2.0, stdio/HTTP 双传输)
│   │   ├── rag/            #   混合检索 (向量余弦 + BM25 + RRF 融合)
│   │   ├── memory/         #   短期记忆 + 长期记忆 (基于 RAG DocumentStore 接口)
│   │   ├── guardrails/     #   注入检测 + 内容过滤 + Token 预算
│   │   ├── checkpoint/     #   Agent 状态快照 (CheckpointStore 接口)
│   │   └── workers/        #   23 个常驻工具 + subagent(可配模式, 默认关)
│   ├── forgex/             # ===== ForgeX 控制面 (24 个子包) =====
│   │   ├── model/          #   Run/Trace/工件/策略/停止/世界状态等模型
│   │   ├── policy/         #   策略引擎 + Authority L0–L4 渐进式信任
│   │   ├── toolgw/         #   工具契约 + 校验器 + 审计
│   │   ├── stop/           #   停止信号 + 终止仲裁器
│   │   ├── failure/        #   失败分类学 + 稳定指纹
│   │   ├── state/          #   世界状态：claim 校验、权限、范围
│   │   ├── storage/        #   追加式 run 工件 (JSONL/YAML) + SQLite 索引
│   │   ├── eval/           #   确定性规则评测 + 轨迹断言
│   │   ├── scorecard/      #   多维质量评分
│   │   ├── report/         #   Markdown 报告 + badcase 草稿
│   │   ├── lessons/        #   经验沉淀
│   │   ├── promotion/      #   坏例提升 (需人工 review)
│   │   ├── reliability/    #   重复评测 (pass@k / flaky rate)
│   │   ├── runtimegate/    #   执行前反控闸门 (shadow / enforce)
│   │   ├── productapi/     #   本地控制面 API
│   │   └── demo/           #   通用 success / violation 用例
│   ├── bus/                # NATS JetStream 消息总线
│   ├── cache/              # NATS KV Store Worker 心跳
│   ├── cdc/                # CDC 引擎 (WAL 逻辑复制 + 轮询回退 + Trigger YAML)
│   ├── coordinator/        # DAG 执行器 (CEL 条件 + 结果路由 + 循环 + DAG 缓存)
│   ├── discovery/          # etcd 服务发现 + Leader 选举
│   ├── event/              # 事件溯源存储 (Replay + ReplayUntil)
│   ├── hitl/               # HITL 管理器 + 回调 + 消息格式化
│   ├── observability/      # 指标 + 追踪 + Profiling + eBPF + 结构化日志
│   ├── registry/           # Workflow YAML Schema → DAG 编译 + fsnotify 热加载
│   ├── saga/               # Saga 补偿器 (BuildPlan 逆序 + Execute)
│   ├── scheduler/          # 调度器 (Cron + Poll Trigger + 去重)
│   ├── storage/            # 存储后端 (BoltDB 嵌入式 + PostgreSQL)
│   ├── wasm/               # Wasm 运行时 (wazero 沙箱 + 插件注册 + SHA-256 校验)
│   ├── worker/             # Worker 管理器 + 4 层时间轮 + 执行器 + 闸门接缝
│   └── workers/            # V2 Worker 实现 (ai/git/shell/database/review/claudecode/mcp/hitl)
├── operator/               # Kubernetes CRD (ForgeCluster) + Controller
├── plugins/                # Wasm 插件示例
├── projects/               # 项目配置模板 (每个仓库一个 YAML)
├── scripts/                # 结构 lint 工具 (依赖方向检查 + 文件大小 + 命名)
├── sdk/
│   ├── python/             # Python Worker SDK
│   └── cpp/                # C++ Worker SDK
├── test/                   # 集成测试
├── web/                    # Admin Dashboard (React + Vite + Ant Design + D3.js)
└── workflows/              # 工作流 YAML 定义
```

---

## 技术细节

### DAG 引擎

Forge 的核心调度单元是 DAG。每个工作流被编译为 DAG，由 Coordinator 驱动执行。

| 特性 | 实现 |
|------|------|
| **拓扑排序执行** | `TopologicalOrder()` 按依赖顺序迭代（Kahn 算法） |
| **判环** | 编译期校验，检出环路即拒绝加载 |
| **CEL 条件分支** | Google CEL 表达式引擎，编译结果缓存；上下文含 `results`（已完成任务的命名产出）与 `workflow_id`，循环中另有 `iteration`。任务自己的 `condition` 为假即标记 `SKIPPED`；表达式本身出错则**任务失败**（失败方向取安全的一侧） |
| **结果路由** | 4 种动作：`continue`（默认）/ `goto:<task>`（回跳到**祖先**，重跑该段）/ `abort`（中止工作流）/ `skip`（跳过下游）。**无法履行的路由（非祖先、目标不存在）会失败整个工作流**，而不是静默当作 `continue` |
| **循环支持** | `loop.max_iterations`（默认 10，硬上限 100）+ `loop.break_on` CEL 表达式；**计数持久化在任务的 `loop_iteration` 列**，重启不丢 |
| **超时+重试** | 任务级 `timeout`（回落工作流级）+ 重试：`retry.max_attempts` / `backoff`（`fixed`/`exponential`/`exponential_with_jitter`）/ `initial_interval` / `max_interval` / `multiplier`（默认 2.0）。`max_attempts` 是**总执行次数** |
| **DAG 缓存** | Coordinator 按 workflow ID 缓存已编译 DAG，工作流终态时**显式淘汰**（`evictDAGCache`），避免泄漏 |

---

### AI Agent 运行时

采用**插件式架构**——通过 `WithXxx()` Option 注入模块，未注入的模块自动跳过。

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
│  终止条件: Answer / maxSteps / Token 预算超限      │
└─────────────────────────────────────────────────┘
```

**运行流程：**
1. InputGuard 检查输入（注入检测）
2. 构建 System Prompt + 对话历史 → LLM
3. LLM 返回结构化响应（Thought + Action，或 Answer）
4. 若为 Action → ToolRouter 调度工具（带超时与 panic 防护）→ 结果作为 Observation 追加
5. OutputGuard 过滤敏感信息
6. BudgetChecker 记录 token 消耗
7. Checkpoint 保存状态快照
8. Verifier 自检（Reflexion：错误时反馈给 LLM 重试）

#### 可插拔模块

| 模块 | 接口 | 功能 |
|------|------|------|
| **MCP** | `MCPManager` | Model Context Protocol 工具协议——JSON-RPC 2.0 双向通信，stdio/HTTP 双传输层，动态发现工具 |
| **Harness** | `AgentLoop` | ReAct 循环 + LLM Client（OpenAI 兼容 + 重试 + TokenUsage 统计）+ Context Window 管理 |
| **RAG** | `Retriever` | 混合检索——向量余弦相似度 + BM25 + RRF (Reciprocal Rank Fusion) 融合排序 |
| **Memory** | `MemoryStore` | 短期记忆（TTL）+ 长期记忆（pgvector 语义搜索，复用 RAG 的 Embedder + DocumentStore） |
| **Guardrails** | `InputGuard` / `OutputGuard` / `BudgetChecker` | 注入检测 + 敏感信息脱敏 + Session 级 Token 预算 |
| **Structured** | `SchemaGenerator` / `Validator` | Go struct → JSON Schema（反射生成）+ 响应校验 + 失败自动重试 |
| **Checkpoint** | `CheckpointStore` | Agent 状态快照（Messages + StepIndex）持久化，崩溃后从最近快照续跑 |

#### 四层 DAG 校验 (`internal/agent/planning/`)

LLM 生成的 DAG 在采纳前经过四层防御，任一层失败都会把**具体错误**反馈给模型重新生成，最终以模板兜底：

| 层 | 校验内容 |
|---|---|
| L1 | 格式与可解析性 |
| L2 | 结构合法性（schema） |
| L3 | 语义（handler 存在性 + 无环） |
| L4 | 参数完整性 |

---

### Worker 体系

Worker 通过 gRPC 连接 Coordinator，接收任务、执行、返回结果。支持多语言（Go/Python/C++）。

#### 内建 Worker

| Worker | 实现路径 | 核心能力 |
|--------|----------|----------|
| **AI Worker** | `internal/workers/ai/` | LLM 推理——Prompt 渲染 + JSON 输出解析 + 失败重试 |
| **Claude Code Worker** | `internal/workers/claudecode/` | 代码执行与修改（可注入命令工厂，便于测试） |
| **Git Worker** | `internal/workers/git/` | 读操作 (status/log/diff/show/blame) + 写操作 (branch/commit/push/mr) + 项目配置 |
| **Shell Worker** | `internal/workers/shell/` | **白名单命令**执行 + 工作目录白名单 + 超时控制 |
| **Database Worker** | `internal/workers/database/` | PostgreSQL **只读** SELECT |
| **Review Worker** | `internal/workers/review/` | 计划与代码评审（可结合 RAG 检索项目约定） |
| **HITL Worker** | `internal/workers/hitl/` | 4 种动作：`notify` / `request_approval` / `request_input` / `notify_and_wait` |
| **MCP Worker** | `internal/workers/mcp/` | MCP 协议操作（list_tools / call_tool / list_resources 等） |
| **Wasm Worker** | `internal/workers/wasm/` | Wasm 插件执行（wazero 沙箱；`action` 即插件名；插件目录自动发现 + SHA-256 身份） |
| **Agent Worker** | `internal/workers/agent/` | 完整 ReAct agent 执行（黄金工具集 real 模式；`action: run` + `params.task`；检索/数据/搜索后端按环境注入） |

#### 安全边界

- Shell Worker：仅执行白名单命令，拒绝任意命令
- Database Worker：PostgreSQL 只允许 SELECT
- Git Worker：只写特性分支，不触碰主干
- 所有 Worker：执行超时硬限制

#### 派发链的完整性守卫

工作流 YAML 里声明的每一种 Worker 都必须在二进制里注册，否则该节点只会在运行时以「未知 handler」失败。测试套件包含一条守卫：**扫描 `workflows/*.yaml` 中引用的全部 Worker，断言它们都已注册**。

---

### ForgeX：Agent 控制面

ForgeX 是与运行时同进程、**刻意不依赖 LLM** 的控制面。它回答的是另一个问题：**Agent 执行得是否可信、失败在哪里、下次能不能自动回归。**

核心闭环：

```
Case / Dataset → Run / Trace → Evaluate / Score → Explain / Report
              → Learn / Lesson → Promote / Regression
```

| 能力 | 说明 |
|------|------|
| **Run 工件** | 每次执行落一个追加式 run 目录：事件流、工具调用、策略决策、停止决策、世界状态、产物索引、报告、badcase |
| **工具契约** | 工具不只是 API wrapper，而是带 `risk_level` / `side_effect` / `idempotency` / `approval` 的契约 |
| **策略引擎** | Authority L0–L4 渐进式信任；规则只能收紧安全底线，不会意外放宽 |
| **停止仲裁** | 多路停止信号按固定优先级链仲裁；硬安全信号永远压过「模型说完成了」 |
| **失败指纹** | 归一化可变字段后再哈希，让重试预算、去重与经验沉淀都锚定在**逻辑失败**而非单次现场 |
| **确定性评测** | 规则断言 + **轨迹断言**（断言它怎么跑的，而不只是它说了什么） |
| **回归闭环** | 失败 → badcase → 人工 review → 提升为 case → 可重复评测（pass@k / flaky rate） |
| **执行前闸门** | 支持 shadow（只记录）/ enforce（真拦截）两种模式，可在 worker 执行前反控 |
| **世界状态** | Claim → 权限校验 → 校验器 → Fact；Agent 只能提出事实，不能直接改写事实 |

---

### HITL 人工闸门

HITL (Human-in-the-Loop) 是 Forge 的核心安全机制——确保关键操作都有人工确认。

```
Workflow 执行 → 到达 HITL 节点 → 暂停 DAG
        ↓
通知外部系统 → 推送审批消息给人类
        ↓
人类做决策 (approve/reject/modify) → 回调 Forge
        ↓
DAG 恢复执行 (根据决策走不同分支)
```

**关键设计：**
- `Manager` 维护 pending 请求池，可接持久化存储防止崩溃丢失
- 可配置超时，超时自动升级或中止
- 节点以 `waiting_approval` 持久化并**释放 Worker**，审批结果作为事件回来驱动状态机继续——不占用调度容量等待人工
- 回调解耦：HTTP 回调发送通知，HTTP Handler 接收响应

---

### 事件溯源与 Saga 补偿

#### 事件溯源 (`internal/event/`)

所有状态变更记录为不可变事件流：

- `Replay()` — 从头重放所有事件，重建当前状态
- `ReplayUntil(t)` — 重放到指定时点（调试 / 审计 / 时间旅行）

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

- **WAL 逻辑复制** (`internal/cdc/wal.go`)：基于 PostgreSQL 逻辑复制协议，自动创建 Replication Slot + Publication，流式接收 INSERT/UPDATE/DELETE，Standby Status 心跳保活
- **轮询回退** (`internal/cdc/postgres.go`)：WAL 不可用时（权限不足 / 版本不支持）自动降级为轮询模式
- **Trigger YAML** (`internal/cdc/trigger.go`)：CDC 事件 → YAML 条件匹配 → 触发工作流

---

### Cron 调度与时间轮

- **Cron 调度器**：标准 5 字段表达式解析 + 下次触发时间计算 + 分布式锁去重
- **4 层层级时间轮**：O(1) 添加/取消定时器，毫秒级精度，到期自动级联降层

```
Layer 0: 1ms  精度
Layer 1: 1s   精度
Layer 2: 1m   精度
Layer 3: 1h   精度
```

---

### Wasm 插件系统

基于 [wazero](https://github.com/tetratelabs/wazero)（纯 Go，无 CGO）的安全沙箱：

**沙箱约束：** 内存限制、执行超时、文件系统隔离、输出大小限制、WASI stdin/stdout 协议通信。

**插件管理：** 注册/版本管理/激活切换、SHA-256 校验（防篡改）、Pipeline 模式串行执行、运行时不支持时降级为内建实现。

---

### 可观测性

- **Prometheus 指标** (`/metrics`)：任务总数、任务延迟分位、活跃工作流、Worker 数、重试次数、队列深度
- **OpenTelemetry 追踪**：W3C `traceparent` 传播，Span 嵌套为 Workflow → Task → Tool Call，支持 OTLP 导出
- **eBPF 内核追踪**：kprobe 挂载 TCP 连接建立路径，测量连接延迟；非 Linux 平台编译为空实现
- **连续 Profiling**：CPU / Heap / Goroutine / Mutex / Block 五种 Profile，`/debug/profile` 实时获取
- **结构化日志**：`slog` 标准库，三后端（StdLog / JSON / Nop），自动附加 trace_id / span_id
- **Grafana Dashboard**：预置多面板看板

---

### 部署架构

- **Docker 镜像**：多阶段构建，最终镜像基于 `scratch`
- **Helm Chart**：Coordinator 为 StatefulSet（需持久化事件日志）+ Headless Service；Worker 为 Deployment + HPA；含 PodDisruptionBudget；独立的 dev / prod values
- **Kubernetes Gateway API**：GRPCRoute + HTTPRoute，原生支持 gRPC
- **Kueue GPU 调度**：通过 ResourceFlavor + ClusterQueue + LocalQueue 实现 GPU 任务排队与优先级抢占
- **CI/CD**：`ci.yml` 依次执行 **hygiene → lint → test → build**；`release.yml` 负责多架构镜像与 Helm chart 打包

---

## 快速开始

### 环境要求

| 组件 | 版本 | 用途 |
|------|------|------|
| Go | **1.26+** | 编译 |
| PostgreSQL | 15+ | 元数据存储、事件存储、CDC（需 `wal_level=logical`） |
| Node.js | 18+ | Dashboard 前端（可选） |
| etcd | 3.5+ | 分布式协调（可选，单机可用嵌入模式） |

### 编译

```bash
# 生成 protobuf 代码（首次构建必须先执行，生成目录不入库）
buf generate

# 编译
go build ./...

# 运行测试
go test ./...

# 编译 Dashboard 前端
cd web && npm install && npm run build
```

### 本地运行

```bash
# 启动依赖（PostgreSQL + etcd）
docker-compose -f deploy/docker-compose.yml up -d

# 启动 Coordinator (gRPC :50051, REST :8081, Metrics :9090)
go run ./cmd/coordinator

# 启动 Worker
go run ./cmd/worker

# 跑一次 ForgeX 通用用例
go run ./cmd/forgex run-demo --case generic-contract-success --root .forgex
```

### 配置

```bash
# 本地配置覆盖（已 gitignore）
cp conf/agent.toml conf/agent.local.toml

# 项目接入配置
cp projects/example-project.yaml projects/my-project.yaml
```

敏感信息通过环境变量注入：

| 环境变量 | 说明 |
|----------|------|
| `FORGE_PG_PASSWORD` | PostgreSQL 密码 |
| `FORGE_LLM_API_KEY` | LLM API 密钥（OpenAI 兼容格式） |
| `FORGE_PROJECT_CONFIG` | Git Worker 所用的项目配置路径 |
| `FORGE_MCP_ENDPOINT` | MCP Server 地址 |
| `FORGE_CORS_ORIGINS` | Dashboard 允许的 CORS 源 |

---

## 工作流定义

工作流用一份 YAML 声明触发方式与各阶段任务。下面是完整结构示例：

```yaml
apiVersion: forge/v1
kind: Workflow
metadata:
  name: bug_fix
  version: "1.0"

# 触发：声明式轮询 + 去重键（幂等）
triggers:
  - type: poll
    source: issue_tracker
    interval: 2m
    query: "status = 'open' AND type = 'bug'"
    dedup_key: "{{.event.work_item_id}}"

config:
  timeout: 30m
  max_retries: 2

inputs:
  work_item_id: "{{.event.work_item_id}}"

stages:
  # 同一阶段内的任务并行；节点之间不直接通信，
  # 依赖通过 output 命名 + {{...}} 引用交给 Coordinator 传递
  - name: investigate
    parallel: true
    tasks:
      - worker: mcp
        action: get_workitem
        params: { id: "{{.inputs.work_item_id}}" }
        output: bug_info
      - worker: git
        action: search
        params: { query: "{{.bug_info.keywords}}" }
        output: related_code
      - worker: database
        action: query_pg
        params: { sql: "SELECT * FROM errors WHERE id = '{{.inputs.work_item_id}}'" }
        output: db_context

  - name: analyze
    tasks:
      - worker: ai
        action: analyze
        params:
          prompt: "分析根因，给出修复建议"
          context: ["{{.bug_info}}", "{{.related_code}}", "{{.db_context}}"]
        output: analysis

  - name: plan
    tasks:
      - worker: ai
        action: generate_code_plan
        params: { analysis: "{{.analysis}}" }
        output: plan

  # 人工闸门：审批任务。`condition` 是任务级 CEL 表达式，为假时该任务标记 SKIPPED
  # 而不执行（下游照常解锁）。全局的 `hitl.auto_pause_on` 已删除——它从未接线。
  # 条件读已完成任务的命名产出，所以这里用 `results.<output 名>.<字段>`。
  - name: approve
    tasks:
      - worker: hitl
        action: request_approval
        params:
          message: "修复方案：{{.plan.summary}}"
          options: [approve, reject, modify]
        condition: "results.plan.confidence < 0.95"
      - worker: review
        action: review_plan
        params: { plan: "{{.plan}}" }

  - name: execute
    tasks:
      - worker: git
        action: create_branch
        params: { name: "fix/{{.inputs.work_item_id}}" }
      - worker: claude_code
        action: implement
        params: { plan: "{{.plan}}" }
      - worker: shell
        action: run_test
        params: { command: "go test ./..." }

  - name: deliver
    tasks:
      - worker: git
        action: push_and_mr
        params: { target: "{{.project.branching.test_target}}" }
      - worker: mcp
        action: add_comment
        params:
          id: "{{.inputs.work_item_id}}"
          content: "已提交 MR: {{.mr_url}}"
```

---

## 多语言 Worker SDK

### Python Worker

```python
from forge_sdk import ForgeWorker, Task, TaskResult

worker = ForgeWorker(coordinator_addr="localhost:50051")

@worker.handler("data_analysis")
def handle(task: Task) -> TaskResult:
    result = analyze(task.params["input"])
    return TaskResult(output={"report": result})

worker.start()  # 注册到 Coordinator 并开始接收任务
```

### C++ Worker

```cpp
#include "forge_worker.h"

class DataProcessor : public forge::TaskHandler {
    forge::TaskResult Execute(const forge::Task& task) override {
        auto input = task.GetParam("input_path");
        auto output = Process(input);
        return forge::TaskResult::Success({{"output_path", output}});
    }
};

int main() {
    forge::Worker worker("localhost:50051");
    worker.RegisterHandler("data_process", std::make_unique<DataProcessor>());
    worker.Start();  // 阻塞，持续接收任务
}
```

---

## 设计原则

| 原则 | 说明 |
|------|------|
| **YAML 驱动** | 工作流是声明式的、可版本控制的、支持热加载——改 YAML 即改流程 |
| **人工兜底** | AI 不会自动合并代码或部署——关键节点必须人工确认 |
| **约束在模型之外** | 审批、权限、停止条件、评测都发生在确定性控制面，不依赖模型自觉 |
| **插件架构** | 添加新 Worker 不需要改核心引擎——实现 Handler 接口即可接入 |
| **安全第一** | Shell 白名单、DB 只读、文件保护列表、Token 预算、注入检测 |
| **优雅失败** | Saga 补偿回滚、指数退避重试、事件重放恢复 |
| **可评测** | 每次失败都能沉淀为可回放的 case，而不是停在报告里 |
| **Parse Don't Validate** | 类型系统保证正确性，非法数据在入口就拒绝 |

---

## 项目规模

| 指标 | 数值 |
|------|------|
| Go 源码（非测试、非生成） | 约 **53,800 行** |
| Go 测试代码 | 约 **33,600 行** |
| 测试函数 | **1,360+** |
| 受版本控制文件 | **669** |
| 其中 ForgeX 控制面 | 见 `internal/forgex/`（24 子包） |
| TypeScript (Dashboard) | 见 `web/` |
| Python SDK | 见 `sdk/python/` |
| C++ SDK | 含 protobuf 生成代码 |
| Helm / Docker / K8s / CI 配置 | 见 `deploy/` 与 `.github/` |

---

## License

MIT License — 详见 [LICENSE](LICENSE)
