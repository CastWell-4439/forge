# AGENTS.md — Forge 项目 Agent 指南

## 这是什么

Forge：分布式 DAG 调度引擎 + AI Agent 引擎。Go 实现。详见 `ARCHITECTURE.md`。

## 必读文档

| 文档 | 内容 | 何时读 |
|------|------|--------|
| `ARCHITECTURE.md` | 鸟瞰、codemap、不变量 | **首先读** |
| `docs/tech-spec.md` | 完整技术方案（架构、接口、算法） | 写代码前 |
| `docs/implementation-plan.md` | 分阶段实施计划（任务清单、评估标准） | 确认当前 Phase |
| `docs/tech-plan-phase-ae2-4.md` | AE-2~4 Agent 增强方案 + 设计决策 D1~D5 | Agent 层开发 |
| `docs/coding-conventions.md` | 编码约定（Parse Don't Validate 等） | 写代码前 |

## 硬性约束

1. **一次一个 Phase**。Phase N 未 review 通过前不开 Phase N+1。
2. **忠实实现**。任务引用 tech-spec 某节，就按那节写。同名、同签名、同算法。
3. **不加戏**。只做当前 Phase 指定的内容。
4. **构建必须通过**。每个任务后 `go build ./...`，每个 Phase 后 `go test ./...`。
5. **依赖方向**：`core ← structured ← planning ← session ← harness ← workers`（左不 import 右）。
6. **core 零依赖**：`agent/core/` 禁止 import `internal/` 子包。
7. **Parse Don't Validate**：系统边界处 Parse 为强类型 struct，内部函数不接受 `interface{}`。
8. **文件大小**：单文件 >500 行需拆分。
9. **提交格式**：`feat(module): description` 或 `test(module): description`。
10. **技术选型已定**：Go 1.26 / gRPC+Protobuf / PG+BoltDB / etcd / NATS / wazero / cilium/ebpf。不改。

## 当前状态

- **Layer 0**：✅ 完成（Phase 1A~6C + AE-1，27 commits）
- **AE-1G**：⬜ 架构治理前置
- **AE-2~4**：⬜ Agent 引擎增强
- **Layer 2~3**：⬜ 自动化框架 + 应用层
