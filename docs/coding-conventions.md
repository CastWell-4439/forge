# Forge 编码约定

> 最后更新：2026-05-13
> 适用范围：所有 Forge 代码（Go）

---

## 1. Parse, Don't Validate

**核心原则**：在系统边界处将松散数据解析为强类型，内部函数只接受已解析类型。

### 什么是边界

- 外部输入：gRPC 请求、HTTP 请求、JSON-RPC 消息、配置文件
- LLM 输出：Agent 循环中 LLM 返回的 JSON
- 工具输入：用户传给工具的参数 map
- 文件/网络：读文件内容、HTTP 响应

### 怎么做

```go
// ❌ Bad: validate 后还是 raw type，后续每处都要 type assert
func handleRequest(raw []byte) error {
    var m map[string]interface{}
    json.Unmarshal(raw, &m)
    if m["method"] == nil { return errors.New("missing method") }
    method := m["method"].(string) // 每处都不安全
    ...
}

// ✅ Good: Parse 出强类型，编译器保证后续正确性
type MCPRequest struct {
    JSONRPC string `json:"jsonrpc"`
    ID      int64  `json:"id"`
    Method  string `json:"method"`
}

func ParseMCPRequest(raw []byte) (MCPRequest, error) {
    var req MCPRequest
    if err := json.Unmarshal(raw, &req); err != nil {
        return MCPRequest{}, fmt.Errorf("invalid JSON-RPC: %w", err)
    }
    if req.Method == "" {
        return MCPRequest{}, errors.New("method is required")
    }
    return req, nil // 返回值本身就是"已验证"的证明
}
```

### 命名约定

- Parse 函数：`Parse<Type>(raw) (<Type>, error)` — 如 `ParseMCPRequest`, `ParseAgentAction`
- 已验证类型不加前缀 — 类型名本身就是保证（`MCPRequest` 不叫 `ValidatedMCPRequest`）
- 未验证的原始数据用明确标记 — `rawJSON []byte`, `rawParams map[string]interface{}`

---

## 2. 依赖方向

`internal/agent/` 内部的包有严格依赖方向：

```
core (0) ← structured (1) ← planning (2) ← session (3) ← harness (4) ← workers (5)
                                                           ↑
                                                    mcp/rag/memory/
                                                    guardrails/checkpoint (4)
```

- 数字小的包**不能** import 数字大的包
- `core` 不能 import 任何 `internal/` 子包
- 如果需要跨层调用，通过 `core/interfaces.go` 中的接口解耦
- `scripts/lint_structure.go` 会在 CI 中强制检查

---

## 3. 错误处理

```go
// ✅ 用 fmt.Errorf + %w 包装，保留错误链
return fmt.Errorf("parse MCP request: %w", err)

// ✅ 定义包级别的 sentinel error
var ErrToolNotFound = errors.New("tool not found")

// ❌ 不要用 panic（除非是真正不可恢复的程序错误）
// ❌ 不要吞掉错误（_ = someFunc()）
```

---

## 4. 文件组织

- **单文件 ≤ 300 行**（建议），**≤ 500 行**（硬限）。超过就拆分。
- `workers/` 下的工具文件以 `_handler.go` 结尾。
- 测试文件放同目录，`_test.go` 后缀。
- 一个文件一个主要类型/职责。

---

## 5. 命名

- **包名**：小写单词，不用下划线。`mcp` 不叫 `mcp_protocol`。
- **接口**：动词或 `-er` 后缀。`Retriever`, `Verifier`, `Guard`。
- **实现**：名词。`HybridRetriever`, `LLMVerifier`, `MCPClient`。
- **构造函数**：`New<Type>(deps) *Type`。
- **工具名**：`<category>.<action>`。`file.read`, `web.search`, `code.execute`。

---

## 6. 测试

- 每个公共函数至少一个测试用例。
- 测试用 table-driven 风格。
- mock 对象放同包或 `_test.go` 里。
- 集成测试放 `test/` 目录。

---

## 7. 提交

格式：`<type>(<scope>): <description>`

- `feat(mcp): implement JSON-RPC codec`
- `fix(harness): handle empty tool result`
- `test(planning): add DAG cycle detection test`
- `docs(arch): add ARCHITECTURE.md`
- `refactor(workers): extract file handler`
