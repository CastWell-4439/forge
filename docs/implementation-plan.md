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

| Phase | 名称 | 任务数 | 核心目标 |
|-------|------|--------|----------|
| 1A | 项目骨架 + Proto + DAG 引擎 | 7 | 目录结构、gRPC 定义、DAG 解析和验证 |
| 1B | 存储层 + 基础 Coordinator + Worker | 8 | PG/BoltDB 存储、单节点端到端跑通 |
| 2A | 服务发现 + Leader 选举 + Worker 管理 | 6 | etcd 嵌入、心跳、故障检测 |
| 2B | 调度算法 + 重试 + 超时 + 事件通知 | 7 | 4 种调度算法、指数退避、PG NOTIFY |
| 3A | Python Worker SDK | 6 | Python SDK 开发 + 测试 + 打包 |
| 3B | C++ Worker SDK + 多语言混合测试 | 6 | C++ SDK 开发 + 三语言混合路由验证 |
| 4A | 事件溯源 + Saga 补偿 | 6 | 事件记录/回放、补偿事务 |
| 4B | Cron 调度 + 时间轮 | 5 | 定时触发、分布式去重、时间轮算法 |
| 4C | CDC 引擎 + Wasm 插件 | 7 | PG CDC 监听、wazero 沙箱执行 |
| 5A | Docker 镜像 + Helm Chart | 7 | 容器化 + K8s 一键部署 |
| 5B | Gateway API + Kueue + CI/CD | 6 | K8s 高级特性 + 自动化流水线 |
| 6A | Metrics + Tracing + Profiling | 6 | OTel 四信号中的三个 |
| 6B | eBPF + NATS + Go 1.23 新特性 | 7 | 内核探针、可选消息后端、语言新特性 |
| 6C | Admin Dashboard | 6 | React Web UI + DAG 可视化 |
| 7 | 文档 + 示例 + 最终打磨 | 7 | README、SDK 文档、示例、代码清理 |

**总计：15 个 Phase，97 个任务**

---

## Phase 1A：项目骨架 + Proto + DAG 引擎

### 前置条件
- Go 1.22+ 已安装
- `buf` CLI 已安装

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

---

> **启动指令**：当你准备好时，说 **"启动 Phase 1A"** 即可开始。
