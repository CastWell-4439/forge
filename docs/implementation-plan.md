# Forge — AI 编码实施方案

> 项目代号：**Forge**（分布式任务调度引擎）
> 配套文档：`分布式任务调度引擎-技术方案.md`
> 执行方式：AI（Claude Code）逐 Phase 生成代码，人工 Review 后推进下一阶段
> 日期：2026-04-02

---

## 使用说明

### 工作模式

```
你下达 Phase N 启动指令
    ↓
AI 按任务清单逐项生成代码（5-8 个任务/Phase）
    ↓
AI 输出「Phase N 技术报告 + 评估结果」
    ↓
你 Review → 提出修改意见 / 确认通过
    ↓
你说"继续" → AI 启动 Phase N+1
```

### 约束规则

1. **严格按本文档执行**：AI 不得自行增减功能、不得修改技术选型、不得跳过任务项
2. **每个 Phase 完成后必须输出技术报告**：包含完成项清单、代码统计、测试结果、未解决问题
3. **人工 Review 是硬性门禁**：未获得"继续"指令前，AI 不得开始下一 Phase
4. **代码必须可编译/可运行**：每个 Phase 结束时项目必须处于可构建状态
5. **引用来源**：所有数据结构、接口定义、算法实现必须与技术方案文档保持一致

### 技术报告模板（每个 Phase 完成后输出）

```
## Phase X.Y 技术报告

### 完成项
- [ ] 逐项标注 ✅/❌

### 代码统计
- 新增/修改文件数：
- 新增代码行数（不含测试）：
- 新增测试用例数：

### 构建验证
- `go build ./...`：PASS/FAIL
- `go test ./...`：PASS/FAIL（N passed, M failed）

### 评估标准逐项验证
- E.X.1：PASS/FAIL — 说明
- E.X.2：PASS/FAIL — 说明
- ...

### 未解决问题
- （列出所有已知问题、临时跳过项、需要人工决策的点）

### 与技术方案的偏差
- （如果有任何实现与技术方案不一致的地方，在此说明原因）
```

---

## Phase 总览

| Phase | 名称 | 任务数 | 核心目标 | 状态 |
|-------|------|--------|----------|------|
| 1A | 项目骨架 + Proto + DAG 引擎 | 7 | 目录结构、gRPC 定义、DAG 解析和验证 | ✅ |
| 1B | 存储层 + 基础 Coordinator + Worker | 8 | PG/BoltDB 存储、单节点端到端跑通 | ✅ 19e4174 |
| 2A | 服务发现 + Leader 选举 + Worker 管理 | 6 | etcd 嵌入、心跳、故障检测 | ✅ 9460f4c |
| 2B | 调度算法 + 重试 + 超时 + 事件通知 | 7 | 4 种调度算法、指数退避、PG NOTIFY | ✅ fc21860 |
| 3A | Python Worker SDK | 6 | Python SDK 开发 + 测试 + 打包 | ✅ 544a15a |
| 3B | C++ Worker SDK + 多语言混合测试 | 6 | C++ SDK 开发 + 三语言混合路由验证 | ✅ 3af7c1c |
| 4A+4B | 事件溯源 + Saga + Cron + 时间轮 | 11 | 事件记录/回放、补偿事务、定时触发 | ✅ 2f3895c |
| 4C+5A | CDC 引擎 + Wasm + Docker + Helm | 14 | PG CDC、wazero 沙箱、容器化部署 | ✅ c88bee0 |
| 5B | Gateway API + Kueue + CI/CD | 6 | K8s 高级特性 + 自动化流水线 | ✅ 254aa33 |
| 6A | Metrics + Tracing + Profiling | 6 | OTel 四信号 + 跨语言传播 | ✅ b5549c7 + cef4836 |
| 6B | eBPF + NATS + Go 1.26 新特性 | 7 | cilium/ebpf、JetStream、iter.Seq/unique/slog | ✅ 30c2a20 |
| 6C | Admin Dashboard | 6 | gRPC-Gateway REST + React/D3.js | ✅ 58fa773 |
| **A1** | **Agent: Tool Worker 封装** | **6** | **18 个 Handler** | ✅ 6bbc241 |
| **A2** | **Agent: Agent Core** | **6** | **Parser/Planner/DAG/Session** | ✅ afb6525 |
| **AE-1** | **插件式重构 + Structured Output** | **4** | **Registry 合并 + M8 + ReAct** | ✅ e9ce698 |
| — | Bugfix: Task Output 持久化 | — | — | ✅ 53bebcd |
| — | Post-6C Review | — | mock同步/CountWorkflows/ListWorkers/CORS | ✅ cdc08f2 |
| — | Pre-AE2 修复 | — | extractJSON bug + float64→int + Registry合并 | ✅ 45f74b3 |
| **AE-1G** | **架构治理前置** | **4** | **ARCHITECTURE.md + lint + 编码约定** | ⬜ 待开始 |
| **AE-2** | **MCP 协议 + 通用工具 + Agent.Run** | **7** | **Layer 1 Agent 引擎** | ⬜ 待开始 |
| **AE-3** | **RAG + Memory** | **8** | **知识检索 + 记忆系统** | ⬜ |
| **AE-4** | **Guardrails + Checkpoint + Reflexion** | **7** | **安全 + 持久化 + 自纠错** | ⬜ |
| V2-1~10 | Layer 2 自动化框架 | ~60 | 工作流 YAML/Registry/Scheduler/HITL/Workers | ⬜ |

**代码统计（截至 AE-1）**：~10,700+ 行源码 + ~4,800+ 行测试，27 commits

---

## Phase 1A：项目骨架 + Proto + DAG 引擎

### 前置条件
- Go 1.26+ 已安装
- `buf` CLI 1.67+ 已安装

### 任务清单

| 编号 | 任务 | 产出文件/目录 | 对应技术方案章节 |
|------|------|---------------|------------------|
| 1A.1 | 初始化 Go module，创建完整目录结构（cmd/、internal/、api/proto/、sdk/、deploy/ 等） | `go.mod`、目录树 | 附录 A |
| 1A.2 | 编写 Makefile（build、test、lint、proto-gen 目标） | `Makefile` | 3.9 |
| 1A.3 | 编写 common.proto（公共消息：TaskStatus、WorkflowStatus、Error 等） | `api/proto/common.proto` | 5.4 |
| 1A.4 | 编写 coordinator.proto（SubmitWorkflow、GetWorkflow、ListWorkflows 等） | `api/proto/coordinator.proto` | 4.3 |
| 1A.5 | 编写 worker.proto（Register、Heartbeat、ExecuteTask） | `api/proto/worker.proto` | 5.3 |
| 1A.6 | 配置 buf 生成 Go gRPC 代码 | `buf.yaml`、`buf.gen.yaml`、生成产出 | 3.1 |
| 1A.7 | 实现 DAG 数据结构 + YAML 解析 + 验证（环检测、孤立节点、超时合理性） | `internal/coordinator/dag.go`、`dag_test.go` | 5.1、7.1 |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| E1A.1 | 目录完整 | 目录结构与技术方案附录 A 的一级/二级目录一致 |
| E1A.2 | Proto 生成 | `make proto-gen`（或 `buf generate`）成功，生成的 Go 代码无编译错误 |
| E1A.3 | 项目可构建 | `go build ./...` 零错误 |
| E1A.4 | DAG 解析 | 能正确解析技术方案 5.1 中的 video-production YAML 示例 |
| E1A.5 | 环检测 | 输入含环的 DAG 定义，返回 "DAG contains cycle" 错误 |
| E1A.6 | 拓扑排序 | 线性 DAG（A→B→C）输出 [A, B, C]；扇出 DAG（A→B+C→D）输出 A 在前、D 在后 |

---

## Phase 1B：存储层 + 基础 Coordinator + Worker

### 前置条件
- Phase 1A Review 通过
- PostgreSQL 可连接

### 任务清单

| 编号 | 任务 | 产出文件/目录 | 对应技术方案章节 |
|------|------|---------------|------------------|
| 1B.1 | 定义 Storage 接口（SaveWorkflow、SaveTask、ClaimTask、UpdateTaskStatus、GetWorkflowHistory） | `internal/storage/interface.go` | 3.4 |
| 1B.2 | 编写 SQL 建表脚本（workflow_definitions、workflow_instances、task_instances、events、cron_triggers） | `deploy/migrations/001_init.sql` | 6.1 |
| 1B.3 | 实现 PostgreSQL Storage（含 ClaimTask 的 FOR UPDATE SKIP LOCKED） | `internal/storage/postgres.go` | 3.4、7.3 |
| 1B.4 | 实现 BoltDB Storage（单机模式备选） | `internal/storage/boltdb.go` | 3.4 |
| 1B.5 | 实现基础 Coordinator（接收工作流 → 解析 DAG → 创建任务实例 → 推进状态机） | `internal/coordinator/coordinator.go` | 4.3 |
| 1B.6 | 实现 Go Worker 基础框架（注册 handler、接收任务、执行、回传结果） | `internal/worker/worker.go`、`executor.go`、`handler.go` | 5.3 |
| 1B.7 | 实现 CLI 入口（`forge coordinator`、`forge worker`、`forge standalone`） | `cmd/forge/main.go` | 9.2 |
| 1B.8 | 编写端到端集成测试（提交 A→B→C 线性工作流，验证全部 COMPLETED） | `test/integration_test.go` | — |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| E1B.1 | PG 连通 | PG Storage 能完成建表、写入、查询 |
| E1B.2 | ClaimTask | 多个 Worker 并发 ClaimTask，每个任务只被一个 Worker 认领（SKIP LOCKED 生效） |
| E1B.3 | BoltDB 可替代 | 切换为 BoltDB 后，同一个线性工作流能跑通 |
| E1B.4 | 端到端 | 提交 3 节点线性 DAG（A→B→C），三个任务依次完成，工作流状态为 COMPLETED |
| E1B.5 | 单元测试 | `go test ./...` 全部 PASS |
| E1B.6 | Lint | `golangci-lint run` 无 error 级别告警 |

---

## Phase 2A：服务发现 + Leader 选举 + Worker 管理

### 前置条件
- Phase 1B Review 通过
- Redis 可连接

### 任务清单

| 编号 | 任务 | 产出文件/目录 | 对应技术方案章节 |
|------|------|---------------|------------------|
| 2A.1 | 定义 Coordinator 发现接口（LeaderElect、Register、Watch、Lock） | `internal/discovery/interface.go` | 3.3 |
| 2A.2 | 实现嵌入式 etcd 服务发现 | `internal/discovery/etcd.go` | 3.3 |
| 2A.3 | 实现 Leader 选举逻辑（etcd Election API） | `internal/coordinator/leader.go` | 3.3 |
| 2A.4 | 实现 Worker 注册/注销（etcd Watch 自动发现） | `internal/coordinator/worker_manager.go` | 5.3 |
| 2A.5 | 实现 gRPC 双向流心跳（Ping/Pong + 状态上报） | `internal/worker/heartbeat.go` | 5.3 |
| 2A.6 | 实现 Worker 故障检测（3 次 Ping 无响应→SUSPECT，60s→DEAD，任务重调度） | `internal/coordinator/worker_manager.go` | 5.3 |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| E2A.1 | Leader 选举 | 启动 3 个 Coordinator，有且仅有 1 个成为 Leader |
| E2A.2 | Leader 切换 | kill Leader 进程后，30s 内新 Leader 选出 |
| E2A.3 | Worker 注册 | Worker 启动后自动出现在 Coordinator 的 Worker 列表中 |
| E2A.4 | 心跳正常 | Worker 心跳间隔 ~10s，Coordinator 能接收到状态信息 |
| E2A.5 | 故障检测 | kill Worker 进程后，60s 内被标记为 DEAD |
| E2A.6 | 任务重调度 | DEAD Worker 上的 RUNNING 任务被重新分配给存活 Worker |

---

## Phase 2B：调度算法 + 重试 + 超时 + 事件通知

### 前置条件
- Phase 2A Review 通过

### 任务清单

| 编号 | 任务 | 产出文件/目录 | 对应技术方案章节 |
|------|------|---------------|------------------|
| 2B.1 | 定义 Scheduler 接口 | `internal/coordinator/scheduler.go` | 5.2 |
| 2B.2 | 实现加权轮询（WRR）调度算法 | `internal/coordinator/scheduler_wrr.go` | 5.2 |
| 2B.3 | 实现最少活跃任务（Least Active）调度算法 | `internal/coordinator/scheduler_least.go` | 5.2 |
| 2B.4 | 实现一致性哈希调度算法 | `internal/coordinator/scheduler_hash.go` | 5.2 |
| 2B.5 | 实现 Label Selector 任务亲和性（matchLabels） | `internal/coordinator/scheduler.go` | 5.2 |
| 2B.6 | 实现指数退避重试（带 Full Jitter） + 死信处理 | `internal/coordinator/retry.go` | 7.2 |
| 2B.7 | 实现任务级/工作流级超时控制 + PG LISTEN/NOTIFY 事件通知 | `internal/coordinator/timeout.go`、`internal/bus/pg_notify.go` | 3.5 |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| E2B.1 | WRR | 权重 2:1 的两个 Worker，分配比例接近 2:1 |
| E2B.2 | Least Active | 任务分配给当前活跃任务最少的 Worker |
| E2B.3 | Label 亲和 | 带 `gpu: true` 标签的任务只分配到有对应标签的 Worker |
| E2B.4 | 重试 | 任务失败后按指数退避重试，间隔递增，不超过 max_attempts |
| E2B.5 | 超时 | 任务超过 timeout 后被标记 FAILED，触发重试或工作流失败 |
| E2B.6 | DAG 并行 | 扇出 DAG（A→B+C→D）中 B 和 C 并行分配到不同 Worker 执行 |
| E2B.7 | PG NOTIFY | 新任务就绪时通过 NOTIFY 通知调度器，而非纯轮询 |

---

## Phase 3A：Python Worker SDK

### 前置条件
- Phase 2B Review 通过
- Python 3.10+、pip 已安装

### 任务清单

| 编号 | 任务 | 产出文件/目录 | 对应技术方案章节 |
|------|------|---------------|------------------|
| 3A.1 | 配置 buf 生成 Python gRPC 代码 | `buf.gen.yaml` 更新、`sdk/python/forge_sdk/generated/` | 3.1 |
| 3A.2 | 实现 Python Worker 类（连接 Coordinator、自动注册、心跳循环） | `sdk/python/forge_sdk/worker.py` | 3.1 |
| 3A.3 | 实现 @task_handler 装饰器（注册 handler、参数解析、结果回传） | `sdk/python/forge_sdk/decorators.py` | 3.1 |
| 3A.4 | Python SDK 打包配置（pyproject.toml、README） | `sdk/python/pyproject.toml` | 3.1 |
| 3A.5 | 编写 Python Worker 示例（模拟 AI 推理任务） | `examples/python-worker/` | 3.1 |
| 3A.6 | 编写 Python SDK 测试（Worker 注册、心跳、任务执行、错误处理） | `sdk/python/tests/` | — |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| E3A.1 | 安装 | `pip install ./sdk/python` 成功，无依赖冲突 |
| E3A.2 | 注册 | Python Worker 启动后在 Coordinator Worker 列表中可见 |
| E3A.3 | 心跳 | Python Worker 持续发送心跳，Coordinator 正常接收 |
| E3A.4 | 任务执行 | 提交 `handler: ai.generate` 的任务，Python Worker 正确执行并返回结果 |
| E3A.5 | 错误处理 | Python 任务抛异常后，Coordinator 收到 FAILED 状态和错误信息 |

---

## Phase 3B：C++ Worker SDK + 多语言混合测试

### 前置条件
- Phase 3A Review 通过
- CMake 3.20+、gRPC C++ 可用（通过 vcpkg 或系统包）

### 任务清单

| 编号 | 任务 | 产出文件/目录 | 对应技术方案章节 |
|------|------|---------------|------------------|
| 3B.1 | 配置 buf 生成 C++ gRPC 代码 | `buf.gen.yaml` 更新、`sdk/cpp/generated/` | 3.1 |
| 3B.2 | 实现 C++ Worker SDK（Worker 类、TaskHandler 基类、gRPC client、心跳线程） | `sdk/cpp/include/forge/`、`sdk/cpp/src/` | 3.1 |
| 3B.3 | C++ SDK CMake 构建 + vcpkg 配置 | `sdk/cpp/CMakeLists.txt`、`sdk/cpp/vcpkg.json` | 3.1 |
| 3B.4 | 编写 C++ Worker 示例（模拟高性能计算任务） | `examples/cpp-worker/` | 3.1 |
| 3B.5 | Go Worker SDK 对外封装（从 internal 提取为 `sdk/go/` 独立 module） | `sdk/go/` | 3.1 |
| 3B.6 | 多语言混合调度集成测试 + 更新 docker-compose | `test/multilang_test.go`、`deploy/docker-compose.yml` | 9.1 |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| E3B.1 | C++ 编译 | `cmake --build` 成功，无编译错误 |
| E3B.2 | C++ 注册 | C++ Worker 启动后在 Coordinator 可见，心跳正常 |
| E3B.3 | C++ 任务执行 | 提交 `handler: video.render` 的任务，C++ Worker 正确执行 |
| E3B.4 | 混合路由 | 同时运行 Go/Python/C++ Worker，不同 handler 路由到正确语言的 Worker |
| E3B.5 | docker-compose | `docker-compose up` 拉起全套服务（Coordinator + 3 语言 Worker + PG + Redis），健康检查通过 |

---

## Phase 4A：事件溯源 + Saga 补偿

### 前置条件
- Phase 3B Review 通过

### 任务清单

| 编号 | 任务 | 产出文件/目录 | 对应技术方案章节 |
|------|------|---------------|------------------|
| 4A.1 | 实现事件存储（Event 写入 PG events 表 + 按 workflow_id 查询） | `internal/event/store.go` | 5.4、6.1 |
| 4A.2 | 实现事件回放（从事件序列重建 WorkflowState） | `internal/event/replay.go` | 5.4 |
| 4A.3 | 改造 Coordinator：所有状态变更先写事件，再更新状态 | `internal/coordinator/coordinator.go` 改造 | 5.4 |
| 4A.4 | DAG 定义支持 `compensate` 字段 | `internal/coordinator/dag.go` 更新 | 5.5 |
| 4A.5 | 实现 Saga 补偿器（失败时按拓扑逆序执行补偿操作） | `internal/saga/compensator.go` | 5.5 |
| 4A.6 | 编写测试（事件回放一致性、Saga A→B→C(fail) 补偿链） | `test/event_test.go`、`test/saga_test.go` | — |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| E4A.1 | 事件记录 | 工作流执行过程中所有状态变更出现在 events 表中 |
| E4A.2 | 事件回放 | 从 events 重建的 WorkflowState 与 workflow_instances 实际状态一致 |
| E4A.3 | Saga 正序 | 工作流 A→B→C 中 C 失败后，触发补偿流程 |
| E4A.4 | Saga 逆序 | 补偿执行顺序为 B.compensate → A.compensate（拓扑逆序） |
| E4A.5 | 全量测试 | `go test ./...` 全部 PASS |

---

## Phase 4B：Cron 调度 + 时间轮

### 前置条件
- Phase 4A Review 通过

### 任务清单

| 编号 | 任务 | 产出文件/目录 | 对应技术方案章节 |
|------|------|---------------|------------------|
| 4B.1 | 实现 CronTrigger 数据结构 + PG 持久化（cron_triggers 表已在 Phase 1B 建好） | `internal/coordinator/cron.go` | 5.6 |
| 4B.2 | 实现 Cron 表达式解析 + 触发逻辑 | `internal/coordinator/cron.go` | 5.6 |
| 4B.3 | 实现分布式 Cron 去重（etcd Lock 确保多 Coordinator 只触发一次） | `internal/coordinator/cron.go` | 5.6 |
| 4B.4 | 实现时间轮算法（TimingWheel + 层级溢出） | `internal/coordinator/timingwheel.go` | 7.4 |
| 4B.5 | 编写测试（Cron 触发频率、去重验证、时间轮精度） | `test/cron_test.go` | — |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| E4B.1 | Cron 触发 | 配置 `*/1 * * * *` 触发器后，每分钟自动创建一个工作流实例 |
| E4B.2 | Cron 去重 | 3 个 Coordinator 运行，同一 Cron 在同一分钟内只触发一次 |
| E4B.3 | MisfirePolicy | SKIP 策略下，错过的触发不补执行 |
| E4B.4 | 时间轮 | 1000 个延迟任务的触发误差 < 100ms |

---

## Phase 4C：CDC 引擎 + Wasm 插件

### 前置条件
- Phase 4B Review 通过

### 任务清单

| 编号 | 任务 | 产出文件/目录 | 对应技术方案章节 |
|------|------|---------------|------------------|
| 4C.1 | 定义 CDC Source 接口 | `internal/cdc/interface.go` | 5.9 |
| 4C.2 | 实现 PG CDC（Logical Replication + WAL 解析） | `internal/cdc/postgres.go` | 5.9 |
| 4C.3 | 实现 CDC 触发器配置（YAML 定义 table/event/filter → 触发指定工作流） | `internal/cdc/trigger.go` | 5.9 |
| 4C.4 | 集成 wazero 运行时 + 沙箱配置（内存限制、超时、无文件系统） | `internal/wasm/executor.go`、`sandbox.go` | 5.7 |
| 4C.5 | 实现插件注册/版本管理 | `internal/wasm/registry.go` | 5.7 |
| 4C.6 | DAG 定义支持 `handler: wasm` + 编写示例 Wasm 插件（Go → tinygo → .wasm） | `internal/coordinator/dag.go` 更新、`plugins/transform/` | 5.7 |
| 4C.7 | 编写测试（CDC 触发、Wasm 执行、Wasm 隔离验证） | `test/cdc_test.go`、`test/wasm_test.go` | — |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| E4C.1 | CDC 监听 | 向 PG 表 INSERT 一行后，Forge 在 5s 内自动触发对应工作流 |
| E4C.2 | CDC 过滤 | 配置 `filter: "status = 'pending'"` 后，只有匹配行触发工作流 |
| E4C.3 | Wasm 执行 | 提交 `handler: wasm` 的任务，.wasm 在沙箱中执行并返回正确结果 |
| E4C.4 | Wasm 隔离 | Wasm 插件尝试访问文件系统时被拒绝 |
| E4C.5 | Wasm 超时 | 死循环 Wasm 插件在 timeout 后被强制终止 |
| E4C.6 | 全量测试 | `go test ./...` 全部 PASS |

---

## Phase 5A：Docker 镜像 + Helm Chart

### 前置条件
- Phase 4C Review 通过
- Docker 已安装
- kubectl、helm 已安装
- K8s 集群可用（minikube / kind 均可）

### 任务清单

| 编号 | 任务 | 产出文件/目录 | 对应技术方案章节 |
|------|------|---------------|------------------|
| 5A.1 | Coordinator 多阶段 Dockerfile（最终镜像 < 20MB） | `build/coordinator/Dockerfile` | 3.9 |
| 5A.2 | Go Worker Dockerfile | `build/worker-go/Dockerfile` | 3.9 |
| 5A.3 | Python Worker Dockerfile | `build/worker-python/Dockerfile` | 3.9 |
| 5A.4 | C++ Worker Dockerfile | `build/worker-cpp/Dockerfile` | 3.9 |
| 5A.5 | Helm Chart 骨架（Chart.yaml、values.yaml、values-dev.yaml、values-prod.yaml） | `deploy/helm/forge/` | 9.3.5 |
| 5A.6 | Helm 模板：Coordinator StatefulSet + Headless Service + PDB | `deploy/helm/forge/templates/coordinator-*.yaml` | 9.3.2 |
| 5A.7 | Helm 模板：Worker Deployment（Go/Python/C++）+ HPA + Service | `deploy/helm/forge/templates/worker-*.yaml`、`hpa.yaml` | 9.3.3 |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| E5A.1 | 镜像构建 | 4 个 Dockerfile 均能成功 `docker build` |
| E5A.2 | Go 镜像大小 | Coordinator 镜像 < 20MB |
| E5A.3 | Helm 语法 | `helm lint ./deploy/helm/forge` 无错误 |
| E5A.4 | Helm 部署 | `helm install` 在 K8s 集群中成功创建 StatefulSet + Deployment + Service |
| E5A.5 | Pod 运行 | Coordinator Pod 和 Worker Pod 均达到 Running 状态 |

---

## Phase 5B：Gateway API + Kueue + CI/CD

### 前置条件
- Phase 5A Review 通过

### 任务清单

| 编号 | 任务 | 产出文件/目录 | 对应技术方案章节 |
|------|------|---------------|------------------|
| 5B.1 | Gateway API 配置（Gateway + GRPCRoute + HTTPRoute） | `deploy/helm/forge/templates/gateway.yaml` | 9.3.6 |
| 5B.2 | Kueue CRD 配置（ResourceFlavor、ClusterQueue、LocalQueue） | `deploy/kueue/` | 9.3.7 |
| 5B.3 | Coordinator Kueue 提交逻辑（GPU 任务 → K8s Job + Kueue 标签） | `internal/coordinator/kueue.go` | 9.3.7 |
| 5B.4 | GitHub Actions CI workflow（lint + test + build） | `.github/workflows/ci.yml` | 9.3.8 |
| 5B.5 | GitHub Actions Release workflow（多架构构建 + push） | `.github/workflows/release.yml` | 9.3.8 |
| 5B.6 | Forge Operator 脚手架（ForgeCluster CRD 类型定义 + 基础 controller） | `operator/` | 9.3.4 |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| E5B.1 | Gateway API | GRPCRoute 和 HTTPRoute 语法正确，`kubectl apply` 成功 |
| E5B.2 | Kueue 配置 | Kueue CRD 可 apply（若集群安装了 Kueue）；配置符合技术方案 9.3.7 |
| E5B.3 | CI workflow | GitHub Actions CI 配置语法正确（`act` 本地验证或人工审查） |
| E5B.4 | Operator 脚手架 | CRD 类型定义可编译，controller 有空的 Reconcile 方法 |

---

## Phase 6A：Metrics + Tracing + Profiling

### 前置条件
- Phase 5B Review 通过

### 任务清单

| 编号 | 任务 | 产出文件/目录 | 对应技术方案章节 |
|------|------|---------------|------------------|
| 6A.1 | 实现 Prometheus 指标（技术方案 8.1 中 6 个 metric） | `internal/observability/metrics.go` | 8.1 |
| 6A.2 | Coordinator/Worker 暴露 `/metrics` HTTP 端点 | 各组件 main.go 更新 | 8.1 |
| 6A.3 | 编写 Prometheus 采集配置 + Grafana Dashboard JSON（6 个面板） | `deploy/prometheus.yml`、`deploy/grafana/dashboards/forge.json` | 8.1、8.3 |
| 6A.4 | 集成 OTel Tracing SDK（工作流→trace，任务→span） | `internal/observability/tracing.go` | 8.2 |
| 6A.5 | 跨语言 Context Propagation（trace ID 传递到 Python/C++ Worker） | 各 SDK + Worker 更新 | 8.2 |
| 6A.6 | 集成 OTel Continuous Profiling（CPU/Heap/Goroutine/Mutex/Block） | `internal/observability/profiling.go` | 5.8 |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| E6A.1 | Metrics 可访问 | `curl :9090/metrics` 返回 `forge_*` 指标 |
| E6A.2 | Tracing 可见 | Jaeger UI 中能查到工作流 trace，含多个 task span |
| E6A.3 | 跨语言 Trace | Python/C++ Worker 的 task span 出现在同一条 trace 中 |
| E6A.4 | Profiling 数据 | OTel Profiling exporter 能输出 profile 数据（验证日志/输出即可） |
| E6A.5 | Grafana Dashboard | 导入 JSON 后面板能渲染（有数据或空面板不报错） |

---

## Phase 6B：eBPF + NATS + Go 1.23 新特性

### 前置条件
- Phase 6A Review 通过

### 任务清单

| 编号 | 任务 | 产出文件/目录 | 对应技术方案章节 |
|------|------|---------------|------------------|
| 6B.1 | 编写 TCP 延迟追踪 BPF 程序（C） | `bpf/tcp_latency.c`、`bpf/Makefile` | 5.8 |
| 6B.2 | Go 侧加载 eBPF 程序 + perf buffer 读取 + 导出 Prometheus 指标 | `internal/observability/ebpf.go` | 5.8 |
| 6B.3 | eBPF 模块条件编译（`//go:build ebpf`，默认不编译） | 条件编译 | 11（风险应对） |
| 6B.4 | 实现 NATS JetStream 消息总线后端（发布/订阅任务） | `internal/bus/nats.go` | 3.6 |
| 6B.5 | 实现 NATS KV Store Worker 心跳后端 | `internal/cache/nats_kv.go` | 3.6 |
| 6B.6 | 重构 DAG 遍历为 `iter.Seq` 迭代器 + Worker 注册用 `unique.Handle` | `internal/coordinator/dag.go`、`worker_manager.go` | 7.5 |
| 6B.7 | 日志层增加 `log/slog` 适配器（与 zerolog 可切换） | `internal/observability/logger.go` | 7.5 |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| E6B.1 | eBPF 编译 | `make -C bpf/` 成功生成 .o 字节码（需 clang + Linux headers） |
| E6B.2 | eBPF 可选 | 默认 `go build` 不包含 eBPF；`go build -tags ebpf` 包含 |
| E6B.3 | NATS 切换 | `--bus=nats` 启动后，任务分发走 NATS JetStream |
| E6B.4 | NATS KV 心跳 | `--heartbeat-backend=nats` 启动后，Worker 心跳走 NATS KV Store |
| E6B.5 | iter.Seq | `range dag.TopologicalOrder()` 语法正确编译和运行 |
| E6B.6 | slog 切换 | `--logger=slog` 启动后日志走 `log/slog` 输出 |
| E6B.7 | 全量测试 | `go test ./...` 全部 PASS |

---

## Phase 6C：Admin Dashboard

### 前置条件
- Phase 6B Review 通过
- Node.js 18+ 已安装

### 任务清单

| 编号 | 任务 | 产出文件/目录 | 对应技术方案章节 |
|------|------|---------------|------------------|
| 6C.1 | 初始化 React + Ant Design Pro 项目 | `web/package.json`、`web/tsconfig.json` | 5.10 |
| 6C.2 | 实现 Overview 页面（活跃工作流数、Worker 数、成功率、队列深度） | `web/src/pages/overview/` | 5.10 |
| 6C.3 | 实现工作流列表页（状态筛选、搜索、分页） | `web/src/pages/workflows/list.tsx` | 5.10 |
| 6C.4 | 实现 DAG 可视化组件（D3.js，节点实时状态着色） | `web/src/components/dag-visualizer/` | 5.10 |
| 6C.5 | 实现 Worker 拓扑页（节点列表、语言类型标签、负载、健康状态） | `web/src/pages/workers/` | 5.10 |
| 6C.6 | 连接 gRPC-Gateway REST API（Coordinator 的 :8081 端口） | `web/src/services/api.ts` | 5.10 |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| E6C.1 | 前端启动 | `cd web && npm install && npm run dev` 成功启动，无编译错误 |
| E6C.2 | Overview | Overview 页面展示工作流和 Worker 统计数据（可以是 mock 数据） |
| E6C.3 | 工作流列表 | 能从 API 加载并展示工作流列表，支持分页 |
| E6C.4 | DAG 渲染 | 选中一个工作流后，能渲染出 DAG 图，节点按状态着色 |
| E6C.5 | Worker 页 | 能展示 Worker 列表，区分 Go/Python/C++ 类型 |

---

## Phase 7：文档 + 示例 + 最终打磨

### 前置条件
- Phase 6C Review 通过

### 任务清单

| 编号 | 任务 | 产出文件/目录 | 对应技术方案章节 |
|------|------|---------------|------------------|
| 7.1 | 编写 README.md（项目介绍、架构图、Feature 列表、Quick Start、技术栈） | `README.md` | 1.1、1.3 |
| 7.2 | 编写 Getting Started 指南（安装 → 启动 → 提交第一个工作流） | `docs/getting-started.md` | — |
| 7.3 | 编写 Python SDK 文档 + C++ SDK 文档 | `docs/sdk-python.md`、`docs/sdk-cpp.md` | 3.1 |
| 7.4 | 编写 4 个完整示例（simple-workflow、video-production、cdc-trigger、wasm-plugin） | `examples/` | 附录 A |
| 7.5 | 编写架构设计文档 + API Reference | `docs/architecture.md`、`docs/api-reference.md` | 4、5 |
| 7.6 | 代码清理（去除 TODO/FIXME、统一错误处理、补全注释）+ `golangci-lint` 全量修复 | 全局 | — |
| 7.7 | 创建 CLAUDE.md + 打 `v1.0.0` tag | `CLAUDE.md`、`git tag v1.0.0` | — |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| E7.1 | README 完整 | 包含项目介绍、架构图、Feature 列表、Quick Start、技术栈 |
| E7.2 | 示例可运行 | 4 个 examples 各自有独立 README，按步骤可跑通 |
| E7.3 | 零 lint 告警 | `golangci-lint run` 零 error |
| E7.4 | 全量测试 | `go test ./...` 全部 PASS |
| E7.5 | docker-compose 一键 | `docker-compose up` 拉起全套，提交示例工作流成功 |
| E7.6 | v1.0.0 tag | Git tag 存在且指向正确 commit |

---

## Phase A1：Agent — Tool Worker 封装

> 配套技术方案：`agent-tech-spec.md` 第 4 节（Tool 体系设计）

### 前置条件
- Forge Phase 2A/2B Review 通过（服务发现 + 调度算法可用）
- FaceFusion HTTP API 可调用
- TTS 服务可调用
- FFmpeg 已安装

### 任务清单

| 编号 | 任务 | 产出文件/目录 | 对应 agent-tech-spec 章节 |
|------|------|---------------|--------------------------|
| A1.1 | 封装 `media.download` / `media.upload` Worker handler | `internal/agent/workers/media_handler.go` | 4.2 |
| A1.2 | 封装 `video.probe` + `video.preprocess` handler（复用 FaceFusion bug 修复的编码探测逻辑） | `internal/agent/workers/video_handler.go` | 4.2 |
| A1.3 | 封装 `ai.face_swap` handler（封装 FaceFusion HTTP API 调用） | `internal/agent/workers/ai_handler.go` | 4.2、4.3 |
| A1.4 | 封装 `ai.tts` + `ai.script` + `ai.subtitle_gen` handler | `internal/agent/workers/ai_handler.go` | 4.2 |
| A1.5 | 封装 `video.encode` / `video.trim` / `video.concat` / `video.subtitles` / `audio.mix` / `audio.bgm_select` handler | `internal/agent/workers/ffmpeg_handler.go` | 4.2 |
| A1.6 | 编写集成测试：提交手写 DAG（换脸+配音流水线）→ Forge 执行 → 验证产物文件生成 | `test/agent_worker_test.go` | — |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| EA1.1 | Handler 注册 | 所有 18 个 handler（见 agent-tech-spec 4.2 表格）在 Worker 启动时注册成功 |
| EA1.2 | media.download | 给定 URL，成功下载到 `/tmp/forge/{workflow_id}/` 目录 |
| EA1.3 | video.probe | 对标准 MP4 和非标准编码视频均能返回正确的探测结果 |
| EA1.4 | ai.face_swap | 调用 FaceFusion API，输出换脸视频文件存在且可播放 |
| EA1.5 | 端到端 DAG | 手写 3-task DAG（download → probe → preprocess），通过 Forge 执行，三个任务依次完成 |
| EA1.6 | 全量测试 | `go test ./...` 全部 PASS |

---

## Phase A2：Agent — Agent Core

> 配套技术方案：`agent-tech-spec.md` 第 3 节（Agent 层设计）、第 5 节（DAG 生成引擎）

### 前置条件
- Phase A1 Review 通过
- LLM API 可调用（OpenAI / Anthropic）

### 任务清单

| 编号 | 任务 | 产出文件/目录 | 对应 agent-tech-spec 章节 |
|------|------|---------------|--------------------------|
| A2.1 | 实现 `ToolRegistry`（工具注册、描述、JSON Schema、FormatForPrompt 输出供 LLM 使用） | `internal/agent/tools/registry.go` | 4.1 |
| A2.2 | 实现 `RequirementParser`（LLM 需求解析，输出 VideoRequirement 结构体） | `internal/agent/parser.go` | 3.2 |
| A2.3 | 实现 `TaskPlanner`（模板匹配 + LLM 生成 DAG + DAG 验证 + LLM 自修正） | `internal/agent/planner.go` | 3.3 |
| A2.4 | 实现 `DAGGenerator`（三策略：模板 → LLM+验证 → 降级兜底）+ `DAGValidator`（4 层校验：格式清洗、Schema、语义、参数） | `internal/agent/dag_gen.go`、`internal/agent/dag_validate.go` | 5.1、5.3 |
| A2.5 | 实现 `Session` 管理 + Agent 状态机（8 种状态：idle/parsing/planning/executing/checking/fixing/completed/failed + 验证转换）+ `ForgeClient`（提交 DAG + 轮询状态） | `internal/agent/session.go`、`internal/agent/forge_client.go` | 3.4、9.1 |
| A2.6 | 端到端测试：自然语言输入 → RequirementParser → TaskPlanner → DAG 生成 → Forge 执行 → 任务完成 | `test/agent_e2e_test.go` | — |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| EA2.1 | ToolRegistry | 18 个工具注册完成，FormatForPrompt 输出的文本包含所有工具名称和描述 |
| EA2.2 | 需求解析 | 输入"帮我做一个30秒换脸视频配BGM"，输出 VideoRequirement 包含 FaceSwap + BGM 字段 |
| EA2.3 | 模板匹配 | 简单换脸+配音需求命中 FaceSwapWithTTSTemplate 模板 |
| EA2.4 | LLM DAG 生成 | 复杂非标需求走 LLM 路径，生成的 DAG 通过 4 层校验 |
| EA2.5 | 状态机 | Session 按 idle → parsing → planning → executing → completed 正确流转，非法转换被拒绝 |
| EA2.6 | 端到端 | 自然语言输入到 Forge DAG 提交成功（可用 mock Worker） |

---

## Phase A3：Agent — 质量检查 + Reflection

> 配套技术方案：`agent-tech-spec.md` 第 6 节（质量回路）

### 前置条件
- Phase A2 Review 通过

### 任务清单

| 编号 | 任务 | 产出文件/目录 | 对应 agent-tech-spec 章节 |
|------|------|---------------|--------------------------|
| A3.1 | 封装 `quality.video_check` / `quality.face_check` Worker handler（FFprobe 检查 + 人脸相似度） | `internal/agent/workers/quality_handler.go` | 4.2、6.2 |
| A3.2 | 实现 `QualityChecker`（多维度质量评估：分辨率、时长、音画同步、换脸相似度；QualityMetric 接口 + CheckResult） | `internal/agent/quality_checker.go` | 6.1、6.2 |
| A3.3 | 实现修正 DAG 生成（`PlanFix`）：分析质量检查失败原因，生成最小化修正 DAG，只重做受影响步骤 | `internal/agent/planner.go` 扩展 | 6.3 |
| A3.4 | 实现 Reflection 循环：检查 → 判定 → 通过/修正，最多 N 次重试（默认 3），超限标记失败 | `internal/agent/session.go` 扩展 | 3.1（Phase 3: Reflect） |
| A3.5 | 编写测试：模拟低质量产物输入（分辨率不对、换脸相似度低），验证自动检测 + 触发修正 DAG | `test/agent_quality_test.go` | — |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| EA3.1 | quality.video_check | 对正常视频返回 PASS，对分辨率不符视频返回 FAIL + 原因 |
| EA3.2 | quality.face_check | 换脸相似度 > 0.8 返回 PASS，< 0.5 返回 FAIL |
| EA3.3 | QualityChecker | 多指标综合评估，输出 CheckResult 含各维度得分和总体 pass/fail |
| EA3.4 | PlanFix | 输入"分辨率不符"错误，生成的修正 DAG 只包含 video.encode（不重做换脸等步骤） |
| EA3.5 | Reflection 循环 | 质量检查失败 → 自动修正 → 再次检查 → 通过；超过 3 次标记 failed |

---

## Phase A4：Agent — API + 对话管理

> 配套技术方案：`agent-tech-spec.md` 第 8 节（API 设计）

### 前置条件
- Phase A3 Review 通过

### 任务清单

| 编号 | 任务 | 产出文件/目录 | 对应 agent-tech-spec 章节 |
|------|------|---------------|--------------------------|
| A4.1 | 定义 `agent.proto`（AgentService: CreateSession / SendMessage streaming / GetSession / CancelSession，15+ 消息类型，AgentState enum） | `api/proto/agent.proto` + 生成代码 | 8.1 |
| A4.2 | 实现 AgentService gRPC 服务（对接 Agent Core，管理会话生命周期） | `internal/agent/service.go` | 8.1 |
| A4.3 | 配置 gRPC-Gateway REST API（POST /sessions, POST /sessions/{id}/messages, GET /sessions/{id}, DELETE /sessions/{id}） | `api/proto/agent.proto` 注解 + gateway 配置 | 8.2 |
| A4.4 | 实现多轮对话管理（Session 内消息历史、上下文追加、用户补充需求后重规划） | `internal/agent/session.go` 扩展 | 8.3 |
| A4.5 | 实现 SSE 进度推送（Agent 状态变化、Forge 任务进度、质量检查结果实时推送给客户端） | `internal/agent/sse.go` | 8.4 |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| EA4.1 | Proto 生成 | `buf generate` 成功，生成的 Go 代码无编译错误 |
| EA4.2 | CreateSession | gRPC 调用 CreateSession 返回 session_id |
| EA4.3 | SendMessage | 发送自然语言消息，返回 streaming response 包含 Agent 状态更新 |
| EA4.4 | REST API | `curl -X POST /api/v1/sessions` + `curl -X POST /api/v1/sessions/{id}/messages` 正常工作 |
| EA4.5 | 多轮对话 | 第一轮"做个换脸视频"→ 第二轮"再加上字幕"→ Agent 重新规划包含字幕步骤 |
| EA4.6 | SSE 进度 | 客户端收到状态变化事件流（parsing → planning → executing → completed） |

---

## 附录：Phase 对应关系

| 实施方案 Phase | 技术方案实施计划 Phase | 聚焦点 |
|----------------|----------------------|--------|
| 1A + 1B | Phase 1（地基） | 骨架 / 存储+端到端 |
| 2A + 2B | Phase 2（分布式） | 协调层 / 调度+容错 |
| 3A + 3B | Phase 3（多语言 SDK） | Python / C++ + 混合测试 |
| 4A + 4B + 4C | Phase 4（高级特性） | 事件+Saga / Cron / CDC+Wasm |
| 5A + 5B | Phase 5（K8s 云原生） | 镜像+Helm / Gateway+Kueue+CI |
| 6A + 6B + 6C | Phase 6（可观测+Dashboard） | OTel / eBPF+NATS+Go新特性 / Web UI |
| 7 | 新增 | 文档+示例+打磨 |
| **A1 + A2 + A3 + A4** | **Agent（agent-tech-spec.md）** | **Tool封装 / Agent Core / 质量检查 / API** |
| **AE-1 ~ AE-4** | **Agent 增强（agent-enhancement-slim.md）** | **7 模块：Structured Output / Harness / MCP / RAG / Memory / Guardrails / Checkpointing** |

---

> **启动指令**：当你准备好时，说 **"启动 Phase 1A"** 即可开始。
> 
> Agent 相关：Phase 2B 完成后，说 **"启动 Phase A1"** 开始 Agent 开发。
> 
> Agent 增强：Phase A2 完成后，说 **"启动 Phase AE-1"** 开始增强模块开发。

---

## Phase A3/A4 状态说明

> **Phase A3（质量检查+Reflection）和 A4（API+对话管理）的功能被以下增强模块覆盖，不再单独实施：**
> - A3 的质量检查 → M2 Harness 的 Reflect 步骤
> - A3 的 Reflection 循环 → M2 Harness 的 ReAct 循环自带
> - A4 的 API → 后续独立迭代（不在增强阶段范围内）
> - A4 的 SSE 进度 → 后续独立迭代
> - A4 的对话管理 → M5 Memory 部分覆盖

---

## Phase AE-1：Agent 增强 — 基础设施（M8 + M2） ✅

> 配套方案：`桌面\agent\Agent增强方案-精简版(7模块).md`
> 这是 Agent 增强的核心 Phase，产出一个能跑的 ReAct Agent 闭环。
> **完成时间：2026-04-20 14:30** | 待 CastWell 手动 commit

### 前置条件
- Phase A1 ✅ + A2 ✅（Tool Worker + Agent Core 已完成）
- LLM API 可调用（bmc-llm-relay）

### 任务清单

| 编号 | 任务 | 产出文件 | 说明 |
|------|------|---------|------|
| **M8: Structured Output** | | | |
| AE-1.1 | 实现 JSON Schema 生成器（从 Go struct 反射生成 schema） | `internal/agent/structured/schema.go` | 给 LLM 的 response_format 用 |
| AE-1.2 | 实现 AgentResponse 结构体（Thought + Action + Answer 三选一） | `internal/agent/structured/types.go` | Agent 循环的输出格式 |
| AE-1.3 | 实现输出校验 + 自动重试（解析失败最多重试 2 次） | `internal/agent/structured/validator.go` | |
| **M2: Agent Harness** | | | |
| AE-1.4 | 实现 LLM Client 封装（调 bmc-llm-relay API，支持 Structured Output） | `internal/agent/harness/llm.go` | 对接真实 LLM |
| AE-1.5 | **实现 ReAct 核心循环（⚠️ CastWell 自己手写）** | `internal/agent/harness/loop.go` | Think→Act→Observe 循环 |
| AE-1.6 | 实现 ToolRouter（从 ToolRegistry 查找并调用工具） | `internal/agent/harness/tool_router.go` | 桥接 Harness 和已有 ToolRegistry |
| AE-1.7 | 实现 Context Window 管理（消息超限时摘要压缩旧历史） | `internal/agent/harness/context.go` | 防止 token 爆掉 |
| AE-1.8 | 端到端测试：自然语言 → ReAct 循环 → 调用工具 → 返回结果 | `test/agent_harness_test.go` | |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| EAE-1.1 | Schema 生成 | AgentResponse struct → 合法 JSON Schema，能被 LLM API 接受 |
| EAE-1.2 | Structured Output | LLM 返回的 JSON 能成功解析为 AgentResponse |
| EAE-1.3 | ReAct 循环 | 输入 "查一下这个视频的分辨率" → Agent 调用 video.probe → 返回正确结果 |
| EAE-1.4 | 多步推理 | 输入 "帮我做换脸视频" → Agent 依次调用 download → probe → face_swap → 返回 |
| EAE-1.5 | 异常处理 | 工具调用失败 → Agent 接收错误信息 → 尝试替代方案或报错 |
| EAE-1.6 | maxSteps | 超过最大步数 → 优雅退出不死循环 |
| EAE-1.7 | 全量测试 | `go build ./...` ✅ `go test ./...` ✅ |

---

## TODO: Agent Handler Mock → Real 对接

> **记录时间：2026-04-20** | 优先级：引擎层全部完成后
>
> 18 个 Agent handler（`internal/agent/workers/`）目前全部是 mock 模式，real 模式的实现全是 `ErrNotConfigured` 空壳。
> 引擎层做完、下游服务接口明确后，需要：
> 1. 梳理每个 handler 对接的真实服务（API 地址、协议、认证方式）
> 2. 实现 real 模式逻辑
> 3. `conf/agent.toml` 的 `handler.mode` 切换到 `real` 后能跑通端到端
>
> 涉及文件：`ai_handler.go` / `ffmpeg_handler.go` / `media_handler.go` / `quality_handler.go` / `video_handler.go`
> 现有切换机制已就绪：`HandlerConfig.Mode` + `conf/agent.toml`

---

## Phase AE-1G：架构治理前置（2026-05-13 新增）

> 来源：OpenAI Harness Engineering、Parse Don't Validate、ARCHITECTURE.md、AI Forces Good Code、Ralph Loop
> 目的：在进入 AE-2 大量新代码之前，建立架构纪律和 Agent 可读性基础设施
> 预估时间：0.5 天

### 前置条件
- Phase AE-1 Review 通过（已 ✅）
- Pre-AE2 修复已完成（已 ✅ 45f74b3）

### 任务清单

| 编号 | 任务 | 产出文件 | 说明 |
|------|------|---------|------|
| AE-1G.1 | 编写 ARCHITECTURE.md | `ARCHITECTURE.md`（项目根目录） | 鸟瞰 → codemap → 不变量 → 边界，≤200 行 |
| AE-1G.2 | 精简 AGENTS.md 为目录式 | `AGENTS.md`（项目根目录） | ≤100 行，只含项目概述 + 文档指针 + 硬性约束 |
| AE-1G.3 | 编写结构 lint 脚本 | `scripts/lint_structure.go` | 校验依赖方向 + 文件大小 + 命名约定 |
| AE-1G.4 | 编写编码约定文档 | `docs/coding-conventions.md` | Parse Don't Validate 原则 + 类型命名 + 错误处理规范 |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| EAE-1G.1 | ARCHITECTURE.md 完整性 | 包含鸟瞰、codemap（覆盖所有 internal/ 一级目录）、≥3 条不变量、边界说明 |
| EAE-1G.2 | AGENTS.md 精简 | ≤100 行，包含项目描述 + ≥3 个文档指针 + ≥5 条编码约束 |
| EAE-1G.3 | 结构 lint 有效 | `go run scripts/lint_structure.go` 在当前代码上 PASS；故意引入违反规则的 import 后 FAIL |
| EAE-1G.4 | 编码约定 | 文档包含 Parse Don't Validate 示例 + 边界解析规范 + 文件/命名约定 |
| EAE-1G.5 | 不破坏已有代码 | `go build ./...` ✅ `go test ./...` ✅ |

---

## Phase AE-2：Agent 增强 — MCP 工具协议（M1）

> MCP 是 2025-2026 最热的 Agent 技术。面试亮点最高的模块。
> ~~⏸️ 决策（2026-04-20）：暂停~~ → **2026-05-13 恢复：引擎层 4A~6C + AE-1.5 架构治理已完成，开始 AE-2。**
> **设计约束**：遵循 D3（Parse Don't Validate）+ D5（自验证循环）原则，详见 tech-plan-phase-ae2-4.md §一-A

### 前置条件
- Phase AE-1 Review 通过（Agent Harness 能跑）✅
- Phase AE-1G 架构治理前置完成 ⬜

### 任务清单

| 编号 | 任务 | 产出文件 | 说明 |
|------|------|---------|------|
| AE-2.1 | 实现 JSON-RPC 消息编解码（Parse Don't Validate：边界 Parse 为强类型） | `internal/agent/mcp/jsonrpc.go` | MCP 底层协议，所有消息入口经 ParseMCPRequest/ParseMCPResponse |
| AE-2.2 | 实现 StdioTransport（启动外部进程，通过 stdin/stdout 通信） | `internal/agent/mcp/transport_stdio.go` | 最常用的 MCP 传输方式 |
| AE-2.3 | 实现 MCPClient（Initialize 握手 + ListTools + CallTool） | `internal/agent/mcp/client.go` | 核心三个方法 |
| AE-2.4 | 实现 MCPManager（管理多个 MCP Server 的生命周期） | `internal/agent/mcp/manager.go` | 启动/停止/重启 |
| AE-2.5 | 实现 MCPBridge（MCP 发现的工具自动注册到 ToolRegistry） | `internal/agent/mcp/bridge.go` | 让 Harness 无感使用 MCP 工具 |
| AE-2.6 | 在 core/interfaces.go 新增 Verifier 接口 + harness/loop.go 加 Verify 步骤 | `core/interfaces.go` + `harness/loop.go` | D5 自验证循环，Think→Act→Observe→**Verify** |
| AE-2.7 | 编写测试：MCP 协议 + Verify 循环 | `test/agent_mcp_test.go` + `harness/loop_test.go` 扩展 | |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| EAE-2.1 | stdio 通信 | MCPClient 通过 stdin/stdout 与 mock server 成功交换 JSON-RPC 消息 |
| EAE-2.2 | 工具发现 | ListTools 返回 mock server 注册的工具列表 |
| EAE-2.3 | 工具调用 | CallTool 发送请求，收到正确响应 |
| EAE-2.4 | Bridge | MCP 工具出现在 ToolRegistry 中，Agent Harness 可以无感调用 |
| EAE-2.5 | 生命周期 | MCPManager 能启动/停止 MCP Server 进程 |
| EAE-2.6 | Parse Don't Validate | MCP 层所有公共函数签名不接受 `json.RawMessage`/`interface{}`，只接受强类型 struct |
| EAE-2.7 | 自验证循环 | 注入一个总是返回 `ok=false` 的 MockVerifier，Agent 会重试而非直接结束 |

---

## Phase AE-3：Agent 增强 — 知识与记忆（M3 + M5）

### 前置条件
- Phase AE-2 Review 通过

### 任务清单

| 编号 | 任务 | 产出文件 | 说明 |
|------|------|---------|------|
| **M3: RAG** | | | |
| AE-3.1 | 创建 pgvector 扩展 + 向量存储表 | `deploy/migrations/003_rag.sql` | |
| AE-3.2 | 实现 Embedder 接口 + LLM embedding 调用 | `internal/agent/rag/embedder.go` | |
| AE-3.3 | 实现向量检索 + BM25 全文检索 + RRF 融合 | `internal/agent/rag/hybrid.go` | 混合检索核心 |
| AE-3.4 | 注册为 Agent 工具（Agent 主动决定是否搜索 — Agentic RAG） | `internal/agent/rag/tool.go` | |
| **M5: Memory** | | | |
| AE-3.5 | 实现短期记忆（Redis，session 级别，24h TTL） | `internal/agent/memory/shortterm.go` | |
| AE-3.6 | 实现长期记忆（复用 pgvector，存经验/教训） | `internal/agent/memory/longterm.go` | |
| AE-3.7 | 在 Agent 循环结束后自动提取经验存入长期记忆 | `internal/agent/harness/loop.go` 扩展 | |
| AE-3.8 | 编写测试 | `test/agent_rag_test.go` | |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| EAE-3.1 | 向量检索 | 文档索引后，语义相关的查询能检索到 |
| EAE-3.2 | 混合检索 | 向量 + BM25 + RRF 融合结果比单路更好 |
| EAE-3.3 | Agentic RAG | Agent 遇到不确定的工具参数时主动触发搜索 |
| EAE-3.4 | 短期记忆 | session 内的上下文能正确缓存和读取 |
| EAE-3.5 | 长期记忆 | 上次的经验教训在新 session 中能被检索到 |

---

## Phase AE-4：Agent 增强 — 安全与容错（M6 + M12）

### 前置条件
- Phase AE-3 Review 通过

### 任务清单

| 编号 | 任务 | 产出文件 | 说明 |
|------|------|---------|------|
| **M6: Guardrails** | | | |
| AE-4.1 | 实现 Prompt Injection 检测（规则匹配 + 可选小模型分类） | `internal/agent/guardrails/injection.go` | |
| AE-4.2 | 实现输出内容过滤（敏感信息检测：API key、密码等） | `internal/agent/guardrails/content.go` | |
| AE-4.3 | 实现 Token 预算熔断（per-session 预算限制） | `internal/agent/guardrails/budget.go` | |
| AE-4.4 | 集成到 Agent Harness（输入过 input guard → LLM → 输出过 output guard） | `internal/agent/harness/loop.go` 扩展 | |
| **M12: Checkpointing** | | | |
| AE-4.5 | 创建 checkpoint 存储表 | `deploy/migrations/004_checkpoint.sql` | |
| AE-4.6 | 实现 CheckpointStore（Save / Load / Latest） | `internal/agent/checkpoint/store.go` | |
| AE-4.7 | 集成到 Agent Harness（每步自动保存，崩溃后从最近 checkpoint 恢复） | `internal/agent/harness/loop.go` 扩展 | |
| AE-4.8 | 编写测试 | `test/agent_guardrails_test.go` | |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| EAE-4.1 | Injection 检测 | 输入 "ignore previous instructions" 被拦截 |
| EAE-4.2 | 内容过滤 | 输出中包含的 API key 被替换为 `***` |
| EAE-4.3 | 预算熔断 | 超过 token 预算后 Agent 优雅停止 |
| EAE-4.4 | Checkpoint 保存 | Agent 每步后能在 PostgreSQL 中找到对应 checkpoint |
| EAE-4.5 | Checkpoint 恢复 | 模拟崩溃后从 checkpoint 恢复，继续执行不重复已完成步骤 |
| EAE-4.6 | 全量测试 | `go build ./...` ✅ `go test ./...` ✅ |

---

> **Layer 1 Agent 引擎完成。以下进入 Layer 2 自动化框架。**


## Phase V2-1: Workflow YAML Schema + Registry

> 前置: AE-4 完成（Layer 1 Agent 引擎完整）
> 层级: Layer 2 — 自动化框架

### 任务清单

| 编号 | 任务 | 产出文件 | 说明 |
|------|------|---------|------|
| V2-1.1 | 定义 Workflow YAML Schema（Go struct） | `internal/registry/schema.go` | apiVersion/kind/metadata/triggers/config/inputs/stages |
| V2-1.2 | 实现 YAML 解析器（解析 + 校验） | `internal/registry/parser.go` | yaml.v3 |
| V2-1.3 | 实现模板渲染引擎（`{{context.xxx}}`） | `internal/registry/template.go` | text/template 封装 |
| V2-1.4 | 实现 YAML → DAG 编译器 | `internal/registry/compiler.go` | stages → DAG nodes + edges |
| V2-1.5 | 实现 Registry（加载/缓存/Get） | `internal/registry/registry.go` | |
| V2-1.6 | 实现 FileWatcher（fsnotify 热加载） | `internal/registry/watcher.go` | |
| V2-1.7 | 编写测试 + 示例 YAML | `internal/registry/registry_test.go` + `workflows/bug_fix.yaml` | |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| EV2-1.1 | YAML 解析 | 正确解析 bug_fix.yaml |
| EV2-1.2 | DAG 编译 | 编译后拓扑顺序正确 |
| EV2-1.3 | 热加载 | 修改 YAML 后 Registry 自动更新 |
| EV2-1.4 | 错误处理 | 非法 YAML 报明确错误 |
| EV2-1.5 | 全量测试 | `go build ./...` + `go test ./...` PASS |

---

## Phase V2-2: Scheduler + Poll Trigger

> 层级: Layer 2

### 任务清单

| 编号 | 任务 | 产出文件 | 说明 |
|------|------|---------|------|
| V2-2.1 | 定义 Trigger 接口 + PollTrigger | `internal/scheduler/trigger.go` | |
| V2-2.2 | 实现 Scheduler | `internal/scheduler/scheduler.go` | Start/Stop/Register |
| V2-2.3 | 实现去重机制（checkpoint 表） | `internal/scheduler/dedup.go` | |
| V2-2.4 | 实现飞书 MCP 轮询 | `internal/scheduler/feishu_poll.go` | **复用 AE-2 MCPClient + HTTPTransport** |
| V2-2.5 | 数据库迁移 | `deploy/migrations/005_v2_scheduler.sql` | |
| V2-2.6 | 编写测试 | `internal/scheduler/scheduler_test.go` | |

### 评估标准

| 编号 | 评估项 | 通过条件 |
|------|--------|----------|
| EV2-2.1 | 轮询执行 | 定时调 MCP（mock） |
| EV2-2.2 | 去重 | 同一事件不重复触发 |
| EV2-2.3 | 触发 | 新事件 → 创建 workflow instance |
| EV2-2.4 | 全量测试 | PASS |

---

## Phase V2-3: HITL Manager + Workflow API

> 层级: Layer 2

### 任务清单

| 编号 | 任务 | 产出文件 | 说明 |
|------|------|---------|------|
| V2-3.1 | HITL Manager 核心 | `internal/hitl/manager.go` | Create/Respond/CheckTimeouts |
| V2-3.2 | HITL 存储 | `deploy/migrations/006_v2_hitl.sql` | |
| V2-3.3 | HITL Callback（通知 OpenClaw） | `internal/hitl/callback.go` | |
| V2-3.4 | Proto 定义 (WorkflowService) | `api/proto/workflow.proto` | 6 个 RPC |
| V2-3.5 | gRPC Server 实现 | `internal/coordinator/workflow_api.go` | |
| V2-3.6 | gRPC-Gateway REST | `api/proto/workflow.proto` 标注 | |
| V2-3.7 | 编写测试 | `internal/hitl/manager_test.go` | |

---

## Phase V2-4: MCP Worker + Git Worker

> 层级: Layer 2
> **复用: AE-2 MCPClient + HTTPTransport**

### 任务清单

| 编号 | 任务 | 产出文件 | 说明 |
|------|------|---------|------|
| V2-4.1 | MCP Worker 框架 | `internal/workers/mcp/worker.go` | 构造 MCPClient(HTTPTransport) |
| V2-4.2 | MCP Worker Actions | `internal/workers/mcp/actions.go` | 7 个 action |
| V2-4.3 | Git Worker 框架 + go-git | `internal/workers/git/worker.go` | |
| V2-4.4 | Git Worker 只读 Actions | `internal/workers/git/actions_read.go` | pull/search/log/blame/diff |
| V2-4.5 | Git Worker 写入 Actions | `internal/workers/git/actions_write.go` | branch/commit/push/mr |
| V2-4.6 | 项目配置加载 | `internal/workers/git/project_config.go` | projects/*.yaml |
| V2-4.7 | 编写测试 | 测试文件 | |

---

## Phase V2-5: AI Worker + Claude Code Worker

> 层级: Layer 2
> **复用: AE-3 RAG + Memory, AE-4 Reflexion + Guardrails, LLM重试**

### 任务清单

| 编号 | 任务 | 产出文件 | 说明 |
|------|------|---------|------|
| V2-5.1 | AI Worker 框架 | `internal/workers/ai/worker.go` | 内部启动 Agent Session |
| V2-5.2 | AI Worker Actions | `internal/workers/ai/actions.go` | analyze/synthesize/classify |
| V2-5.3 | 模型配置 | `internal/workers/ai/config.go` | 不同 action 用不同模型 |
| V2-5.4 | Claude Code Worker | `internal/workers/claudecode/worker.go` | --print 模式 |
| V2-5.5 | Claude Code Actions | `internal/workers/claudecode/actions.go` | implement/fix |
| V2-5.6 | 编写测试 | 测试文件 | |

---

## Phase V2-6: Review Worker + Shell Worker

> 层级: Layer 2
> **复用: AE-2 code.execute, AE-3 RAG (代码规范检索)**

### 任务清单

| 编号 | 任务 | 产出文件 | 说明 |
|------|------|---------|------|
| V2-6.1 | Review Worker 框架 | `internal/workers/review/worker.go` | |
| V2-6.2 | Review Actions | `internal/workers/review/actions.go` | review_plan/review_code |
| V2-6.3 | Shell Worker + 安全 | `internal/workers/shell/worker.go` | 白名单+超时 |
| V2-6.4 | Shell Actions | `internal/workers/shell/actions.go` | run_test/build/lint |
| V2-6.5 | 命令白名单配置 | `internal/workers/shell/security.go` | |
| V2-6.6 | 编写测试 | 测试文件 | |

---

## Phase V2-7: Database Worker + HITL Worker

> 层级: Layer 2
> **复用: AE-2 data.query, §22 HITL Manager**

### 任务清单

| 编号 | 任务 | 产出文件 | 说明 |
|------|------|---------|------|
| V2-7.1 | Database Worker (PG) | `internal/workers/database/worker.go` | SELECT only |
| V2-7.2 | Database Worker (Redis) | `internal/workers/database/redis.go` | GET/KEYS |
| V2-7.3 | HITL Worker | `internal/workers/hitl/worker.go` | 通知/审批/等待 |
| V2-7.4 | 连接配置从 project config 读取 | `internal/workers/database/config.go` | |
| V2-7.5 | 编写测试 | 测试文件 | |

---

## Phase V2-8: DAG 引擎增强

> 层级: Layer 0 扩展

### 任务清单

| 编号 | 任务 | 产出文件 | 说明 |
|------|------|---------|------|
| V2-8.1 | CEL 表达式引擎集成 | `internal/coordinator/cel.go` | google/cel-go |
| V2-8.2 | Task 条件执行 | `internal/coordinator/dag.go` 扩展 | condition 字段 CEL 求值 |
| V2-8.3 | 结果路由 (on_result) | `internal/coordinator/routing.go` | goto/abort/continue |
| V2-8.4 | 循环支持 (max_iterations) | `internal/coordinator/loop.go` | |
| V2-8.5 | 编写测试 | 测试文件 | |

---

## Phase V2-9: OpenClaw 集成

> 层级: Layer 2 → Layer 3 桥接

### 任务清单

| 编号 | 任务 | 产出文件 | 说明 |
|------|------|---------|------|
| V2-9.1 | OpenClaw Cron 配置 | OpenClaw 配置文件 | 每 2 分钟触发 |
| V2-9.2 | HITL → OpenClaw 回调 | `internal/hitl/openclaw_callback.go` | |
| V2-9.3 | OpenClaw → Forge HITL 响应 | 集成代码 | gRPC ResumeHITL |
| V2-9.4 | 飞书消息格式化 | `internal/hitl/message_format.go` | |
| V2-9.5 | 集成测试 | 测试文件 | |

---

## Phase V2-10: 端到端联调

> 层级: Layer 3

### 任务清单

| 编号 | 任务 | 产出文件 | 说明 |
|------|------|---------|------|
| V2-10.1 | bug_fix.yaml 正式版 | `workflows/bug_fix.yaml` | |
| V2-10.2 | avp_eds 项目配置 | `projects/avp_eds.yaml` | |
| V2-10.3 | 端到端测试 | 测试记录 | 手动创建 Bug → 自动修复 |
| V2-10.4 | 异常场景测试 | 测试记录 | 超时/失败/HITL 超时 |
| V2-10.5 | Dashboard 扩展 | `web/src/pages/workflows/` | 工作流列表 + DAG 进度 |
| V2-10.6 | 文档更新 | `docs/forge-v2-guide.md` | 使用指南 |

---

## 执行总览

| 阶段 | Phase 数 | 任务数 | 层级 | 状态 |
|------|---------|--------|------|------|
| 基础设施 (1A~6C) | 19 | ~100+ | Layer 0 | ✅ 已完成 |
| Agent 骨架 (AE-1) | 1 | 8 | Layer 1 | ✅ 已完成 |
| Agent 引擎 (AE-2~4) | 3 | ~30 | Layer 1 | ← **下一步** |
| 自动化框架 (V2-1~9) | 9 | ~56 | Layer 2 | 待做 |
| 应用联调 (V2-10) | 1 | 6 | Layer 3 | 待做 |
| **合计** | **33** | **~200** | | |