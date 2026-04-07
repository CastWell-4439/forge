# 分布式任务调度引擎 - 技术方案文档

> 项目代号：**Forge**（锻造厂 —— 锻造每一个任务）
> 作者：CastWell
> 日期：2026-04-01
> 状态：技术选型 & 架构设计

---

## 目录

1. [项目概述](#1-项目概述)
2. [核心需求与设计目标](#2-核心需求与设计目标)
3. [技术选型](#3-技术选型)
   - 3.1 编程语言：多语言协同
   - 3.2 通信框架
   - 3.3 服务发现与协调
   - 3.4 持久化存储
   - 3.5 消息队列 / 事件总线
   - 3.6 NATS JetStream（可选升级）
   - 3.7 缓存
   - 3.8 可观测性
   - 3.9 测试
   - 3.10 构建与部署
   - 3.11 技术选型全景图
4. [系统架构](#4-系统架构)
5. [核心模块设计](#5-核心模块设计)
   - 5.1 DAG 引擎
   - 5.2 调度器
   - 5.3 Worker 管理器
   - 5.4 事件溯源模块
   - 5.5 Saga 补偿事务
   - 5.6 Cron 调度器
   - 5.7 Wasm 插件系统（WASI P2 Component Model）
   - 5.8 eBPF 可观测性 + OTel Continuous Profiling
   - 5.9 CDC 数据源接入
   - 5.10 Admin Dashboard
6. [数据模型](#6-数据模型)
7. [关键算法与机制](#7-关键算法与机制)
   - 7.1 DAG 拓扑排序与环检测
   - 7.2 指数退避重试
   - 7.3 任务认领（PG FOR UPDATE SKIP LOCKED）
   - 7.4 分布式定时器（时间轮）
   - 7.5 Go 1.23 新特性应用
8. [可观测性设计](#8-可观测性设计)
9. [部署方案](#9-部署方案)
   - 9.1 开发环境（docker-compose）
   - 9.2 单二进制模式
   - 9.3 Kubernetes 云原生部署
     - 9.3.1 K8s 整体架构
     - 9.3.2 Coordinator — StatefulSet
     - 9.3.3 Worker — Deployment + HPA
     - 9.3.4 Forge Operator（CRD）
     - 9.3.5 Helm Chart
     - 9.3.6 Gateway API（取代 Ingress）
     - 9.3.7 Kueue — K8s 原生批任务队列
     - 9.3.8 CI/CD — GitHub Actions + ArgoCD
10. [实施计划](#10-实施计划)
11. [风险与应对](#11-风险与应对)
12. [附录 A：项目目录结构](#附录-a项目目录结构)
13. [附录 B：参考项目](#附录-b参考项目)

---

## 1. 项目概述

### 1.1 是什么

一个用 Go 实现的**轻量级分布式任务调度引擎**，支持 DAG（有向无环图）工作流编排、分布式任务执行、故障恢复和可观测性。

对标产品：Temporal / Cadence / Airflow，但更轻量、更 Go-native。

### 1.2 为什么做这个

| 动机 | 说明 |
|------|------|
| 技术深度 | 涉及分布式共识、状态机、调度算法、故障恢复等硬核话题 |
| 业务呼应 | 任务调度是后端基础设施的核心问题，几乎所有中大型系统都需要（数据 ETL、AI 推理、定时任务、审批流） |
| 差异化 | 市面上 Go 实现的工作流引擎极少，不会被认为是"又一个 CRUD 项目" |
| 堆料空间 | 从简单到复杂可以一直迭代，每个模块都有深挖空间 |

### 1.3 核心卖点

- **DAG 工作流编排**：YAML 定义 + Go SDK 定义，双模式
- **多语言 Worker**：Go / Python / C++ 跨语言协同，gRPC 统一通信
- **分布式执行**：多 Worker 节点，任务自动分发与负载均衡
- **故障自愈**：任务重试、超时检测、Saga 补偿事务
- **事件溯源**：所有状态变更可追溯、可回放
- **CDC 事件驱动**：监听数据库变更自动触发工作流
- **Wasm 沙箱插件**：轻量任务直接在沙箱中执行，冷启动 < 1ms
- **eBPF 内核观测**：零侵入的网络延迟追踪和系统调用分析
- **K8s 云原生**：Helm Chart + 自定义 Operator + HPA 弹性伸缩
- **完整可观测性**：Metrics + Tracing + Logging + eBPF 四件套
- **Admin Dashboard**：Web UI 管理工作流、Worker、CDC、插件
- **嵌入式可选**：既能独立部署，也能作为 SDK 嵌入现有项目

---

## 2. 核心需求与设计目标

### 2.1 功能需求

| 编号 | 需求 | 优先级 |
|------|------|--------|
| F1 | DAG 工作流定义与解析 | P0 |
| F2 | 任务调度与分发 | P0 |
| F3 | 分布式 Worker 执行 | P0 |
| F4 | 任务重试与超时控制 | P0 |
| F5 | 工作流状态持久化 | P0 |
| F6 | 事件溯源与审计日志 | P1 |
| F7 | Saga 补偿事务 | P1 |
| F8 | Cron 定时触发 | P1 |
| F9 | Web Dashboard | P2 |
| F10 | Go SDK（代码定义工作流） | P2 |

### 2.2 非功能需求

| 指标 | 目标 |
|------|------|
| 吞吐量 | 单 Coordinator 节点 > 10,000 任务/秒 调度 |
| 延迟 | 任务从就绪到开始执行 < 100ms（P99） |
| 可用性 | Coordinator 多副本，Worker 无状态，单点故障不影响整体 |
| 数据一致性 | 任务状态变更 Exactly-once 语义 |
| 扩展性 | Worker 水平扩展，Coordinator 通过分片扩展 |

---

## 3. 技术选型

### 3.1 编程语言：多语言协同（Go 主导 + Python/Rust/Java Worker SDK）

**Go 是控制面语言，但 Worker 不只用 Go。** 通过 gRPC 跨语言协议，Worker 可以用任何语言实现。

| 角色 | 语言 | 理由 |
|------|------|------|
| **Coordinator / 核心引擎** | Go | goroutine + channel 天生适合并发调度；编译为单二进制；分布式系统库生态最强 |
| **Worker SDK — Go** | Go | 一等公民，与 Coordinator 同语言，零序列化开销 |
| **Worker SDK — Python** | Python | AI/ML 生态无可替代（PyTorch/TensorFlow/LangChain）；数据处理库丰富（pandas/numpy）；脚本类任务开发效率最高；PyPI 发包方便 |
| **Worker SDK — C++** | C++ | 极致性能计算任务（视频渲染、编解码、数据压缩）；直接操作硬件/SIMD；体现系统级功底 |

**多语言 Worker 架构：**
```
                    Coordinator (Go)
                         │ gRPC
          ┌──────────────┼──────────────┐
          │              │              │
    Go Worker     Python Worker   C++ Worker
    (原生SDK)      (pip install)   (CMake/vcpkg)
```

**关键设计：Worker SDK 只需实现一个 gRPC 接口即可注册为 Worker**
```protobuf
// worker.proto — 所有语言的 Worker 都实现这个接口
service WorkerService {
    rpc Register(RegisterRequest) returns (RegisterResponse);
    rpc Heartbeat(stream HeartbeatPing) returns (stream HeartbeatPong);
    rpc ExecuteTask(TaskRequest) returns (TaskResponse);
}
```

**Python Worker 示例（展示 SDK 易用性）：**
```python
from forge_sdk import Worker, task_handler

worker = Worker(coordinator="localhost:8080")

@task_handler("ai.generate")
def handle_ai_generate(ctx, params):
    """AI 生成任务 — 用 Python 生态更自然"""
    from openai import OpenAI
    client = OpenAI()
    result = client.chat.completions.create(
        model=params["model"],
        messages=[{"role": "user", "content": params["prompt"]}]
    )
    return {"output": result.choices[0].message.content}

@task_handler("video.analyze")
def handle_video_analyze(ctx, params):
    """视频分析 — 依赖 opencv/ffmpeg 等 Python 库"""
    import cv2
    # ...
    return {"frames": extracted_frames}

worker.start()  # 自动注册、心跳、任务接收
```

**C++ Worker 示例（展示极致性能场景）：**
```cpp
#include <forge/worker.h>
#include <forge/task_handler.h>
#include <grpcpp/grpcpp.h>

class VideoRenderHandler : public forge::TaskHandler {
public:
    std::string name() const override { return "video.render"; }

    forge::TaskResult execute(const forge::TaskContext& ctx) override {
        auto input_path = ctx.param<std::string>("input_path");

        // SIMD 加速的视频渲染管线，比 Go/Python 快 10-50x
        auto pipeline = RenderPipeline::create(input_path);
        pipeline.enable_avx2();          // 利用 CPU 向量指令集
        pipeline.set_thread_pool(std::thread::hardware_concurrency());
        auto output = pipeline.render();

        return forge::TaskResult::ok({{"output_path", output}});
    }
};

int main() {
    forge::Worker worker("localhost:8080");
    worker.register_handler(std::make_unique<VideoRenderHandler>());
    worker.start();  // 阻塞，自动注册 + 心跳 + 任务接收
    return 0;
}
```

**C++ SDK 构建（CMakeLists.txt）：**
```cmake
cmake_minimum_required(VERSION 3.20)
project(forge-worker-cpp LANGUAGES CXX)
set(CMAKE_CXX_STANDARD 20)

find_package(gRPC CONFIG REQUIRED)
find_package(Protobuf CONFIG REQUIRED)
find_package(forge-sdk CONFIG REQUIRED)  # vcpkg 安装

add_executable(forge-worker main.cpp)
target_link_libraries(forge-worker PRIVATE
    forge::sdk
    gRPC::grpc++
    protobuf::libprotobuf
)
```

**堆料亮点：**
- 多语言 SDK 发布：Go module / PyPI / vcpkg + Conan（C++）
- 统一的 Protobuf 接口定义（`api/proto/`），`buf generate` 一键生成所有语言的 client/server 代码
- Worker 语言透明：Coordinator 不关心 Worker 用什么语言，只看 gRPC 接口
- **面试时一句话杀招：** "我的调度引擎支持多语言 Worker，AI 任务用 Python，高性能渲染/编解码用 C++，核心调度用 Go，通过 gRPC + Protobuf 实现跨语言零摩擦协同"

### 3.2 通信框架

| 选项 | 优点 | 缺点 | 结论 |
|------|------|------|------|
| **gRPC + Protobuf** | 高性能、强类型、双向流、生态成熟 | 调试不如 HTTP 直观 | ✅ **选定** |
| HTTP/REST + JSON | 简单直观、调试方便 | 性能差、无流式支持、缺少代码生成 | ❌ |
| NATS | 消息模式灵活、轻量 | 需要额外引入中间件、不如 gRPC 类型安全 | ❌ |

**选择理由：**
- gRPC 的双向流（Bidirectional Streaming）用于 Worker 心跳和任务推送完美适配
- Protobuf 代码生成保证接口一致性，多语言 SDK 扩展方便
- gRPC 是云原生微服务的事实标准，与 K8s、Istio、Envoy 等生态无缝集成

**备选说明：** 内部模块间通信全走 gRPC；外部 API（Dashboard、CLI）提供 gRPC-Gateway 自动生成 REST 接口，兼顾易用性。

### 3.3 服务发现与协调

| 选项 | 优点 | 缺点 | 结论 |
|------|------|------|------|
| **嵌入式 etcd (embed)** | 强一致、Watch 机制、无外部依赖 | 内存占用较大（~200MB）、运维复杂度 | ✅ **主选** |
| **HashiCorp Raft** | 纯 Go、可嵌入、轻量 | 只提供共识，服务发现/KV 需自己实现 | ✅ **备选** |
| 外部 etcd 集群 | 成熟稳定 | 需要额外部署运维 | 🔄 生产环境备选 |
| Consul | 服务发现+KV+健康检查一体 | 需要额外部署、功能过重 | ❌ |
| ZooKeeper | 久经考验 | Java 生态、运维复杂、Go 客户端质量一般 | ❌ |

**选择理由：**

开发阶段用**嵌入式 etcd**，零外部依赖，启动即用：
- Leader 选举：用 etcd 的 Election API
- 服务注册：Worker 注册到 etcd，Coordinator 通过 Watch 发现
- 分布式锁：用 etcd 的 Lock API
- 配置存储：工作流定义可选存 etcd

**进阶路线：** 如果后期想更硬核，可以切换到 HashiCorp Raft 自己实现共识层，这个过程本身就是一个巨大的技术亮点。通过接口抽象，让两者可插拔：

```go
type Coordinator interface {
    LeaderElect(ctx context.Context) (<-chan bool, error)
    Register(ctx context.Context, node NodeInfo) error
    Watch(ctx context.Context, prefix string) (<-chan Event, error)
    Lock(ctx context.Context, key string) (Unlock func(), error)
}
```

### 3.4 持久化存储

| 选项 | 优点 | 缺点 | 结论 |
|------|------|------|------|
| **PostgreSQL** | ACID、JSONB 灵活查询、成熟稳定、Go 生态支持最好 | 分布式扩展需要中间件 | ✅ **主选** |
| MySQL | 普及度高 | JSONB 支持不如 PG、锁机制粗糙 | ❌ |
| **嵌入式 BoltDB/Pebble** | 零依赖、极简部署 | 不支持并发写、查询能力弱 | ✅ **单机模式备选** |
| MongoDB | 文档模型灵活 | 一致性模型复杂、Go 驱动较重 | ❌ |
| CockroachDB | 分布式 SQL | 太重、杀鸡用牛刀 | ❌ |

**选择理由：**
- PostgreSQL 的 JSONB 用来存工作流定义和任务参数很方便
- `FOR UPDATE SKIP LOCKED` 天然适合任务队列的消费模式
- Advisory Lock 可以做轻量分布式锁
- PostgreSQL 是 Go 生态中支持最好的关系型数据库（pgx 驱动性能优异）

**双模式设计：**
```go
type Storage interface {
    SaveWorkflow(ctx context.Context, wf *Workflow) error
    SaveTask(ctx context.Context, task *Task) error
    ClaimTask(ctx context.Context, workerID string) (*Task, error)
    UpdateTaskStatus(ctx context.Context, taskID string, status Status) error
    GetWorkflowHistory(ctx context.Context, wfID string) ([]Event, error)
    // ...
}
```
- 开发/单机模式：BoltDB 实现（零依赖）
- 生产模式：PostgreSQL 实现

### 3.5 消息队列 / 事件总线

| 选项 | 优点 | 缺点 | 结论 |
|------|------|------|------|
| **内置（PG LISTEN/NOTIFY + 轮询）** | 零依赖、够用 | 吞吐量有上限（~万级/秒） | ✅ **默认** |
| **Redis Streams** | 高吞吐、消费者组、部署简单 | 持久性不如 Kafka | ✅ **高吞吐场景** |
| Kafka | 吞吐量极高、消息持久化 | 太重、需要 ZK/KRaft | ❌ 杀鸡用牛刀 |
| RabbitMQ | 成熟的消息语义 | Erlang 运维、不够轻量 | ❌ |
| **NATS JetStream** | 超轻量、Go 原生、内置 KV/对象存储 | 社区较小 | ✅ **高级模式可选** |

**选择理由：**
- 默认用 PG 的 LISTEN/NOTIFY 做事件通知，加轮询做兜底，对中小规模够用
- Redis Streams 作为可选后端，用于高吞吐场景
- 通过接口抽象，后期可以接入 Kafka/NATS

### 3.6 NATS JetStream — 轻量级全能消息层（可选升级）

**NATS JetStream（2024 新特性爆发）** 是一个被严重低估的基础设施组件。2024 年新增了 KV Store、Object Store、Subject Transform 等能力，已经不只是消息队列了：

```
传统方案（3 个组件）              NATS JetStream（1 个组件全搞定）
┌──────────┐                    ┌─────────────────────────┐
│  Redis   │ ← 缓存/KV          │                         │
├──────────┤                    │  NATS JetStream         │
│  Kafka   │ ← 消息队列    →    │  ├─ Stream（消息队列）   │
├──────────┤                    │  ├─ KV Store（键值缓存） │
│  etcd    │ ← 服务发现          │  └─ Object Store（文件） │
└──────────┘                    └─────────────────────────┘
   3 个进程                          1 个进程，< 20MB 内存
```

**为什么在 Forge 中可选集成 NATS：**

| 用途 | 默认方案 | NATS 替代方案 | 优势 |
|------|----------|---------------|------|
| 任务分发 | PG LISTEN/NOTIFY | JetStream Consumer | 10x 吞吐量，消费者组，持久化 |
| Worker 心跳 | Redis Hash + TTL | KV Store + Watch | 少一个 Redis 依赖 |
| 事件总线 | Redis Streams | JetStream Subject | 天然支持 Exactly-once |
| 配置分发 | etcd Watch | KV Store Watch | 更轻量 |
| Wasm 插件存储 | PG + S3 | Object Store | 内置，不需要 S3 |

**Go 集成示例：**
```go
import "github.com/nats-io/nats.go/jetstream"

func (b *NATSBus) PublishTask(ctx context.Context, task *Task) error {
    js, _ := jetstream.New(b.nc)

    // JetStream：持久化 + At-least-once + Consumer Group
    _, err := js.Publish(ctx,
        fmt.Sprintf("forge.tasks.%s", task.Handler),
        task.Encode(),
        jetstream.WithMsgID(task.ID),        // 去重
        jetstream.WithExpectLastMsgID(""),    // 乐观并发控制
    )
    return err
}

func (b *NATSBus) SubscribeTasks(ctx context.Context, handler string, fn func(*Task)) error {
    js, _ := jetstream.New(b.nc)

    // Durable Consumer：断线重连自动续传
    consumer, _ := js.CreateOrUpdateConsumer(ctx, "FORGE_TASKS", jetstream.ConsumerConfig{
        Durable:       fmt.Sprintf("worker-%s-%s", handler, workerID),
        FilterSubject: fmt.Sprintf("forge.tasks.%s", handler),
        AckPolicy:     jetstream.AckExplicitPolicy,
        MaxDeliver:    3,                    // 最多重投 3 次
        AckWait:       time.Minute,
    })

    // 拉取模式（比推送更可控）
    iter, _ := consumer.Messages()
    for msg := range iter.Chan() {
        task := decodeTask(msg.Data())
        fn(task)
        msg.Ack()
    }
    return nil
}

// KV Store 替代 Redis 做 Worker 心跳
func (b *NATSBus) WorkerHeartbeat(ctx context.Context, workerID string, info WorkerInfo) error {
    kv, _ := jetstream.New(b.nc)
    bucket, _ := kv.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
        Bucket: "forge-workers",
        TTL:    60 * time.Second,    // 自动过期 = Worker 死亡
    })
    _, err := bucket.Put(ctx, workerID, info.Encode())
    return err
}
```

**部署极简（单二进制 < 20MB）：**
```yaml
# docker-compose — NATS 替代 Redis + Kafka
nats:
  image: nats:2.11-alpine
  command: ["--jetstream", "--store_dir=/data"]
  ports:
    - "4222:4222"   # 客户端
    - "8222:8222"   # 监控 Dashboard
  volumes:
    - nats-data:/data
```

**堆料亮点：**
- NATS JetStream 2024 年新增 KV/Object Store，一个进程替代 Redis + Kafka + S3
- 嵌入式模式：NATS 可以直接嵌入 Go 进程（`nats-server` 作为 library），真正的零外部依赖
- **面试时："我的系统支持多种消息后端，默认用 PG 零依赖，高吞吐场景可切换到 NATS JetStream，一个 20MB 的进程替代了 Redis + Kafka + etcd 三件套"**

### 3.6 缓存

| 选项 | 用途 | 结论 |
|------|------|------|
| **Redis** | Worker 心跳、任务锁、速率限制、热点数据缓存 | ✅ **选定** |
| 本地 sync.Map / LRU | 进程内缓存（工作流定义等不常变的数据） | ✅ **辅助** |

### 3.7 可观测性

| 组件 | 选型 | 理由 |
|------|------|------|
| Metrics | **Prometheus + Grafana** | Go 生态标配，client_golang 官方库 |
| Tracing | **OpenTelemetry + Jaeger** | 厂商中立，链路追踪行业标准 |
| Logging | **zerolog** | 高性能结构化日志，零内存分配 |

**备选日志库对比：**

| 选项 | 性能 | 结构化 | 易用性 | 结论 |
|------|------|--------|--------|------|
| **zerolog** | 极快（零分配） | ✅ JSON 原生 | 链式调用 | ✅ **选定** |
| zap | 很快 | ✅ | 略繁琐 | 🔄 备选 |
| slog (标准库) | 够用 | ✅ | 官方支持 | 🔄 备选 |
| logrus | 慢 | ✅ | 易用但过时 | ❌ |

### 3.8 测试

| 类型 | 工具 | 说明 |
|------|------|------|
| 单元测试 | Go 标准 testing + testify | 断言库用 testify，mock 用 mockgen |
| 集成测试 | testcontainers-go | 自动启动 PG/Redis 容器跑测试 |
| 压测 | 自研 benchmark + pprof | Go 自带 benchmark 框架 + pprof 分析 |
| 混沌测试 | toxiproxy | 模拟网络分区、延迟、丢包 |

### 3.9 构建与部署

| 组件 | 选型 | 理由 |
|------|------|------|
| 构建 | **Makefile + GoReleaser** | Makefile 日常开发，GoReleaser 发布多平台二进制 |
| 容器化 | **Docker 多阶段构建** | 最终镜像 < 20MB（FROM scratch） |
| CI/CD | **GitHub Actions** | 免费、生态好、开源项目标配 |
| 编排 | **docker-compose（开发）/ K8s Helm（生产）** | 开发轻量，生产弹性 |

### 3.10 技术选型全景图

```
┌──────────────────────────────────────────────────────────────────┐
│                        Forge 技术栈                              │
├──────────────┬───────────────────────────────────────────────────┤
│ 核心语言     │ Go 1.22+ (Coordinator/Worker/CLI/SDK)             │
│ 多语言 Worker│ Python (AI/ML) / C++ (高性能渲染/编解码)          │
│ eBPF 探针    │ C (BPF 字节码，cilium/ebpf 加载)                  │
│ 通信         │ gRPC + Protobuf + gRPC-Gateway + buf 多语言生成   │
│ 协调         │ 嵌入式 etcd / HashiCorp Raft（可插拔）             │
│ 存储         │ PostgreSQL（生产）/ BoltDB（单机）                 │
│ 缓存         │ Redis + 本地 LRU                                  │
│ 事件总线     │ PG LISTEN/NOTIFY / Redis Streams                  │
│ CDC 数据源   │ PG Logical Replication / MySQL Binlog / Redis KS  │
│ 插件系统     │ WASI P2 Component Model (wazero, 2024 新标准)     │
│ 消息层(可选) │ NATS JetStream (KV/Object Store, 替代Redis+Kafka) │
│ 可观测性     │ Prometheus + Jaeger + zerolog + eBPF + OTel Profiling │
│ 前端         │ React + Ant Design Pro (Admin Dashboard)          │
│ 测试         │ testify + testcontainers + toxiproxy              │
│ 构建         │ Makefile + GoReleaser + CMake (C++) + buf (Proto) │
│ 部署         │ Docker + docker-compose / K8s Helm + Operator     │
│ K8s 流量入口 │ Gateway API + GRPCRoute（取代 Ingress）            │
│ K8s 批任务   │ Kueue（Google 2024，资源配额 + 公平调度）          │
│ CI/CD        │ GitHub Actions + ArgoCD                           │
└──────────────┴───────────────────────────────────────────────────┘

语言覆盖：Go · Python · C++ · C (eBPF) · TypeScript (Dashboard) · YAML (工作流定义)
```

---

## 4. 系统架构

### 4.1 整体架构

```
                        ┌──────────────┐
                        │   CLI / SDK  │
                        └──────┬───────┘
                               │ gRPC
                               ▼
                    ┌─────────────────────┐
                    │    API Gateway       │
                    │  (gRPC-Gateway)      │
                    └─────────┬───────────┘
                              │
                ┌─────────────┼─────────────┐
                │             │             │
                ▼             ▼             ▼
         ┌────────────┐ ┌────────────┐ ┌────────────┐
         │Coordinator │ │Coordinator │ │Coordinator │  ← Leader 选举
         │  (Leader)  │ │ (Follower) │ │ (Follower) │     (etcd/raft)
         └─────┬──────┘ └────────────┘ └────────────┘
               │
    ┌──────────┼──────────┬──────────┐
    │          │          │          │
    ▼          ▼          ▼          ▼
┌────────┐┌────────┐┌────────┐┌────────┐
│Worker 1││Worker 2││Worker 3││Worker N│   ← 无状态，水平扩展
└───┬────┘└───┬────┘└───┬────┘└───┬────┘
    │         │         │         │
    └─────────┴─────────┴─────────┘
                    │
              ┌─────┴─────┐
              │           │
         ┌────┴───┐  ┌───┴────┐
         │  PG    │  │ Redis  │
         └────────┘  └────────┘
```

### 4.2 组件职责

| 组件 | 职责 | 部署方式 |
|------|------|----------|
| **Coordinator** | 工作流管理、DAG 解析、任务调度、Leader 选举 | 3 副本（1 Leader + 2 Follower） |
| **Worker** | 任务执行、心跳上报、结果回传 | 无状态，按需扩缩 |
| **API Gateway** | 对外 REST/gRPC 接口、认证鉴权 | 与 Coordinator 同进程或独立 |
| **PostgreSQL** | 工作流定义、任务状态、事件日志持久化 | 单实例或主从 |
| **Redis** | Worker 心跳、分布式锁、速率限制 | 单实例或 Sentinel |
| **etcd (embed)** | Leader 选举、服务发现、配置同步 | 嵌入 Coordinator 进程 |

### 4.3 核心交互流程

```
用户提交工作流
    │
    ▼
Coordinator 接收 → 解析 DAG → 持久化到 PG
    │
    ▼
调度器扫描就绪任务（入度为 0 的节点）
    │
    ▼
选择 Worker（负载均衡）→ 通过 gRPC Stream 推送任务
    │
    ▼
Worker 执行任务 → 上报结果
    │
    ▼
Coordinator 更新状态 → 检查后继节点是否就绪
    │
    ▼
循环直到所有节点完成 → 标记工作流完成
```

---

## 5. 核心模块设计

### 5.1 DAG 引擎

负责工作流的定义、解析、验证和执行推进。

**YAML 定义示例：**
```yaml
name: video-production
version: 1
timeout: 3600s

tasks:
  fetch-data:
    handler: feishu.pull
    params:
      source_id: 123
    timeout: 300s
    retry:
      max_attempts: 3
      backoff: exponential
      initial_interval: 5s

  ai-generate:
    handler: ai.generate
    depends_on: [fetch-data]
    params:
      model: gpt-4
    timeout: 600s

  render-video:
    handler: video.render
    depends_on: [ai-generate]
    timeout: 1800s

  upload:
    handler: oss.upload
    depends_on: [render-video]
    timeout: 300s
    retry:
      max_attempts: 5
      backoff: exponential

  notify:
    handler: feishu.notify
    depends_on: [upload]
    timeout: 30s
```

**核心数据结构：**
```go
type DAG struct {
    Name    string
    Version int
    Tasks   map[string]*TaskDef
    Edges   map[string][]string // task -> dependencies
}

type TaskDef struct {
    Name      string
    Handler   string
    Params    map[string]interface{}
    DependsOn []string
    Timeout   time.Duration
    Retry     RetryPolicy
    OnFailure FailureAction // FAIL_WORKFLOW | CONTINUE | COMPENSATE
}

type RetryPolicy struct {
    MaxAttempts     int
    InitialInterval time.Duration
    MaxInterval     time.Duration
    BackoffType     BackoffType // FIXED | EXPONENTIAL | EXPONENTIAL_WITH_JITTER
    Multiplier      float64
}
```

**DAG 验证规则：**
1. 环检测（Kahn 算法拓扑排序）
2. 孤立节点检测
3. Handler 注册检查
4. 超时合理性检查（子任务超时 < 工作流超时）

### 5.2 调度器（Scheduler）

负责将就绪任务分配给合适的 Worker。

**调度算法：**

| 算法 | 说明 | 适用场景 |
|------|------|----------|
| **加权轮询（WRR）** | 按 Worker 权重轮流分配 | 默认策略，简单高效 |
| **最少活跃任务（Least Active）** | 分配给当前执行任务最少的 Worker | 任务执行时间差异大时 |
| **一致性哈希** | 相同 Handler 的任务倾向分配到同一 Worker | 需要本地缓存的场景 |
| **任务窃取（Work Stealing）** | 空闲 Worker 主动从忙碌 Worker 偷任务 | 高级模式，最大化利用率 |

```go
type Scheduler interface {
    // 为任务选择最合适的 Worker
    Schedule(task *Task, workers []*WorkerInfo) (*WorkerInfo, error)
}

type WorkerInfo struct {
    ID            string
    Addr          string
    Labels        map[string]string  // 标签（GPU/CPU/内存等）
    ActiveTasks   int
    Capacity      int
    LastHeartbeat time.Time
    Weight        int
}
```

**任务亲和性：** 通过 Label Selector 实现
```yaml
tasks:
  render-video:
    handler: video.render
    selector:
      matchLabels:
        gpu: "true"
        region: "cn-north"
```

### 5.3 Worker 管理器

**心跳机制：**
```
Worker                          Coordinator
  │                                  │
  │──── gRPC Bidirectional Stream ──→│
  │                                  │
  │◄──── Heartbeat Ping ────────────│  (每 10s)
  │───── Heartbeat Pong + Status ──→│
  │                                  │
  │◄──── Task Assignment ──────────│
  │───── Task Progress ───────────→│
  │───── Task Result ─────────────→│
```

- Worker 通过 gRPC 双向流保持长连接
- Coordinator 每 10s 发 Ping，Worker 回 Pong 并附带当前状态
- 3 次 Ping 无响应（30s）→ 标记 Worker 为 SUSPECT
- 60s 仍无响应 → 标记为 DEAD，其上的任务重新调度

**Worker 注册信息：**
```go
type WorkerRegistration struct {
    ID       string
    Addr     string
    Labels   map[string]string
    Capacity int  // 最大并发任务数
    Handlers []string  // 支持的 Handler 列表
}
```

### 5.4 事件溯源模块

所有状态变更记录为不可变事件，支持状态重建和审计。

```go
type Event struct {
    ID          string
    WorkflowID  string
    TaskID      string  // 可选
    Type        EventType
    Payload     json.RawMessage
    Timestamp   time.Time
    SequenceNum int64
}

type EventType string
const (
    WorkflowSubmitted  EventType = "WORKFLOW_SUBMITTED"
    WorkflowStarted    EventType = "WORKFLOW_STARTED"
    WorkflowCompleted  EventType = "WORKFLOW_COMPLETED"
    WorkflowFailed     EventType = "WORKFLOW_FAILED"
    TaskScheduled      EventType = "TASK_SCHEDULED"
    TaskStarted        EventType = "TASK_STARTED"
    TaskCompleted      EventType = "TASK_COMPLETED"
    TaskFailed         EventType = "TASK_FAILED"
    TaskRetrying       EventType = "TASK_RETRYING"
    TaskCompensating   EventType = "TASK_COMPENSATING"
)
```

**事件回放：** 从事件序列重建任意时刻的工作流状态
```go
func ReplayWorkflow(events []Event) (*WorkflowState, error) {
    state := &WorkflowState{}
    for _, event := range events {
        state.Apply(event)
    }
    return state, nil
}
```

### 5.5 Saga 补偿事务

当工作流失败时，自动执行已完成任务的补偿操作。

```yaml
tasks:
  create-order:
    handler: order.create
    compensate: order.cancel  # 补偿操作

  charge-payment:
    handler: payment.charge
    depends_on: [create-order]
    compensate: payment.refund

  send-notification:
    handler: notify.send
    depends_on: [charge-payment]
    # 无补偿，通知失败不需要回滚
```

**补偿执行顺序：** 按拓扑逆序执行已完成任务的补偿操作

```
正常流程: A → B → C → D(失败!)
补偿流程: C.compensate → B.compensate → A.compensate
```

### 5.6 Cron 调度器

支持定时触发工作流。

```go
type CronTrigger struct {
    WorkflowName string
    Cron         string        // "0 */5 * * * *" (每5分钟)
    Params       map[string]interface{}
    MaxConcurrent int          // 最大并发执行数
    MisfirePolicy MisfirePolicy // FIRE_ONCE | SKIP | FIRE_ALL
}
```

使用**分布式锁**确保多 Coordinator 实例下 Cron 只触发一次：
```go
func (s *CronScheduler) fire(trigger CronTrigger) {
    lock, err := s.coordinator.Lock(ctx, "cron:"+trigger.WorkflowName)
    if err != nil {
        return // 其他实例已获取锁
    }
    defer lock.Unlock()
    s.submitWorkflow(trigger)
}
```

---

## 5.7 Wasm 插件系统（WASI P2 Component Model — 2024 新标准）

不是所有任务都需要独立部署一个 Worker。轻量级任务可以通过 **WebAssembly 插件**直接在 Coordinator 或通用 Worker 内沙箱执行，无需额外进程。

**为什么用 WASI P2 而不是传统 Wasm：**

| 特性 | 传统 Wasm (WASI P1) | **WASI P2 Component Model (2024)** |
|------|---------------------|--------------------------------------|
| 接口定义 | 手动约定内存布局 | **WIT (Wasm Interface Type)** 声明式接口 |
| 插件组合 | 不支持 | **组件可组合**：A 的输出直接接 B 的输入 |
| 类型安全 | 只有 i32/i64/f32/f64 | **丰富类型**：string, list, record, variant, result |
| 语言互操作 | 手动 FFI | **自动绑定生成**：wit-bindgen 一键生成 Go/Rust/Python/JS 绑定 |
| 热插拔 | 需重启 | **运行时动态加载/卸载组件** |
| 标准化 | 碎片化 | **W3C 标准**，ByteCode Alliance 主导 |

**WIT 接口定义（插件契约）：**
```wit
// wit/forge-plugin.wit — 所有插件必须实现的接口
package forge:plugin@1.0.0;

interface task {
    record task-input {
        params: list<tuple<string, string>>,
        metadata: option<string>,
    }

    record task-output {
        result: string,
        artifacts: list<string>,
    }

    variant task-error {
        transient(string),    // 可重试的错误
        permanent(string),    // 不可重试的错误
    }

    execute: func(input: task-input) -> result<task-output, task-error>;
}

world forge-plugin {
    export task;
}
```

**Go 侧实现（使用 wazero + WASI P2 支持）：**
```go
import (
    "github.com/tetratelabs/wazero"
    "github.com/tetratelabs/wazero/imports/wasi_snapshot_preview2"
)

type WasmExecutor struct {
    runtime wazero.Runtime
    cache   wazero.CompilationCache
}

func (e *WasmExecutor) Execute(ctx context.Context, component []byte, input TaskInput) (*TaskOutput, error) {
    // WASI P2: 使用 Component Model 加载，自动类型转换
    compiled, err := e.runtime.CompileModule(ctx, component)
    if err != nil {
        return nil, fmt.Errorf("compile component: %w", err)
    }

    // 沙箱配置：WASI P2 的 capability-based security
    config := wazero.NewModuleConfig().
        WithStartFunctions("_start").
        WithSysWalltime().
        WithFSConfig(wazero.NewFSConfig()) // 空 = 无磁盘访问

    // Component Model 自动处理类型编解码，不需要手动序列化
    execCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
    defer cancel()

    mod, err := e.runtime.InstantiateModule(execCtx, compiled, config)
    if err != nil {
        return nil, fmt.Errorf("execute component: %w", err)
    }
    defer mod.Close(ctx)

    return parseOutput(mod), nil
}

// 组件组合：A 的输出自动传给 B（WASI P2 独有能力）
func (e *WasmExecutor) Pipeline(ctx context.Context, components [][]byte, input TaskInput) (*TaskOutput, error) {
    current := input
    for _, comp := range components {
        output, err := e.Execute(ctx, comp, current)
        if err != nil {
            return nil, err
        }
        current = outputToInput(output) // 类型安全的自动转换
    }
    return current.(*TaskOutput), nil
}
```

**插件可以用任何语言编写，编译为 WASI P2 Component：**
```bash
# Go 插件
tinygo build -target=wasip2 -o transform.wasm ./plugins/transform/

# Rust 插件
cargo component build --release  # cargo-component 工具链

# Python 插件（通过 componentize-py）
componentize-py -d wit/forge-plugin.wit -w forge-plugin componentize app -o analyze.wasm

# C++ 插件（通过 wit-bindgen-c）
wit-bindgen c wit/forge-plugin.wit && clang --target=wasm32-wasip2 -o validate.wasm plugin.c
```

**堆料亮点：**
- WASI P2 是 2024 年才稳定的最新标准，国内几乎无人在项目中使用
- Component Model 的组件组合能力实现了"无代码 ETL pipeline"
- wit-bindgen 自动生成多语言绑定，呼应项目的多语言主题
- 面试时一句话：**"我用 WASI P2 Component Model 实现了插件系统，支持 Go/Rust/Python/C++ 编写的组件在沙箱中组合执行"**

---

## 5.8 eBPF 内核级可观测性 + OTel Continuous Profiling（第四信号）

除了 Prometheus + Jaeger 的应用层可观测性，加入 **eBPF** 做内核级无侵入观测，并整合 **OpenTelemetry Profiling**（2024 年正式合入 OTel 的第四种信号类型）。

### 可观测性四信号架构（2024+ 标准）

```
传统三信号（2023 前）          四信号（2024+ OTel 标准）
┌─────────┐                  ┌─────────┐
│ Metrics │                  │ Metrics │
├─────────┤                  ├─────────┤
│ Traces  │        →         │ Traces  │
├─────────┤                  ├─────────┤
│ Logs    │                  │ Logs    │
└─────────┘                  ├─────────┤
                             │Profiling│ ← NEW! 持续性能剖析
                             └─────────┘
```

**OTel Profiling 解决什么问题：**
- Metrics 告诉你"CPU 高了"，但不告诉你"哪个函数导致的"
- Tracing 告诉你"请求慢了"，但不告诉你"慢在哪行代码"
- **Profiling 精确到函数级/行级**，持续采样，零性能开销（eBPF 实现）

**Go 侧集成（OTel Profiling SDK — 2024 新 API）：**
```go
import (
    "go.opentelemetry.io/auto"                    // OTel 自动 instrumentation
    "go.opentelemetry.io/contrib/profiling/pprof"  // OTel Profiling 桥接
)

func initProfiling() {
    // 持续 Profiling：每 10 秒采样一次 CPU/Memory/Goroutine profile
    profiler, _ := pprof.NewProfiler(
        pprof.WithPeriod(10 * time.Second),
        pprof.WithProfileTypes(
            pprof.CPUProfile,
            pprof.HeapProfile,
            pprof.GoroutineProfile,
            pprof.MutexProfile,       // 锁竞争分析
            pprof.BlockProfile,       // 阻塞分析
        ),
        // 导出到 OTel Collector → Grafana Pyroscope
        pprof.WithExporter(otlpProfileExporter("otel-collector:4317")),
    )
    profiler.Start()
}
```

**与 eBPF 联动 — 内核态 + 用户态双重剖析：**

**能力矩阵：**

| 观测维度 | 传统方案 | eBPF 方案 | 优势 |
|----------|----------|-----------|------|
| 网络延迟 | 应用层埋点 | TCP 内核事件追踪 | 零代码侵入，精确到微秒 |
| 系统调用 | strace（有性能开销） | seccomp + eBPF | 开销 < 1%，可生产环境常驻 |
| gRPC 流量 | 中间件拦截 | socket 层自动解析 | 不需要修改任何代码 |
| 内存泄漏 | pprof 手动触发 | 内核 RSS 监控 + OOM 预警 | 实时监控，自动告警 |
| 调度延迟 | goroutine 埋点 | 内核调度器事件 | 能看到 OS 层面的调度抖动 |

**Go 侧集成（使用 cilium/ebpf 库）：**
```go
import "github.com/cilium/ebpf"

type eBPFObserver struct {
    tcpLatencyProg *ebpf.Program
    grpcParseProg  *ebpf.Program
}

// 加载 eBPF 程序到内核，自动追踪 Worker 间 gRPC 调用延迟
func (o *eBPFObserver) AttachTCPLatency() error {
    spec, err := ebpf.LoadCollectionSpec("bpf/tcp_latency.o")
    if err != nil {
        return err
    }
    coll, err := ebpf.NewCollection(spec)
    if err != nil {
        return err
    }
    // attach 到 kprobe/tcp_rcv_established
    link, err := kprobe.Kprobe("tcp_rcv_established", coll.Programs["trace_tcp"], nil)
    if err != nil {
        return err
    }
    // 从 perf buffer 读取延迟数据 → 导出到 Prometheus
    go o.readPerfEvents(coll.Maps["latency_events"])
    return nil
}
```

**eBPF 程序（C，编译为 BPF 字节码）：**
```c
// bpf/tcp_latency.c
SEC("kprobe/tcp_rcv_established")
int trace_tcp(struct pt_regs *ctx) {
    struct sock *sk = (struct sock *)PT_REGS_PARM1(ctx);
    u64 ts = bpf_ktime_get_ns();
    u32 pid = bpf_get_current_pid_tgid() >> 32;

    struct latency_event evt = {
        .pid = pid,
        .timestamp_ns = ts,
        .saddr = sk->__sk_common.skc_rcv_saddr,
        .daddr = sk->__sk_common.skc_daddr,
        .sport = sk->__sk_common.skc_num,
        .dport = sk->__sk_common.skc_dport,
    };

    bpf_perf_event_output(ctx, &latency_events, BPF_F_CURRENT_CPU, &evt, sizeof(evt));
    return 0;
}
```

**堆料亮点：**
- Go + eBPF + C 三种语言协同，内核级可观测性是顶级基础设施项目才有的
- **OTel Profiling 是 2024 年才正式加入 OpenTelemetry 的第四信号**，极少有开源项目集成
- 持续 Profiling + eBPF 联动 = "我能看到从内核 TCP 栈到 Go 函数调用栈的全链路性能瓶颈"
- 面试杀招：**"我的引擎实现了 OpenTelemetry 四信号全覆盖，包括 2024 年刚加入的 Continuous Profiling"**

### Grafana 全家桶集成

```
┌────────────────────────────────────────────────────────┐
│                  Grafana 统一观测平台                    │
├──────────────┬─────────────────────────────────────────┤
│ Metrics      │ Prometheus → Grafana Dashboard          │
│ Traces       │ OTel → Tempo → Grafana Traces           │
│ Logs         │ zerolog → Loki → Grafana Explore         │
│ Profiling    │ OTel Profiling → Pyroscope → Flame Graph │
│ eBPF         │ cilium/ebpf → Prometheus → Dashboard     │
└──────────────┴─────────────────────────────────────────┘

关联查询：从慢 Trace → 定位到 Profile 火焰图 → 精确到 Go 函数行号
```

---

## 5.9 CDC 数据源接入（Change Data Capture）

Forge 不仅能调度"被提交的"工作流，还能**自动监听数据变更触发工作流**——这才是真正的事件驱动架构。

**支持的 CDC 源：**

| 数据源 | 协议 | 用途示例 |
|--------|------|----------|
| PostgreSQL | Logical Replication（pgoutput） | 订单表变更 → 触发通知工作流 |
| MySQL | Binlog（canal 协议） | 商品更新 → 触发同步工作流 |
| Redis | Keyspace Notification | 缓存过期 → 触发刷新工作流 |
| Kafka | Consumer Group | 外部事件 → 触发处理工作流 |

**PG CDC 实现：**
```go
type PGCDCSource struct {
    conn        *pgx.Conn
    slotName    string
    publication string
}

func (s *PGCDCSource) Subscribe(ctx context.Context, handler func(CDCEvent)) error {
    // 创建逻辑复制槽
    _, err := s.conn.Exec(ctx,
        "SELECT pg_create_logical_replication_slot($1, 'pgoutput')", s.slotName)
    if err != nil {
        return err
    }

    // 启动流式复制
    err = pglogrepl.StartReplication(ctx, s.conn, s.slotName, 0,
        pglogrepl.StartReplicationOptions{
            PluginArgs: []string{
                "proto_version '1'",
                fmt.Sprintf("publication_names '%s'", s.publication),
            },
        })

    // 持续读取 WAL 变更
    for {
        msg, err := s.conn.ReceiveMessage(ctx)
        if err != nil {
            return err
        }
        // 解析 WAL → 触发工作流
        event := s.parseWAL(msg)
        handler(event)
    }
}
```

**CDC 触发器配置（YAML）：**
```yaml
triggers:
  - name: order-created
    type: cdc
    source:
      type: postgres
      table: orders
      events: [INSERT]
      filter: "status = 'pending'"     # 只关注特定变更
    workflow: process-order             # 触发哪个工作流
    params_mapping:
      order_id: "{{.new.id}}"
      amount: "{{.new.total_amount}}"

  - name: video-uploaded
    type: cdc
    source:
      type: redis
      pattern: "upload:video:*"
      events: [SET]
    workflow: video-pipeline
```

**堆料亮点：** CDC + 工作流引擎 = 事件驱动架构的完整实现，面试时说"我的引擎能监听数据库 binlog 自动触发工作流"直接起飞

---

## 5.10 内置 Admin Dashboard（Web UI）

不只是 CLI 工具，提供完整的 Web 管理界面：

**技术选型：**
- **后端**：已有的 gRPC-Gateway REST API，零额外开发
- **前端**：React + Ant Design Pro / Next.js（SSR）
- **实时推送**：gRPC-Web + Server-Sent Events（SSE）

**Dashboard 功能：**
```
┌────────────────────────────────────────────────────────┐
│  🔧 Forge Dashboard                    [admin] [logout]│
├──────────┬─────────────────────────────────────────────┤
│          │                                             │
│ Overview │  Active Workflows: 142    Workers: 12       │
│ Workflows│  Success Rate: 98.7%      Queue Depth: 23   │
│ Workers  │                                             │
│ Tasks    │  ┌─────────────────────────────────────┐    │
│ CDC      │  │ 📊 Workflow Throughput (realtime)   │    │
│ Plugins  │  │  ████████████████░░░░ 1,247/min     │    │
│ Logs     │  └─────────────────────────────────────┘    │
│ Settings │                                             │
│          │  Recent Workflows:                          │
│          │  ✅ video-pipeline-#1842   2.3s   3 tasks   │
│          │  ✅ data-sync-#1841        0.8s   5 tasks   │
│          │  🔄 ai-generate-#1843     running  2/4     │
│          │  ❌ order-process-#1840   failed   step 3   │
│          │                                             │
│          │  [View All] [Create Workflow] [Import YAML] │
└──────────┴─────────────────────────────────────────────┘
```

**核心页面：**
- **工作流可视化**：DAG 图形渲染（D3.js / ELK 布局），实时显示每个节点的执行状态
- **Worker 拓扑**：展示所有 Worker 节点、语言类型、负载、健康状态
- **CDC 监控**：数据源连接状态、事件吞吐量、延迟
- **Wasm 插件市场**：上传/管理/版本控制 .wasm 插件
- **事件时间线**：按工作流 ID 查看完整事件溯源链

---

## 6. 数据模型

### 6.1 PostgreSQL 表设计

```sql
-- 工作流定义
CREATE TABLE workflow_definitions (
    id          BIGSERIAL PRIMARY KEY,
    name        VARCHAR(255) NOT NULL,
    version     INT NOT NULL DEFAULT 1,
    dag_yaml    JSONB NOT NULL,          -- DAG 定义
    created_at  TIMESTAMPTZ DEFAULT NOW(),
    updated_at  TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(name, version)
);

-- 工作流实例（每次执行一条记录）
CREATE TABLE workflow_instances (
    id          VARCHAR(36) PRIMARY KEY, -- UUID
    def_id      BIGINT REFERENCES workflow_definitions(id),
    status      VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    -- PENDING / RUNNING / COMPLETED / FAILED / CANCELLED / COMPENSATING
    input       JSONB,
    output      JSONB,
    error_msg   TEXT,
    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ DEFAULT NOW(),
    timeout_at  TIMESTAMPTZ              -- 工作流级超时
);
CREATE INDEX idx_wf_status ON workflow_instances(status);
CREATE INDEX idx_wf_created ON workflow_instances(created_at);

-- 任务实例
CREATE TABLE task_instances (
    id              VARCHAR(36) PRIMARY KEY, -- UUID
    workflow_id     VARCHAR(36) REFERENCES workflow_instances(id),
    task_name       VARCHAR(255) NOT NULL,
    handler         VARCHAR(255) NOT NULL,
    status          VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    -- PENDING / READY / SCHEDULED / RUNNING / COMPLETED / FAILED / SKIPPED / COMPENSATING
    worker_id       VARCHAR(255),
    input           JSONB,
    output          JSONB,
    error_msg       TEXT,
    attempt         INT DEFAULT 0,
    max_attempts    INT DEFAULT 1,
    scheduled_at    TIMESTAMPTZ,
    started_at      TIMESTAMPTZ,
    finished_at     TIMESTAMPTZ,
    timeout_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX idx_task_wf ON task_instances(workflow_id);
CREATE INDEX idx_task_status ON task_instances(status);
CREATE INDEX idx_task_worker ON task_instances(worker_id);
-- 用于 SKIP LOCKED 的任务认领
CREATE INDEX idx_task_claim ON task_instances(status, handler) WHERE status = 'READY';

-- 事件日志（事件溯源）
CREATE TABLE events (
    id              BIGSERIAL PRIMARY KEY,
    workflow_id     VARCHAR(36) NOT NULL,
    task_id         VARCHAR(36),
    event_type      VARCHAR(50) NOT NULL,
    payload         JSONB,
    sequence_num    BIGINT NOT NULL,
    created_at      TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX idx_event_wf ON events(workflow_id, sequence_num);

-- Cron 触发器
CREATE TABLE cron_triggers (
    id              BIGSERIAL PRIMARY KEY,
    workflow_name   VARCHAR(255) NOT NULL,
    cron_expr       VARCHAR(100) NOT NULL,
    params          JSONB,
    max_concurrent  INT DEFAULT 1,
    enabled         BOOLEAN DEFAULT TRUE,
    last_fire_at    TIMESTAMPTZ,
    next_fire_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ DEFAULT NOW()
);
```

### 6.2 Redis 数据结构

```
# Worker 心跳（Hash）
forge:worker:{worker_id}  → { addr, labels, capacity, active_tasks, last_heartbeat }
TTL: 60s（自动过期 = Worker 死亡）

# Worker 集合（Set）
forge:workers → { worker_id_1, worker_id_2, ... }

# 分布式锁
forge:lock:{resource} → { owner, expire_at }

# 速率限制（Sorted Set）
forge:ratelimit:{handler} → { request_id: timestamp, ... }

# 任务通知（Stream）
forge:tasks:ready → [ {workflow_id, task_id, handler, ...} ]
```

---

## 7. 关键算法与机制

### 7.1 DAG 拓扑排序与环检测

```go
func (d *DAG) TopologicalSort() ([]string, error) {
    inDegree := make(map[string]int)
    for name := range d.Tasks {
        inDegree[name] = 0
    }
    for name, task := range d.Tasks {
        for _, dep := range task.DependsOn {
            inDegree[name]++
        }
    }

    queue := make([]string, 0)
    for name, degree := range inDegree {
        if degree == 0 {
            queue = append(queue, name)
        }
    }

    var sorted []string
    for len(queue) > 0 {
        node := queue[0]
        queue = queue[1:]
        sorted = append(sorted, node)

        // 找到所有依赖 node 的任务
        for name, task := range d.Tasks {
            for _, dep := range task.DependsOn {
                if dep == node {
                    inDegree[name]--
                    if inDegree[name] == 0 {
                        queue = append(queue, name)
                    }
                }
            }
        }
    }

    if len(sorted) != len(d.Tasks) {
        return nil, fmt.Errorf("DAG contains cycle")
    }
    return sorted, nil
}
```

### 7.2 指数退避重试（带抖动）

```go
func calculateBackoff(attempt int, policy RetryPolicy) time.Duration {
    if policy.BackoffType == FIXED {
        return policy.InitialInterval
    }

    // 指数退避
    backoff := float64(policy.InitialInterval) * math.Pow(policy.Multiplier, float64(attempt-1))

    // 上限
    if time.Duration(backoff) > policy.MaxInterval {
        backoff = float64(policy.MaxInterval)
    }

    // Full Jitter: [0, backoff)
    jittered := time.Duration(rand.Float64() * backoff)

    return jittered
}
```

### 7.3 任务认领（基于 PG FOR UPDATE SKIP LOCKED）

```go
func (s *PGStorage) ClaimTask(ctx context.Context, workerID string, handlers []string) (*Task, error) {
    tx, _ := s.db.BeginTx(ctx, nil)
    defer tx.Rollback()

    var task Task
    err := tx.QueryRowContext(ctx, `
        UPDATE task_instances
        SET status = 'SCHEDULED', worker_id = $1, scheduled_at = NOW()
        WHERE id = (
            SELECT id FROM task_instances
            WHERE status = 'READY' AND handler = ANY($2)
            ORDER BY created_at
            FOR UPDATE SKIP LOCKED
            LIMIT 1
        )
        RETURNING id, workflow_id, task_name, handler, input
    `, workerID, handlers).Scan(&task.ID, &task.WorkflowID, &task.Name, &task.Handler, &task.Input)

    if err != nil {
        return nil, err
    }

    tx.Commit()
    return &task, nil
}
```

### 7.4 分布式定时器（时间轮）

```go
type TimingWheel struct {
    tick     time.Duration  // 每格时间
    size     int            // 格数
    current  int            // 当前指针
    slots    []list.List    // 每格一个链表
    overflow *TimingWheel   // 层级时间轮
    mu       sync.Mutex
}

// 添加定时任务
func (tw *TimingWheel) Add(delay time.Duration, callback func()) {
    ticks := int(delay / tw.tick)
    if ticks < tw.size {
        slot := (tw.current + ticks) % tw.size
        tw.slots[slot].PushBack(callback)
    } else {
        // 溢出到上层时间轮
        tw.overflow.Add(delay, callback)
    }
}
```

### 7.5 Go 1.22+ 新特性应用（展示最新语言能力）

项目全面拥抱 Go 最新版本特性，体现作者对语言演进的关注：

**range-over-func（Go 1.23）— 自定义迭代器：**
```go
// DAG 遍历迭代器 — 用 range-over-func 实现优雅的拓扑遍历
func (d *DAG) TopologicalOrder() iter.Seq[*TaskDef] {
    return func(yield func(*TaskDef) bool) {
        inDegree := make(map[string]int)
        for name, task := range d.Tasks {
            inDegree[name] = len(task.DependsOn)
        }

        queue := make([]string, 0)
        for name, deg := range inDegree {
            if deg == 0 {
                queue = append(queue, name)
            }
        }

        for len(queue) > 0 {
            name := queue[0]
            queue = queue[1:]
            if !yield(d.Tasks[name]) {
                return // 支持 break
            }
            for next, task := range d.Tasks {
                for _, dep := range task.DependsOn {
                    if dep == name {
                        inDegree[next]--
                        if inDegree[next] == 0 {
                            queue = append(queue, next)
                        }
                    }
                }
            }
        }
    }
}

// 使用：像遍历 slice 一样遍历 DAG
for task := range dag.TopologicalOrder() {
    fmt.Printf("Ready: %s (handler: %s)\n", task.Name, task.Handler)
}

// 事件流迭代器
func (s *EventStore) Events(workflowID string) iter.Seq2[int, *Event] {
    return func(yield func(int, *Event) bool) {
        rows, _ := s.db.Query("SELECT * FROM events WHERE workflow_id = $1 ORDER BY sequence_num", workflowID)
        defer rows.Close()
        i := 0
        for rows.Next() {
            var evt Event
            rows.Scan(&evt)
            if !yield(i, &evt) {
                return
            }
            i++
        }
    }
}
```

**unique 包（Go 1.23）— 字符串驻留/去重：**
```go
import "unique"

// Worker ID 和 Handler 名称大量重复出现，用 unique.Handle 去重节省内存
type WorkerRegistry struct {
    workers map[unique.Handle[string]]*WorkerInfo
}

func (r *WorkerRegistry) Register(workerID string, info *WorkerInfo) {
    // unique.Make 自动驻留字符串，相同内容只存一份
    handle := unique.Make(workerID)
    r.workers[handle] = info
}

// 在大规模集群中（10k+ Worker），内存占用显著降低
// 对比：map[string] 每个 key 独立分配 vs unique.Handle 共享底层存储
```

**log/slog 结构化日志（Go 1.21+）— 替代 zerolog 的标准库方案：**
```go
import "log/slog"

// 项目同时支持 zerolog 和 slog，通过接口适配
// slog 是 Go 官方推荐的结构化日志方案（1.21+），面试加分

var logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
    Level: slog.LevelInfo,
    ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
        if a.Key == slog.TimeKey {
            a.Value = slog.StringValue(a.Value.Time().Format(time.RFC3339Nano))
        }
        return a
    },
}))

func (c *Coordinator) scheduleTask(ctx context.Context, task *Task) {
    logger.InfoContext(ctx, "task scheduled",
        slog.String("workflow_id", task.WorkflowID),
        slog.String("task_id", task.ID),
        slog.String("handler", task.Handler),
        slog.String("worker_id", task.WorkerID),
        slog.Duration("timeout", task.Timeout),
        slog.Group("retry",
            slog.Int("attempt", task.Attempt),
            slog.Int("max", task.MaxAttempts),
        ),
    )
}
```

**堆料亮点：**
- `iter.Seq` / `iter.Seq2` 是 Go 1.23（2024.8）才加入的，展示你用的是最新标准
- `unique` 包是 Go 1.23 的隐藏宝石，知道的人不多
- `log/slog` 是 Go 官方在 1.21 推的结构化日志标准，逐步替代第三方库
- **面试时："我的代码全面使用了 Go 1.23 的新特性，包括 range-over-func 自定义迭代器和 unique 包做字符串驻留优化"**

---

## 8. 可观测性设计

### 8.1 Metrics（Prometheus）

```go
var (
    workflowsSubmitted = promauto.NewCounter(prometheus.CounterOpts{
        Name: "forge_workflows_submitted_total",
        Help: "Total number of workflows submitted",
    })

    workflowDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
        Name:    "forge_workflow_duration_seconds",
        Help:    "Workflow execution duration",
        Buckets: []float64{1, 5, 10, 30, 60, 300, 600, 1800, 3600},
    }, []string{"workflow_name", "status"})

    taskDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
        Name:    "forge_task_duration_seconds",
        Help:    "Task execution duration",
        Buckets: []float64{0.1, 0.5, 1, 5, 10, 30, 60, 300},
    }, []string{"handler", "status"})

    activeWorkers = promauto.NewGauge(prometheus.GaugeOpts{
        Name: "forge_active_workers",
        Help: "Number of active workers",
    })

    taskQueueDepth = promauto.NewGaugeVec(prometheus.GaugeOpts{
        Name: "forge_task_queue_depth",
        Help: "Number of tasks waiting to be scheduled",
    }, []string{"handler"})

    taskRetries = promauto.NewCounterVec(prometheus.CounterOpts{
        Name: "forge_task_retries_total",
        Help: "Total number of task retries",
    }, []string{"handler", "reason"})
)
```

### 8.2 Tracing（OpenTelemetry）

```go
// 每个工作流一条 trace，每个任务一个 span
func (c *Coordinator) executeWorkflow(ctx context.Context, wf *Workflow) {
    ctx, span := tracer.Start(ctx, "workflow.execute",
        trace.WithAttributes(
            attribute.String("workflow.id", wf.ID),
            attribute.String("workflow.name", wf.Name),
        ),
    )
    defer span.End()

    for _, task := range wf.ReadyTasks() {
        c.scheduleTask(ctx, task) // 会创建子 span
    }
}
```

### 8.3 Grafana Dashboard 关键面板

| 面板 | 说明 |
|------|------|
| 工作流吞吐量 | 每秒完成的工作流数（按名称分组） |
| 任务队列深度 | 等待执行的任务数（按 Handler 分组） |
| 任务执行耗时 P50/P95/P99 | 各 Handler 的耗时分布 |
| Worker 健康状态 | 活跃/可疑/死亡 Worker 数量 |
| 重试率 | 各 Handler 的重试比例 |
| 失败工作流 | 最近失败的工作流列表 |

---

## 9. 部署方案

### 9.1 开发环境（docker-compose）

```yaml
version: "3.8"
services:
  coordinator:
    build: .
    command: ["forge", "coordinator", "--embed-etcd"]
    ports:
      - "8080:8080"   # gRPC
      - "8081:8081"   # REST (gRPC-Gateway)
      - "9090:9090"   # Metrics
    depends_on:
      - postgres
      - redis

  worker-go:
    build: .
    command: ["forge", "worker", "--coordinator=coordinator:8080"]
    deploy:
      replicas: 3
    depends_on:
      - coordinator

  worker-python:
    build:
      context: ./sdk/python
      dockerfile: Dockerfile
    command: ["python", "-m", "forge_worker", "--coordinator=coordinator:8080"]
    deploy:
      replicas: 2
    depends_on:
      - coordinator

  worker-cpp:
    build:
      context: ./sdk/cpp
      dockerfile: Dockerfile
    command: ["./forge-worker", "--coordinator=coordinator:8080"]
    deploy:
      replicas: 1
    depends_on:
      - coordinator

  postgres:
    image: postgres:17
    environment:
      POSTGRES_DB: forge
      POSTGRES_USER: forge
      POSTGRES_PASSWORD: forge
    ports:
      - "5433:5432"
    volumes:
      - pgdata:/var/lib/postgresql/data

  redis:
    image: redis:7-alpine
    ports:
      - "6380:6379"

  prometheus:
    image: prom/prometheus
    volumes:
      - ./deploy/prometheus.yml:/etc/prometheus/prometheus.yml
    ports:
      - "9091:9090"

  grafana:
    image: grafana/grafana
    ports:
      - "3000:3000"
    depends_on:
      - prometheus

  jaeger:
    image: jaegertracing/all-in-one
    ports:
      - "16686:16686"  # UI
      - "4317:4317"    # OTLP gRPC

volumes:
  pgdata:
```

### 9.2 单二进制模式

```bash
# 全部组件跑在一个进程里（开发/演示用）
forge standalone --db=postgres://... --redis=redis://...

# 分角色启动
forge coordinator --embed-etcd --db=postgres://... --redis=redis://...
forge worker --coordinator=localhost:8080
```

### 9.3 Kubernetes 云原生部署

#### 9.3.1 整体 K8s 架构

```
┌─────────────────── Kubernetes Cluster ───────────────────┐
│                                                           │
│  ┌─────────────────────────────────────────────────────┐  │
│  │              Namespace: forge-system                 │  │
│  │                                                     │  │
│  │  ┌──────────────┐  StatefulSet (3 replicas)         │  │
│  │  │ Coordinator  │  ← Headless Service (leader选举)  │  │
│  │  │   Pod 0 ★    │  ← PDB: minAvailable=2           │  │
│  │  │   Pod 1      │                                   │  │
│  │  │   Pod 2      │                                   │  │
│  │  └──────┬───────┘                                   │  │
│  │         │ gRPC                                      │  │
│  │  ┌──────┴──────────────────────────────────────┐    │  │
│  │  │           Worker Deployments                │    │  │
│  │  │                                             │    │  │
│  │  │  ┌─────────────┐  Deployment + HPA          │    │  │
│  │  │  │ Go Workers  │  min=2, max=20             │    │  │
│  │  │  │ (general)   │  scale on: CPU/队列深度    │    │  │
│  │  │  └─────────────┘                            │    │  │
│  │  │  ┌─────────────┐  Deployment + HPA          │    │  │
│  │  │  │ Python Wkrs │  min=1, max=10             │    │  │
│  │  │  │ (AI tasks)  │  scale on: GPU利用率       │    │  │
│  │  │  └─────────────┘                            │    │  │
│  │  │  ┌─────────────┐  Deployment + HPA          │    │  │
│  │  │  │ C++ Workers │  min=1, max=5              │    │  │
│  │  │  │ (render)    │  scale on: CPU             │    │  │
│  │  │  └─────────────┘                            │    │  │
│  │  └─────────────────────────────────────────────┘    │  │
│  │                                                     │  │
│  │  ┌──────────┐  ┌───────┐  ┌────────┐  ┌─────────┐  │  │
│  │  │ PG (CRD) │  │ Redis │  │ Jaeger │  │Prometheus│  │  │
│  │  │ Operator │  │Sentinel│  │Operator│  │Stack    │  │  │
│  │  └──────────┘  └───────┘  └────────┘  └─────────┘  │  │
│  └─────────────────────────────────────────────────────┘  │
│                                                           │
│  Ingress / Gateway API                                    │
│  ┌─────────────────────────────────────┐                  │
│  │  forge.example.com → gRPC-Gateway   │                  │
│  │  forge.example.com/metrics → Prom   │                  │
│  │  forge.example.com/trace → Jaeger   │                  │
│  └─────────────────────────────────────┘                  │
└───────────────────────────────────────────────────────────┘
```

#### 9.3.2 Coordinator — StatefulSet

Coordinator 是有状态的（嵌入式 etcd 需要稳定网络标识），用 StatefulSet 部署：

```yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: forge-coordinator
  namespace: forge-system
spec:
  serviceName: forge-coordinator-headless
  replicas: 3
  selector:
    matchLabels:
      app: forge-coordinator
  template:
    metadata:
      labels:
        app: forge-coordinator
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "9090"
    spec:
      containers:
        - name: coordinator
          image: forge/coordinator:latest
          ports:
            - containerPort: 8080  # gRPC
              name: grpc
            - containerPort: 8081  # REST
              name: rest
            - containerPort: 9090  # Metrics
              name: metrics
            - containerPort: 2379  # etcd client
              name: etcd-client
            - containerPort: 2380  # etcd peer
              name: etcd-peer
          env:
            - name: POD_NAME
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
            - name: FORGE_ETCD_INITIAL_CLUSTER
              value: "forge-coordinator-0=http://forge-coordinator-0.forge-coordinator-headless:2380,forge-coordinator-1=http://forge-coordinator-1.forge-coordinator-headless:2380,forge-coordinator-2=http://forge-coordinator-2.forge-coordinator-headless:2380"
          resources:
            requests:
              cpu: "500m"
              memory: "512Mi"
            limits:
              cpu: "2"
              memory: "2Gi"
          readinessProbe:
            grpc:
              port: 8080
            initialDelaySeconds: 10
            periodSeconds: 5
          livenessProbe:
            grpc:
              port: 8080
            initialDelaySeconds: 30
            periodSeconds: 10
      topologySpreadConstraints:
        - maxSkew: 1
          topologyKey: kubernetes.io/hostname
          whenUnsatisfiable: DoNotSchedule
          labelSelector:
            matchLabels:
              app: forge-coordinator
  volumeClaimTemplates:
    - metadata:
        name: etcd-data
      spec:
        accessModes: ["ReadWriteOnce"]
        resources:
          requests:
            storage: 10Gi
---
# Headless Service（用于 StatefulSet DNS 和 etcd peer 通信）
apiVersion: v1
kind: Service
metadata:
  name: forge-coordinator-headless
  namespace: forge-system
spec:
  clusterIP: None
  selector:
    app: forge-coordinator
  ports:
    - port: 2380
      name: etcd-peer
---
# 普通 Service（供 Worker 和外部访问）
apiVersion: v1
kind: Service
metadata:
  name: forge-coordinator
  namespace: forge-system
spec:
  selector:
    app: forge-coordinator
  ports:
    - port: 8080
      name: grpc
    - port: 8081
      name: rest
```

#### 9.3.3 Worker — Deployment + HPA 弹性伸缩

Worker 无状态，用 Deployment 部署，按语言/能力分组：

```yaml
# Go Worker — 通用任务
apiVersion: apps/v1
kind: Deployment
metadata:
  name: forge-worker-go
  namespace: forge-system
spec:
  replicas: 2
  selector:
    matchLabels:
      app: forge-worker
      lang: go
  template:
    metadata:
      labels:
        app: forge-worker
        lang: go
        worker-type: general
    spec:
      containers:
        - name: worker
          image: forge/worker-go:latest
          args: ["--coordinator=forge-coordinator:8080", "--labels=lang=go,type=general"]
          resources:
            requests:
              cpu: "250m"
              memory: "256Mi"
            limits:
              cpu: "1"
              memory: "1Gi"
---
# HPA — 基于自定义指标（任务队列深度）自动扩缩
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: forge-worker-go-hpa
  namespace: forge-system
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: forge-worker-go
  minReplicas: 2
  maxReplicas: 20
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: 70
    - type: Pods
      pods:
        metric:
          name: forge_worker_active_tasks  # 自定义 Prometheus 指标
        target:
          type: AverageValue
          averageValue: "8"                # 每个 Pod 平均 8 个活跃任务时扩容
  behavior:
    scaleUp:
      stabilizationWindowSeconds: 30       # 快速扩容
      policies:
        - type: Pods
          value: 4
          periodSeconds: 60
    scaleDown:
      stabilizationWindowSeconds: 300      # 缓慢缩容，避免抖动
      policies:
        - type: Pods
          value: 2
          periodSeconds: 120
---
# Python Worker — AI/ML 任务（可挂载 GPU）
apiVersion: apps/v1
kind: Deployment
metadata:
  name: forge-worker-python
  namespace: forge-system
spec:
  replicas: 1
  selector:
    matchLabels:
      app: forge-worker
      lang: python
  template:
    metadata:
      labels:
        app: forge-worker
        lang: python
        worker-type: ai
    spec:
      containers:
        - name: worker
          image: forge/worker-python:latest
          args: ["--coordinator=forge-coordinator:8080", "--labels=lang=python,type=ai,gpu=true"]
          resources:
            requests:
              cpu: "500m"
              memory: "2Gi"
              nvidia.com/gpu: 1          # GPU 资源请求
            limits:
              cpu: "4"
              memory: "8Gi"
              nvidia.com/gpu: 1
      nodeSelector:
        accelerator: nvidia-gpu          # 调度到 GPU 节点
      tolerations:
        - key: nvidia.com/gpu
          operator: Exists
          effect: NoSchedule
```

#### 9.3.4 Forge Operator（CRD — 高级堆料）

自定义 Kubernetes Operator，用 `kubebuilder` 或 `operator-sdk` 生成脚手架：

```yaml
# 自定义 CRD：ForgeCluster
apiVersion: forge.io/v1alpha1
kind: ForgeCluster
metadata:
  name: production
spec:
  coordinator:
    replicas: 3
    resources:
      cpu: "2"
      memory: "4Gi"
    etcd:
      storageSize: 20Gi

  workers:
    - name: general
      language: go
      replicas: 2
      maxReplicas: 20
      handlers: ["*"]               # 处理所有类型任务
      autoScaling:
        metric: queue_depth
        threshold: 10

    - name: ai-tasks
      language: python
      replicas: 1
      maxReplicas: 10
      handlers: ["ai.*"]            # 只处理 ai.* 开头的任务
      gpu: true
      autoScaling:
        metric: gpu_utilization
        threshold: 80

    - name: render
      language: cpp
      replicas: 1
      maxReplicas: 5
      handlers: ["video.render", "data.compress"]
      autoScaling:
        metric: cpu
        threshold: 70

  storage:
    postgres:
      host: forge-pg-rw.forge-system
      database: forge
      secretRef: forge-pg-credentials
    redis:
      host: forge-redis.forge-system
      db: 0
      secretRef: forge-redis-credentials

  observability:
    prometheus: true
    jaeger:
      endpoint: jaeger-collector.observability:4317
    grafanaDashboard: true          # 自动创建 Grafana Dashboard CRD
```

**Operator 自动做的事：**
- 根据 `ForgeCluster` CRD 自动创建/更新 StatefulSet、Deployment、HPA、Service、PDB
- 监听 Worker 健康状态，自动替换 NotReady Pod
- 滚动更新时保证 Coordinator 的 Leader 最后更新（避免脑裂）
- 自动创建 Prometheus ServiceMonitor 和 Grafana Dashboard

**堆料亮点：** 自己写 K8s Operator 是云原生领域的硬核技能，面试直接加分

#### 9.3.5 Helm Chart 目录结构

```
deploy/helm/forge/
├── Chart.yaml
├── values.yaml                    # 默认配置
├── values-dev.yaml                # 开发环境覆盖
├── values-prod.yaml               # 生产环境覆盖
├── templates/
│   ├── _helpers.tpl
│   ├── namespace.yaml
│   ├── coordinator-statefulset.yaml
│   ├── coordinator-service.yaml
│   ├── coordinator-pdb.yaml       # PodDisruptionBudget
│   ├── worker-go-deployment.yaml
│   ├── worker-go-hpa.yaml
│   ├── worker-python-deployment.yaml
│   ├── worker-python-hpa.yaml
│   ├── worker-rust-deployment.yaml
│   ├── configmap.yaml
│   ├── secret.yaml
│   ├── ingress.yaml
│   ├── servicemonitor.yaml        # Prometheus Operator
│   └── grafana-dashboard.yaml     # Grafana Dashboard ConfigMap
└── crds/
    └── forgecluster-crd.yaml      # ForgeCluster CRD（如果用 Operator）
```

```bash
# 一键部署
helm install forge ./deploy/helm/forge -n forge-system --create-namespace

# 生产环境部署
helm install forge ./deploy/helm/forge -n forge-system \
  -f ./deploy/helm/forge/values-prod.yaml \
  --set coordinator.replicas=3 \
  --set workers.go.maxReplicas=50

# 升级
helm upgrade forge ./deploy/helm/forge -n forge-system
```

#### 9.3.6 Gateway API（K8s 新一代流量入口 — 取代 Ingress）

**为什么用 Gateway API 而不是传统 Ingress：**

| 特性 | 传统 Ingress | **Gateway API (2023 GA)** |
|------|-------------|--------------------------|
| gRPC 路由 | 需要注解 hack | **原生支持 GRPCRoute** |
| 多协议 | HTTP only | HTTP / gRPC / TCP / TLS |
| 角色分离 | 集群管理员一人搞定 | 基础设施 / 集群 / 应用三层分离 |
| 跨命名空间 | 不支持 | **ReferenceGrant 跨 NS 引用** |
| Header 匹配 | 有限 | 精确/正则/存在性匹配 |
| 状态 | 冻结，不再新增功能 | **活跃开发，K8s 官方推荐** |

```yaml
# Gateway API — gRPC 路由（原生支持，不需要 nginx annotation hack）
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: forge-gateway
  namespace: forge-system
spec:
  gatewayClassName: istio  # 或 envoy-gateway / cilium
  listeners:
    - name: grpc
      protocol: HTTPS
      port: 443
      tls:
        mode: Terminate
        certificateRefs:
          - name: forge-tls-cert
    - name: http
      protocol: HTTP
      port: 80
---
# gRPC 路由 — 按 service 和 method 精确路由
apiVersion: gateway.networking.k8s.io/v1
kind: GRPCRoute
metadata:
  name: forge-coordinator-grpc
  namespace: forge-system
spec:
  parentRefs:
    - name: forge-gateway
  rules:
    - matches:
        - method:
            service: forge.CoordinatorService
      backendRefs:
        - name: forge-coordinator
          port: 8080
    - matches:
        - method:
            service: forge.WorkerService
      backendRefs:
        - name: forge-coordinator
          port: 8080
---
# HTTP 路由 — Dashboard 和 REST API
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: forge-http
  namespace: forge-system
spec:
  parentRefs:
    - name: forge-gateway
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /api/v1
      backendRefs:
        - name: forge-coordinator
          port: 8081
    - matches:
        - path:
            type: PathPrefix
            value: /dashboard
      backendRefs:
        - name: forge-dashboard
          port: 3000
```

#### 9.3.7 Kueue — K8s 原生批任务队列（Google 2024）

**Kueue 是什么：** Google 2024 年推出的 K8s 原生任务排队系统，专为批处理/AI 训练/任务调度设计。Forge 与 Kueue 集成，让 K8s 成为任务调度的"第二大脑"。

**为什么和 Forge 天然契合：**
- Forge 负责 DAG 编排和业务逻辑
- Kueue 负责 K8s 层面的资源配额、公平调度、抢占
- 两者互补：Forge 管"做什么"，Kueue 管"用什么资源做"

```yaml
# Kueue ResourceFlavor — 定义不同类型的计算资源
apiVersion: kueue.x-k8s.io/v1beta1
kind: ResourceFlavor
metadata:
  name: gpu-a100
spec:
  nodeLabels:
    cloud.google.com/gke-accelerator: nvidia-tesla-a100
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: ResourceFlavor
metadata:
  name: cpu-general
spec:
  nodeLabels:
    node-type: general
---
# Kueue ClusterQueue — Forge 专属资源池
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: forge-queue
spec:
  resourceGroups:
    - coveredResources: ["cpu", "memory", "nvidia.com/gpu"]
      flavors:
        - name: gpu-a100
          resources:
            - name: "nvidia.com/gpu"
              nominalQuota: 4           # 最多 4 个 GPU
            - name: "cpu"
              nominalQuota: 16
            - name: "memory"
              nominalQuota: 64Gi
        - name: cpu-general
          resources:
            - name: "cpu"
              nominalQuota: 64
            - name: "memory"
              nominalQuota: 128Gi
  preemption:
    withinClusterQueue: LowerPriority   # 高优先级任务可抢占低优先级
---
# Kueue LocalQueue — Forge namespace 绑定
apiVersion: kueue.x-k8s.io/v1beta1
kind: LocalQueue
metadata:
  name: forge-tasks
  namespace: forge-system
spec:
  clusterQueue: forge-queue
```

**Forge Coordinator 与 Kueue 集成：**
```go
// 当任务需要独立 Pod 执行（如 GPU 任务）时，通过 Kueue 提交
func (c *Coordinator) submitToKueue(ctx context.Context, task *Task) error {
    job := &batchv1.Job{
        ObjectMeta: metav1.ObjectMeta{
            Name:      fmt.Sprintf("forge-task-%s", task.ID),
            Namespace: "forge-system",
            Labels: map[string]string{
                "kueue.x-k8s.io/queue-name": "forge-tasks",
                "forge.io/workflow-id":       task.WorkflowID,
                "forge.io/task-id":           task.ID,
                "forge.io/priority":          task.Priority,
            },
        },
        Spec: batchv1.JobSpec{
            Template: corev1.PodTemplateSpec{
                Spec: corev1.PodSpec{
                    Containers: []corev1.Container{{
                        Name:    "task",
                        Image:   task.Handler.Image(),
                        Command: task.Handler.Command(),
                        Resources: corev1.ResourceRequirements{
                            Requests: corev1.ResourceList{
                                "nvidia.com/gpu": resource.MustParse("1"),
                            },
                        },
                    }},
                    RestartPolicy: corev1.RestartPolicyNever,
                },
            },
        },
    }

    // Kueue 自动排队、分配资源、处理抢占
    _, err := c.k8sClient.BatchV1().Jobs("forge-system").Create(ctx, job, metav1.CreateOptions{})
    return err
}
```

**堆料亮点：**
- Gateway API 是 K8s 官方钦定的 Ingress 替代品，展示你跟进最新标准
- Kueue 是 Google 2024 年的重点项目，专为批任务/AI 训练设计
- **面试杀招："我的引擎在 K8s 上通过 Kueue 实现资源配额和公平调度，用 Gateway API 暴露 gRPC 原生路由，全部是 K8s 最新标准"**

```yaml
# .github/workflows/release.yml
name: Build & Deploy
on:
  push:
    tags: ["v*"]

jobs:
  build:
    strategy:
      matrix:
        component: [coordinator, worker-go, worker-python, worker-cpp]
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Build & Push Multi-arch Image
        uses: docker/build-push-action@v5
        with:
          context: .
          file: ./build/${{ matrix.component }}/Dockerfile
          platforms: linux/amd64,linux/arm64   # 多架构构建
          push: true
          tags: |
            ghcr.io/castwell/forge/${{ matrix.component }}:${{ github.ref_name }}
            ghcr.io/castwell/forge/${{ matrix.component }}:latest
          cache-from: type=gha
          cache-to: type=gha,mode=max

  deploy:
    needs: build
    runs-on: ubuntu-latest
    steps:
      - name: Update ArgoCD Application
        run: |
          # 更新 Helm values 中的 image tag，ArgoCD 自动同步
          yq -i '.coordinator.image.tag = "${{ github.ref_name }}"' deploy/helm/forge/values-prod.yaml
          git commit -am "release: ${{ github.ref_name }}" && git push
```

---

## 10. 实施计划

### Phase 1：地基（2-3 周）

| 任务 | 产出 | 技术点 |
|------|------|--------|
| 项目骨架搭建 | Go module、目录结构、Makefile、CI | 工程规范 |
| Protobuf 定义 + buf 配置 | 所有 gRPC 接口定义，buf.gen.yaml 多语言生成 | Protobuf + gRPC + buf |
| DAG 引擎 | YAML 解析、拓扑排序、环检测 | 图算法 |
| Storage 接口 + PG 实现 | 表结构、CRUD、任务认领 | PG、FOR UPDATE SKIP LOCKED |
| 基础 Coordinator | 接收工作流、解析 DAG、推进状态 | 状态机 |

**里程碑：** 单 Coordinator + 单 Go Worker，能跑通一个简单的线性工作流

### Phase 2：分布式（2-3 周）

| 任务 | 产出 | 技术点 |
|------|------|--------|
| Worker 管理 | 注册、心跳、故障检测 | gRPC Stream、健康检查 |
| 调度器 | 多种调度算法、Label Selector | 负载均衡算法 |
| 嵌入式 etcd | Leader 选举、服务发现 | etcd embed、分布式共识 |
| 任务重试 | 指数退避、死信队列 | 重试策略 |
| 超时控制 | 任务级/工作流级超时 | context、定时器 |

**里程碑：** 多 Coordinator + 多 Worker 集群，支持故障转移

### Phase 3：多语言 Worker SDK（2-3 周）

| 任务 | 产出 | 技术点 |
|------|------|--------|
| Python Worker SDK | pip 包、任务注册装饰器、自动心跳 | gRPC-Python、PyPI 发布 |
| C++ Worker SDK | CMake 库、vcpkg 发布、gRPC C++ | gRPC-C++、CMake、SIMD 优化 |
| 多语言集成测试 | Go/Python/C++ Worker 混合调度验证 | testcontainers 多容器编排 |
| SDK 文档 | 各语言快速上手指南 + API Reference | 技术写作 |

**里程碑：** 三种语言的 Worker 能同时注册到集群，按 handler 标签自动路由任务

### Phase 4：高级特性（3-4 周）

| 任务 | 产出 | 技术点 |
|------|------|--------|
| 事件溯源 | 事件记录、状态回放 | Event Sourcing |
| Saga 补偿 | 补偿事务执行 | 分布式事务 |
| Cron 调度 | 定时触发工作流 | 分布式定时器、时间轮 |
| CDC 引擎 | PG logical replication 监听 → 触发工作流 | WAL 解析、流式复制 |
| Wasm 插件系统 | wazero 运行时、插件上传/管理 | WebAssembly 沙箱 |

**里程碑：** 核心功能完整，支持 CDC 事件驱动 + Wasm 插件执行

### Phase 5：K8s 云原生（2-3 周）

| 任务 | 产出 | 技术点 |
|------|------|--------|
| Docker 多阶段构建 | 各组件镜像（Go < 20MB / Python / C++） | 多架构构建 linux/amd64+arm64 |
| Helm Chart | 一键部署全套组件 | Helm 模板、values 分环境 |
| Forge Operator | ForgeCluster CRD，声明式管理集群 | kubebuilder、Operator SDK |
| HPA 自定义指标 | 基于队列深度/GPU利用率弹性伸缩 | Prometheus Adapter、custom metrics API |
| CI/CD Pipeline | GitHub Actions 构建 + ArgoCD 部署 | GitOps |

**里程碑：** `helm install forge` 一键拉起完整集群，Worker 自动弹性伸缩

### Phase 6：可观测性 + Dashboard + 打磨（2-3 周）

| 任务 | 产出 | 技术点 |
|------|------|--------|
| Prometheus Metrics | 全链路指标 | 可观测性 |
| OpenTelemetry Tracing | 分布式链路追踪（跨语言 Worker 传播） | Tracing、Context Propagation |
| eBPF 内核探针 | TCP 延迟追踪、gRPC 流量解析 | cilium/ebpf、C BPF 程序 |
| Grafana Dashboard | 开箱即用的监控面板 | 可视化 |
| Admin Dashboard | React Web UI（工作流管理、DAG 可视化） | React + Ant Design Pro + gRPC-Web |
| 文档 + 示例 | README、API 文档、使用示例 | 技术写作 |

**里程碑：** 项目完整可展示，含 Web UI、多语言 Worker、K8s 部署、完整可观测性

### 总时间线

```
Phase 1 (地基)           ██████░░░░░░░░░░░░░░░░░░░░░░  2-3 周
Phase 2 (分布式)         ░░░░░░██████░░░░░░░░░░░░░░░░  2-3 周
Phase 3 (多语言SDK)      ░░░░░░░░░░░░██████░░░░░░░░░░  2-3 周
Phase 4 (高级特性)       ░░░░░░░░░░░░░░░░░░████████░░  3-4 周
Phase 5 (K8s云原生)      ░░░░░░░░░░░░░░░░░░░░░░░░████  2-3 周
Phase 6 (可观测+Dashboard) ░░░░░░░░░░░░░░░░░░░░░░░░░░██  2-3 周
                                                         --------
                                                         13-19 周
```

---

## 11. 风险与应对

| 风险 | 概率 | 影响 | 应对策略 |
|------|------|------|----------|
| 嵌入式 etcd 内存占用过大 | 中 | 开发体验差 | 提供 BoltDB + 简单选举的降级方案 |
| Raft 实现复杂度超预期 | 高 | 进度延迟 | Phase 2 先用 etcd embed，Raft 自实现作为可选增强 |
| 事件溯源事件表增长过快 | 中 | PG 性能下降 | 定期快照 + 归档旧事件到冷存储 |
| 前端 Dashboard 开发周期长 | 高 | 分散精力 | Phase 6 可选，优先 CLI 工具，前端用 Ant Design Pro 快速搭 |
| 与 Temporal 对比被质疑"重复造轮子" | 中 | 面试减分 | 准备好回答：1)学习目的 2)更轻量 3)多语言 Worker 4)Wasm 插件 5)eBPF 可观测性是 Temporal 没有的 |
| C++ Worker SDK 编译环境复杂 | 高 | 跨平台构建困难 | 统一用 Docker 多阶段构建；本地开发提供 vcpkg manifest + CMake preset |
| WASI P2 生态尚不成熟 | 中 | wazero 对 Component Model 支持不完整 | 核心功能用 WASI P1 兜底；P2 作为实验性特性标注 |
| 多语言 SDK 维护成本高 | 高 | 接口变更需同步多个仓库 | buf 统一管理 Proto 定义，CI 自动生成各语言代码；SDK 只包装 gRPC client |
| K8s Operator 开发门槛高 | 中 | Phase 5 延期 | 先用 Helm Chart 覆盖 80% 场景；Operator 作为进阶目标，用 kubebuilder 降低门槛 |
| Kueue 版本迭代快，API 不稳定 | 中 | 升级时 breaking change | 抽象一层 BatchScheduler 接口，Kueue 只是一种实现；同时支持原生 Job |
| eBPF 需要 Linux 内核 5.8+ | 低 | 部分环境不支持 | eBPF 模块做成可选插件，不影响核心功能；开发环境用 Docker（内核由 Docker Desktop 提供） |
| NATS JetStream 社区较小 | 低 | 遇到问题排查困难 | NATS 作为可选后端，默认仍用 PG + Redis；提供切换开关 |
| OTel Profiling SDK 尚在实验阶段 | 中 | API 可能变化 | 通过适配层隔离 OTel API；Profiling 模块独立，可随时替换底层实现 |
| Go 1.23 新特性面试官不熟悉 | 低 | 面试时需额外解释 | 准备好 30 秒的解释："iter.Seq 是 Go 1.23 的自定义迭代器协议，类似 Python 的 __iter__" |

---

## 附录 A：项目目录结构

```
forge/
├── cmd/
│   └── forge/
│       └── main.go                  # 入口（coordinator / worker / standalone）
├── api/
│   └── proto/
│       ├── coordinator.proto         # Coordinator gRPC 接口
│       ├── worker.proto              # Worker gRPC 接口
│       ├── common.proto              # 公共消息定义
│       ├── cdc.proto                 # CDC 触发器接口
│       └── buf.gen.yaml              # buf 多语言代码生成配置
├── internal/
│   ├── coordinator/
│   │   ├── coordinator.go            # Coordinator 主逻辑
│   │   ├── scheduler.go              # 调度器
│   │   ├── dag.go                    # DAG 引擎
│   │   └── cron.go                   # Cron 调度
│   ├── worker/
│   │   ├── worker.go                 # Go Worker 主逻辑
│   │   ├── executor.go               # 任务执行器
│   │   └── handler.go                # Handler 注册
│   ├── storage/
│   │   ├── interface.go              # Storage 接口
│   │   ├── postgres.go               # PG 实现
│   │   └── boltdb.go                 # BoltDB 实现
│   ├── event/
│   │   ├── store.go                  # 事件存储
│   │   └── replay.go                 # 事件回放
│   ├── saga/
│   │   └── compensator.go            # Saga 补偿
│   ├── cdc/
│   │   ├── interface.go              # CDC Source 接口
│   │   ├── postgres.go               # PG Logical Replication
│   │   ├── mysql.go                  # MySQL Binlog
│   │   └── redis.go                  # Redis Keyspace Notification
│   ├── wasm/
│   │   ├── executor.go               # Wasm 沙箱执行器 (wazero)
│   │   ├── registry.go               # 插件注册/版本管理
│   │   └── sandbox.go                # 资源限制/权限控制
│   ├── discovery/
│   │   ├── interface.go              # 服务发现接口
│   │   ├── etcd.go                   # etcd 实现
│   │   └── raft.go                   # Raft 实现（可选）
│   └── observability/
│       ├── metrics.go                # Prometheus 指标
│       ├── tracing.go                # OpenTelemetry 链路追踪
│       └── ebpf.go                   # eBPF 探针加载/管理
├── sdk/
│   ├── go/                           # Go Worker SDK（pkg/sdk 的对外封装）
│   │   ├── go.mod
│   │   ├── worker.go
│   │   └── README.md
│   ├── python/                       # Python Worker SDK
│   │   ├── forge_sdk/
│   │   │   ├── __init__.py
│   │   │   ├── worker.py             # Worker 客户端
│   │   │   ├── decorators.py         # @task_handler 装饰器
│   │   │   └── generated/            # buf 生成的 gRPC Python 代码
│   │   ├── pyproject.toml            # PyPI 发布配置
│   │   ├── Dockerfile
│   │   └── README.md
│   └── cpp/                          # C++ Worker SDK
│       ├── include/
│       │   └── forge/
│       │       ├── worker.h
│       │       ├── task_handler.h
│       │       └── task_context.h
│       ├── src/
│       │   ├── worker.cpp
│       │   └── grpc_client.cpp
│       ├── CMakeLists.txt
│       ├── vcpkg.json                # vcpkg 依赖清单
│       ├── Dockerfile
│       └── README.md
├── bpf/                              # eBPF 程序（C 源码）
│   ├── tcp_latency.c                 # TCP 延迟追踪
│   ├── grpc_parse.c                  # gRPC 流量解析
│   ├── headers/                      # vmlinux.h 等内核头文件
│   └── Makefile                      # clang 编译 BPF 字节码
├── plugins/                          # Wasm 插件示例
│   ├── transform/
│   │   ├── main.go                   # Go → Wasm 编译
│   │   └── Makefile
│   └── validate/
│       ├── main.rs                   # 也可用其他语言编写 Wasm
│       └── Cargo.toml
├── web/                              # Admin Dashboard 前端
│   ├── package.json
│   ├── src/
│   │   ├── pages/
│   │   │   ├── workflows/            # 工作流管理 + DAG 可视化
│   │   │   ├── workers/              # Worker 拓扑
│   │   │   ├── cdc/                  # CDC 数据源监控
│   │   │   └── plugins/              # Wasm 插件管理
│   │   └── components/
│   │       └── dag-visualizer/       # D3.js DAG 渲染组件
│   └── tsconfig.json
├── deploy/
│   ├── docker-compose.yml
│   ├── Dockerfile                    # Go 组件多阶段构建
│   ├── prometheus.yml
│   ├── grafana/
│   │   └── dashboards/               # 预配置 Grafana 面板
│   └── helm/
│       └── forge/
│           ├── Chart.yaml
│           ├── values.yaml
│           ├── values-dev.yaml
│           ├── values-prod.yaml
│           ├── templates/
│           │   ├── coordinator-statefulset.yaml
│           │   ├── worker-go-deployment.yaml
│           │   ├── worker-python-deployment.yaml
│           │   ├── worker-cpp-deployment.yaml
│           │   ├── hpa.yaml
│           │   ├── servicemonitor.yaml
│           │   └── ingress.yaml
│           └── crds/
│               └── forgecluster-crd.yaml
├── operator/                         # Forge K8s Operator
│   ├── api/v1alpha1/
│   │   └── forgecluster_types.go     # CRD 类型定义
│   ├── controllers/
│   │   └── forgecluster_controller.go
│   ├── Dockerfile
│   └── Makefile
├── build/                            # 各组件 Dockerfile
│   ├── coordinator/Dockerfile
│   ├── worker-go/Dockerfile
│   ├── worker-python/Dockerfile
│   └── worker-cpp/Dockerfile
├── .github/
│   └── workflows/
│       ├── ci.yml                    # PR 检查：lint + test + build
│       └── release.yml               # Tag 触发：多架构构建 + ArgoCD
├── docs/
│   ├── architecture.md
│   ├── getting-started.md
│   ├── sdk-python.md
│   ├── sdk-cpp.md
│   └── api-reference.md
├── examples/
│   ├── simple-workflow/              # 入门示例
│   ├── video-production/             # 多语言 Worker 示例
│   ├── cdc-trigger/                  # CDC 触发示例
│   └── wasm-plugin/                  # Wasm 插件示例
├── buf.yaml                          # buf 配置
├── buf.gen.yaml                      # buf 多语言生成
├── Makefile
├── go.mod
└── README.md
```

## 附录 B：参考项目

| 项目 | 值得学习的点 |
|------|-------------|
| [Temporal](https://github.com/temporalio/temporal) | 工作流状态机设计、事件溯源 |
| [Asynq](https://github.com/hibiken/asynq) | Go 任务队列的简洁 API 设计 |
| [Machinery](https://github.com/RichardKnop/machinery) | Go 分布式任务队列 |
| [etcd](https://github.com/etcd-io/etcd) | Raft 实现、嵌入式使用 |
| [HashiCorp Nomad](https://github.com/hashicorp/nomad) | 调度算法、bin packing |
| [Cadence](https://github.com/uber/cadence) | 工作流引擎架构 |

---

> **下一步：** 确认技术选型无异议后，开始 Phase 1 实施。
