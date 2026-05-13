# AE-2~4 技术方案：Agent 增强 + 通用工具扩展

> 作者：wika
> 日期：2026-04-23
> 状态：待 CastWell 确认
> 前置：AE-1 ✅（M8 Structured Output + M2 ReAct Harness）
> 引用文档：`桌面\agent\Agent增强方案-精简版(7模块).md`、`docs/implementation-plan.md`

---

## 一、总览

### 目标

把 Forge Agent 从"能跑 ReAct 循环的骨架"变成"有工具、有知识、有记忆、能纠错、能安全运行"的完整 Agent 系统。

### 变更范围

| Phase | 模块 | 新增内容 | 预估代码量 |
|-------|------|---------|-----------|
| **AE-1G** | 架构治理前置 | ARCHITECTURE.md + AGENTS.md + lint + 编码约定 | ~300 行（文档+脚本） |
| **前置修复** | 现有 Agent 层改进 | Bug 修复 + 架构优化（穿插在 AE-2~4 中） | ~300 行 |
| **AE-2** | M1: MCP 工具协议 | JSON-RPC + stdio + Client + Manager + Bridge（D3 强类型） | ~400 行 |
| **AE-2+** | 通用工具扩展 + Agent.Run + Verifier | 9 个新工具 + Run() 入口 + D5 自验证循环 | ~800 行 |
| **AE-3** | M3: RAG + M5: Memory | 混合检索 + 短期/长期记忆 | ~500 行 |
| **AE-4** | M6: Guardrails + M12: Checkpointing | 安全护栏 + 状态持久化 + Reflexion LLMVerifier | ~400 行 |
| | **合计** | | **~2700 行** |

### 实施顺序（改进项穿插）

```
AE-1G（半天）：ARCHITECTURE.md + AGENTS.md 精简 + 结构 lint + 编码约定
  ↓
AE-2 前置（半天）：修 Bug #2 #4 + 合并 Registry #5
  ↓
AE-2（2-3 天）：MCP 协议(D3) + 通用工具 + Agent.Run(D5 Verify) + 穿插 #8 #6 #9
  ↓
AE-3（1.5-2 天）：RAG + Memory + 穿插 #1 LLM 重试+Usage / #3 Context 二次检查
  ↓
AE-4（1.5-2 天）：Guardrails + Checkpointing + Reflexion(D5 LLMVerifier) + 穿插 #7 #10
```

---

## 一-A、架构设计决策（2026-05-13 新增）

> 来源：OpenAI Harness Engineering、"AI Is Forcing Us To Write Good Code"、"Parse Don't Validate"、ARCHITECTURE.md、Ralph Loop
> 目的：让代码仓库成为 Agent 可读的"真相之源"，在 AE-2 之前建立架构纪律

### 设计决策 D1：ARCHITECTURE.md — 仓库即地图

**原则**：新贡献者（人类或 AI Agent）不应阅读全部源码才能理解项目结构。

**执行**：
- 在项目根目录创建 `ARCHITECTURE.md`（≤200 行）
- 内容：鸟瞰总览 → codemap（每个 `internal/` 子目录一行说明）→ 架构不变量 → 层间边界
- 只描述不太会频繁变化的高层结构，不深入模块内部实现
- 命名重要类型/接口，但**不直接链接**（用符号搜索替代，免维护）
- 每季度 review 一次

**参考**：[rust-analyzer/architecture.md](https://github.com/rust-analyzer/rust-analyzer/blob/d7c99931d05e3723d878bea5dc26766791fa4e69/docs/dev/architecture.md)

---

### 设计决策 D2：AGENTS.md 精简为目录

**原则**：Agent 上下文是稀缺资源。一个巨大的指令文件会挤掉任务上下文，导致 Agent 对"局部模式匹配"而非"全局导航"。

**执行**：
- AGENTS.md 控制在 ≤100 行
- 只放：项目一句话描述 + 指向 ARCHITECTURE.md / tech-spec.md / implementation-plan.md 的指针 + 5-10 条硬性编码约束
- 不放：具体实现细节、改进清单、API 用法（这些在各自文档/代码注释中）

---

### 设计决策 D3：Parse, Don't Validate

**原则**：在系统边界处将松散数据解析为强类型（`ParsedX`），内部函数只接受已解析类型，不再做运行时校验。

**在 Forge 中的具体应用**：
1. **MCP 协议层**：所有 JSON-RPC 消息在 Transport 层 `Parse` 为 `MCPRequest`/`MCPResponse` 强类型 struct，内部函数不接受 `json.RawMessage` 或 `map[string]interface{}`
2. **工具输入**：`ToolRouter.Call()` 在调用 handler 前，将 `map[string]interface{}` 解析为 handler 期望的强类型参数 struct
3. **Agent 输出**：LLM 返回的 JSON 用 `structured.Parse()` 解析为 `AgentAction`/`AgentFinalAnswer`，后续代码无需再判断 "action" vs "final_answer"
4. **配置加载**：`config.toml` → `ParseConfig()` → `ValidatedConfig` struct（不用 `map[string]interface{}`）

**Go 惯用实现**：
```go
// Bad: validate 后还是 raw type
func handleMCP(raw []byte) error {
    var m map[string]interface{}
    json.Unmarshal(raw, &m)
    if m["method"] == nil { return errors.New("missing method") }
    method := m["method"].(string) // 后续每处都要 type assert
    ...
}

// Good: parse 出强类型，编译器保证后续正确性
type MCPRequest struct {
    JSONRPC string          `json:"jsonrpc"`
    ID      int64           `json:"id"`
    Method  string          `json:"method"`
    Params  json.RawMessage `json:"params,omitempty"`
}

func ParseMCPRequest(raw []byte) (MCPRequest, error) {
    var req MCPRequest
    if err := json.Unmarshal(raw, &req); err != nil {
        return MCPRequest{}, fmt.Errorf("invalid JSON-RPC: %w", err)
    }
    if req.JSONRPC != "2.0" {
        return MCPRequest{}, errors.New("unsupported jsonrpc version")
    }
    if req.Method == "" {
        return MCPRequest{}, errors.New("method is required")
    }
    return req, nil // 返回值本身就是"已验证"的证明
}
```

---

### 设计决策 D4：结构 Lint — 机械化强制架构不变量

**原则**：架构规则如果只写在文档里，随时会被违反。必须用 CI 强制执行。

**Forge 的架构规则**：
1. **依赖方向**（`internal/agent/` 内部）：
   ```
   core ← structured ← planning ← session ← harness ← workers
         （左边不能 import 右边）
   ```
2. **core 不依赖**：`core/` 包禁止 import 任何 `internal/` 子包（纯接口+类型）
3. **文件大小**：单文件 >500 行产生 lint error，>300 行产生 warning
4. **命名约定**：工具 handler 文件必须以 `_handler.go` 结尾

**实现**：一个 Go 脚本 `scripts/lint_structure.go`（~100 行），在 CI 和 pre-commit 中运行。

---

### 设计决策 D5：Agent 自验证循环（Ralph Loop + Reflexion）

**原则**：Agent 执行完动作后，应自动验证结果是否符合预期，而非盲目交给用户。循环是：`Think → Act → Observe → Verify → (loop or done)`。

**在 Forge 中的具体应用**：

```go
// core/interfaces.go 新增
type Verifier interface {
    // Verify 检查工具执行结果是否符合预期
    // 返回 ok=true 表示通过，否则返回 feedback 作为下一轮的额外上下文
    Verify(ctx context.Context, action AgentAction, result ToolResult) (ok bool, feedback string, err error)
}
```

ReAct Loop 扩展（`harness/loop.go`）：
```
for step := 0; step < maxSteps; step++ {
    // Think: LLM 选择动作
    action := llm.Chat(ctx, messages)
    // Act: 执行工具
    result := toolRouter.Call(ctx, action)
    // Observe: 记录结果到消息历史
    messages = append(messages, observationMsg(result))
    // Verify (可选): 检查结果质量
    if a.verifier != nil {
        ok, feedback, _ := a.verifier.Verify(ctx, action, result)
        if !ok {
            messages = append(messages, feedbackMsg(feedback))
            continue // 重试
        }
    }
    // 如果 action 是 final_answer，结束循环
}
```

**验证器类型**（按场景注入）：
- `CodeVerifier`：执行代码后检查 exit_code == 0
- `FileVerifier`：写文件后检查文件存在且非空
- `SchemaVerifier`：输出 JSON 是否符合 schema
- `LLMVerifier`：用一次廉价 LLM 调用判断结果是否合理（Reflexion 思路）

**穿插时间**：AE-2 Day2（Agent.Run）实现 Verifier 接口槽位；AE-4 实现具体的 Reflexion LLMVerifier。

---

### D1~D5 执行时间表

| 决策 | 执行阶段 | 产出 |
|------|---------|------|
| D1 ARCHITECTURE.md | **AE-1G（前置）** | `ARCHITECTURE.md` |
| D2 AGENTS.md 精简 | **AE-1G（前置）** | 更新 `AGENTS.md` |
| D3 Parse Don't Validate | **AE-2 全程** | MCP 层、工具层强类型设计 |
| D4 结构 lint | **AE-1G（前置）** | `scripts/lint_structure.go` |
| D5 自验证循环 | **AE-2 Day2** 接口 + **AE-4** 实现 | `core/interfaces.go` + `harness/loop.go` |

---

## 一-B、现有 Agent 层 Review 改进清单

> 以下 10 个改进项来自对现有 Agent 层（Phase A1/A2/AE-1）的全面 Review。
> 按优先级分级，穿插在 AE-2~4 各阶段中执行，不单独开 Phase。

### 🔴 应该修的（Bug / 健壮性）

#### #1 LLMClient 没有重试和 token 用量追踪 → 穿插在 AE-3

**文件：** `harness/llm.go`

**问题：** 调 LLM API 一次失败就报错，没有重试。`chatResponse.Usage` 读了但没用。

**改进：**
- 加指数退避重试（429/5xx，最多 3 次）
- `Chat()` 返回值增加 `*TokenUsage`（prompt_tokens + completion_tokens）
- 通过回调或返回值上报 usage，给 Budget 模块消费

**为什么在 AE-3：** RAG 的 embedding 调用也走 LLM API 需要重试；Memory 的 saveMemory 也调 LLM 需要 token 计数。此时改最合适。

**改动：**
```go
// core/types.go 新增
type TokenUsage struct {
    PromptTokens     int `json:"prompt_tokens"`
    CompletionTokens int `json:"completion_tokens"`
    TotalTokens      int `json:"total_tokens"`
}

// core/types.go LLMClient 接口扩展
type LLMClient interface {
    Chat(ctx context.Context, messages []Message) (string, error)
    // ChatWithUsage 返回 token 用量（新增，Budget 模块使用）
    ChatWithUsage(ctx context.Context, messages []Message) (string, *TokenUsage, error)
}

// harness/llm.go 加重试逻辑
func (c *LLMClient) Chat(ctx context.Context, messages []core.Message) (string, error) {
    resp, _, err := c.ChatWithUsage(ctx, messages)
    return resp, err
}

func (c *LLMClient) ChatWithUsage(ctx context.Context, messages []core.Message) (string, *core.TokenUsage, error) {
    var lastErr error
    for attempt := 0; attempt <= 3; attempt++ {
        if attempt > 0 {
            backoff := time.Duration(1<<uint(attempt-1)) * time.Second
            select {
            case <-ctx.Done(): return "", nil, ctx.Err()
            case <-time.After(backoff):
            }
        }
        resp, usage, err := c.doChat(ctx, messages)
        if err == nil {
            return resp, usage, nil
        }
        if !isRetryable(err) {
            return "", nil, err
        }
        lastErr = err
    }
    return "", nil, lastErr
}
```

#### #2 parser.go extractJSON 没处理字符串内花括号 → AE-2 前置修复

**文件：** `planning/parser.go`

**问题：** `extractJSON()` 用简单的 `{}` 深度计数，不处理字符串内的花括号。如果 LLM 返回 `"description": "use { and }"`，深度计数会错。而 `structured/validator.go` 里的 `extractJSONObject()` 已经正确处理了字符串转义。

**改进：** 删掉 parser.go 里的 extractJSON，改为调用 `structured.ExtractJSONObject()`（需要导出该函数）。

#### #3 ContextManager 压缩后可能仍然超限 → 穿插在 AE-3

**文件：** `harness/context.go`

**问题：** CompactIfNeeded 压缩一次后不再检查。如果最近 4 条消息本身很长（比如 RAG 返回了大量文档内容），压缩后还是超限。

**改进：**
- 压缩后二次检查 token 数
- 如果仍超限，对 tool result 消息做截断（保留前 N 字符 + `"[truncated, full output: X chars]"`）
- 极端情况下减少保留消息数（从 4 条降到 2 条）

**为什么在 AE-3：** RAG 返回的检索结果可能很长，这时最容易触发超限。

#### #4 ToolRouter.Call 的 float64→int 类型问题 → AE-2 前置修复

**文件：** `harness/tool_router.go`

**问题：** JSON 反序列化时所有数字变 float64，但 handler 里做 `params["face_index"].(int)` 会 panic。

**改进：** 在 ToolRouter.Call() 里加类型适配层：
```go
func adaptParams(params map[string]interface{}, schema map[string]workers.ParamDef) map[string]interface{} {
    for key, val := range params {
        if def, ok := schema[key]; ok {
            switch def.Type {
            case "integer":
                if f, ok := val.(float64); ok {
                    params[key] = int64(f)
                }
            }
        }
    }
    return params
}
```

### 🟡 值得改进的（架构优化）

#### #5 两套 ToolRegistry 合并 → AE-2 前置修复

**问题：** `workers.ToolRegistry` 和 `tools.ToolRegistry` 名字一样、功能重叠。

**改进：** 把 `tools.ToolRegistry` 的 `FormatForPrompt()` / `FindSimilar()` / `DefaultRegistry()` 功能合并到 `workers.ToolRegistry`。删掉 `tools/` 包，`planning/` 直接依赖 `workers.ToolRegistry`。

**影响文件：** `tools/registry.go`（删除）、`workers/registry.go`（合入方法）、`planning/*.go`（改 import）、`harness/tool_router.go`（改 import）

#### #6 DAG 模板 Sprintf 拼 YAML → 穿插在 AE-2 Day3

**文件：** `planning/planner.go`

**问题：** `FaceSwapWithTTSTemplate` 用 `fmt.Sprintf` 拼 YAML。参数含引号/特殊字符会破坏 YAML。

**改进：** 改用 Go struct + `yaml.Marshal` 生成：
```go
type dagTemplate struct {
    Name  string                    `yaml:"name"`
    Tasks map[string]taskTemplate   `yaml:"tasks"`
}
type taskTemplate struct {
    Handler   string                 `yaml:"handler"`
    Params    map[string]interface{} `yaml:"params"`
    DependsOn []string               `yaml:"depends_on,omitempty"`
    Timeout   string                 `yaml:"timeout,omitempty"`
    Retry     *retryConfig           `yaml:"retry,omitempty"`
}
// Build() 填 struct → yaml.Marshal → string
```

#### #7 Session 状态机加 OnTransition 回调 → 穿插在 AE-4

**文件：** `session/session.go`

**问题：** 状态变化时没有通知机制。Checkpointing 需要在状态变化时自动保存。

**改进：**
```go
type Session struct {
    // ...
    onTransition []func(from, to SessionState) // 回调列表
}

func (s *Session) OnTransition(fn func(from, to SessionState)) {
    s.onTransition = append(s.onTransition, fn)
}

func (s *Session) Transition(target SessionState) error {
    // ... 原有逻辑 ...
    old := s.State
    s.State = target
    for _, fn := range s.onTransition {
        fn(old, target)
    }
    return nil
}
```

#### #8 Agent 顶层加 Run() 入口 → 穿插在 AE-2 Day2

**文件：** `agent.go`

**问题：** Agent struct 有所有模块但没有统一入口，调用方要自己串联 Parse→Plan→Execute。

**改进：**
```go
func (a *Agent) Run(ctx context.Context, session *session.Session, userInput string) (*RunResult, error) {
    // 1. Parse: 自然语言 → VideoRequirement
    session.Transition(session.StateParsing)
    req, err := a.parser.Parse(ctx, userInput)
    // 2. Plan: Requirement → DAG
    session.Transition(session.StatePlanning)
    dagResult, err := a.dagGen.Generate(ctx, req)
    // 3. Execute: ReAct loop 或提交 DAG
    session.Transition(session.StateExecuting)
    // ... 
    // 4. Check & potentially retry
    // 5. Return result
}
```

### 💡 可选优化

#### #9 FindSimilar 加编辑距离 → 穿插在 AE-2 Day3

27 个工具后 LLM 拼错概率更高，加 Levenshtein 距离（~20 行）提升纠错准确度。

#### #10 planning 包补集成测试 → 穿插在 AE-4 Day7

`dag_gen.go` 的 Generate() 三策略流转没有测试，补 3 个测试用例。

### 改进项穿插时间表

| 改进项 | 穿插阶段 | 理由 |
|--------|---------|------|
| #2 extractJSON bug | **AE-2 前置** | 基础 bug，新工具会产生更多 JSON |
| #4 float64→int | **AE-2 前置** | 新工具传数字参数，不修会 panic |
| #5 Registry 合并 | **AE-2 前置** | AE-2 要大量注册工具，合并后更干净 |
| #8 Agent.Run() | **AE-2 Day2** | 工具齐了，串联是自然下一步 |
| #6 YAML 模板 | **AE-2 Day3** | 模板要适配新工具，Sprintf 不安全 |
| #9 编辑距离 | **AE-2 Day3** | 27 个工具容易拼错 |
| #1 LLM 重试+Usage | **AE-3 Day4** | RAG embedding 需要重试，Budget 需要 Usage |
| #3 Context 二次检查 | **AE-3 Day5** | RAG 返回长文档容易超限 |
| #7 Session 回调 | **AE-4 Day6** | Checkpoint 需要状态变化通知 |
| #10 planning 测试 | **AE-4 Day7** | 收尾阶段补测试 |

---

## 二、Phase AE-2：MCP 工具协议 + 通用工具扩展

### 2.1 MCP 协议实现（M1）

#### 2.1.1 架构

```
Agent Harness (loop.go)
    │
    ├── ToolRouter
    │     ├── ToolRegistry（原生 18 handler）
    │     └── MCPBridge（MCP 发现的工具，自动注册到 ToolRegistry）
    │              │
    │              ├── MCPManager（管理多个 MCP Server 生命周期）
    │              │     ├── MCPClient #1 ←stdio→ [FFmpeg MCP Server]
    │              │     ├── MCPClient #2 ←stdio→ [DB Query MCP Server]
    │              │     └── ...
    │              │
    │              └── MCPClient（JSON-RPC 2.0 over stdio）
    │                    ├── Initialize()  — 握手
    │                    ├── ListTools()   — 发现工具
    │                    └── CallTool()    — 调用工具
```

#### 2.1.2 JSON-RPC 编解码

文件：`internal/agent/mcp/jsonrpc.go`

```go
// JSON-RPC 2.0 消息结构
type Request struct {
    JSONRPC string          `json:"jsonrpc"` // 固定 "2.0"
    ID      int64           `json:"id"`
    Method  string          `json:"method"`
    Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
    JSONRPC string          `json:"jsonrpc"`
    ID      int64           `json:"id"`
    Result  json.RawMessage `json:"result,omitempty"`
    Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
    Code    int    `json:"code"`
    Message string `json:"message"`
}
```

- 编码：`json.Marshal` + `\n` 分隔
- 解码：`bufio.Scanner` 按行读取 + `json.Unmarshal`
- ID 递增分配，用于请求-响应匹配

#### 2.1.3 StdioTransport

文件：`internal/agent/mcp/transport_stdio.go`

```go
type StdioTransport struct {
    cmd     *exec.Cmd
    stdin   io.WriteCloser
    stdout  *bufio.Scanner
    stderr  *bytes.Buffer  // 捕获错误日志
    mu      sync.Mutex     // 写锁（发请求串行化）
}

func NewStdioTransport(command string, args []string, env []string) *StdioTransport
func (t *StdioTransport) Start(ctx context.Context) error
func (t *StdioTransport) Send(req *Request) (*Response, error)  // 发请求，等响应
func (t *StdioTransport) Close() error  // kill 进程 + 清理
```

关键设计：
- `Start()` 启动子进程，管道连 stdin/stdout
- `Send()` 写 JSON → 读一行 → 解析 Response → 匹配 ID
- 超时控制：`context.WithTimeout`（默认 30s）
- 进程异常退出时 `Send()` 返回明确错误

#### 2.1.4 MCPClient

文件：`internal/agent/mcp/client.go`

```go
type MCPClient struct {
    transport *StdioTransport
    nextID    atomic.Int64
    serverInfo *ServerInfo  // 握手后获得
}

// 三个核心方法
func (c *MCPClient) Initialize(ctx context.Context) error
func (c *MCPClient) ListTools(ctx context.Context) ([]MCPToolInfo, error)
func (c *MCPClient) CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error)
```

Initialize 握手流程：
1. 发送 `initialize` 请求（带 client capabilities）
2. 收到 server capabilities（工具列表、协议版本）
3. 发送 `notifications/initialized` 通知

MCPToolInfo:
```go
type MCPToolInfo struct {
    Name        string          `json:"name"`
    Description string          `json:"description"`
    InputSchema json.RawMessage `json:"inputSchema"` // JSON Schema
}
```

#### 2.1.5 MCPManager

文件：`internal/agent/mcp/manager.go`

```go
type MCPServerConfig struct {
    Name    string   `toml:"name"`     // "ffmpeg-tools"
    Command string   `toml:"command"`  // "python"
    Args    []string `toml:"args"`     // ["-m", "ffmpeg_mcp_server"]
    Env     []string `toml:"env"`      // 环境变量
}

type MCPManager struct {
    clients map[string]*MCPClient  // name → client
    configs []MCPServerConfig
    mu      sync.RWMutex
}

func NewMCPManager(configs []MCPServerConfig) *MCPManager
func (m *MCPManager) Start(ctx context.Context) error   // 启动所有 server
func (m *MCPManager) Stop() error                       // 停止所有 server
func (m *MCPManager) ListTools(ctx context.Context) ([]MCPToolDef, error)  // 聚合所有 server 的工具
func (m *MCPManager) CallTool(ctx context.Context, name string, params json.RawMessage) (*ToolResult, error)
```

- 实现 `core.MCPManager` 接口
- 工具名前缀避免冲突：`{server_name}.{tool_name}`（如 `ffmpeg.transcode`）
- 单个 server 挂了不影响其他 server

#### 2.1.6 MCPBridge

文件：`internal/agent/mcp/bridge.go`

```go
type MCPBridge struct {
    manager  *MCPManager
    registry *workers.ToolRegistry
}

// SyncTools 从 MCP Server 发现的工具自动注册到 ToolRegistry
func (b *MCPBridge) SyncTools(ctx context.Context) error {
    mcpTools, err := b.manager.ListTools(ctx)
    // 每个 MCP 工具 → 生成 ToolDef + 包装成 HandlerFunc → 注册
}
```

桥接逻辑：
- MCP `inputSchema`（JSON Schema）→ 转换为 `map[string]ParamDef`
- 调用时：`HandlerFunc` 内部调 `manager.CallTool()`
- Agent Harness 完全无感，照常通过 ToolRouter 调用

#### 2.1.7 测试

文件：`test/agent_mcp_test.go`

编写一个 Go 的 mock MCP Server（stdin/stdout JSON-RPC），测试：
- 握手成功
- 工具发现
- 工具调用 + 正确响应
- Bridge 注册到 ToolRegistry
- Server 进程异常退出 → 错误处理

---

### 2.2 通用工具扩展（6 个新工具）

新增工具全部放在 `internal/agent/workers/` 下新文件，遵循现有 ToolDef + HandlerFunc 模式。

#### 2.2.1 file.read / file.write / file.list — 文件操作

文件：`internal/agent/workers/file_handler.go`

| 工具名 | 说明 | 输入 | 输出 |
|--------|------|------|------|
| `file.read` | 读取文件内容（文本/二进制 base64） | `path`, `encoding?`（text/base64） | `content`, `size` |
| `file.write` | 写入文件 | `path`, `content`, `encoding?` | `path`, `size` |
| `file.list` | 列出目录内容 | `dir`, `pattern?`（glob） | `entries[]`（name, size, is_dir） |

安全约束：
- **沙箱限制**：只允许在 `HandlerConfig.Workspace` 目录下操作
- `path` 必须经过 `filepath.Clean` + 前缀校验，防止 `../` 逃逸
- 单文件读取上限 10MB
- 文件名黑名单：`.env`, `*.key`, `*.pem` 等敏感文件不可读

实现要点：
- real 模式：直接 `os.ReadFile` / `os.WriteFile` / `os.ReadDir`
- mock 模式：内存 map 模拟文件系统（测试用）

#### 2.2.2 web.search / web.fetch — 网络检索

文件：`internal/agent/workers/web_handler.go`

| 工具名 | 说明 | 输入 | 输出 |
|--------|------|------|------|
| `web.search` | 搜索引擎查询 | `query`, `count?`（默认 5） | `results[]`（title, url, snippet） |
| `web.fetch` | 抓取网页内容 | `url`, `max_chars?`（默认 5000） | `content`（纯文本/markdown）, `title` |

实现要点：
- `web.search`：调 DuckDuckGo HTML API（无需 API key），解析搜索结果
- `web.fetch`：HTTP GET + `golang.org/x/net/html` 提取正文（或用 readability 算法）
- 超时 10s，User-Agent 设为正常浏览器
- 内容截断到 `max_chars` 防止 context window 爆掉

安全约束：
- URL 白名单校验：禁止访问内网地址（127.0.0.1, 10.*, 192.168.* 等）
- 禁止 `file://` 协议

#### 2.2.3 code.execute — 沙箱代码执行

文件：`internal/agent/workers/code_handler.go`

| 工具名 | 说明 | 输入 | 输出 |
|--------|------|------|------|
| `code.execute` | 沙箱中执行代码 | `language`（python/shell）, `code`, `timeout?`（默认 30s） | `stdout`, `stderr`, `exit_code` |

实现方案（二选一）：

**方案 A：Wasm 沙箱**（推荐，已有 wazero 基础）
- Python 代码：编译为 Wasm 或用 Pyodide Wasm 版
- 优点：进程内沙箱，无需 Docker
- 缺点：Python 库受限

**方案 B：Docker 沙箱**
- 每次执行 `docker run --rm --network=none -m 256m --timeout 30 python:3.12-slim -c "..."`
- 优点：完整 Python 生态
- 缺点：需要 Docker

**方案 C：进程沙箱**（最简实现，MVP 选这个）
- `exec.CommandContext` 启动子进程
- 限制：`context.WithTimeout`（硬超时）
- 输出截断到 10KB
- 只允许 Python 和 Shell
- 工作目录锁定在 workspace/sandbox/

实际选择：**先 C（进程沙箱），后续可切到 A/B**。wazero 沙箱可以在 AE-4 后单独迭代。

#### 2.2.4 data.query — 数据库查询

文件：`internal/agent/workers/data_handler.go`

| 工具名 | 说明 | 输入 | 输出 |
|--------|------|------|------|
| `data.query` | 执行只读 SQL 查询 | `sql`, `database?`（默认 forge） | `columns[]`, `rows[][]`, `row_count` |

安全约束：
- **只允许 SELECT**：正则检查，拒绝 INSERT/UPDATE/DELETE/DROP/ALTER/TRUNCATE
- **超时 10s**：`context.WithTimeout`
- **行数限制**：最多返回 100 行
- **连接**：复用 Coordinator 的 `storage.PGStorage` 连接池，或独立只读连接

实现要点：
- 使用 `pgx` 的 `pool.Query` + `rows.FieldDescriptions()` 获取列名
- 结果序列化为 `[][]interface{}` JSON

#### 2.2.5 llm.summarize — LLM 子任务

文件：`internal/agent/workers/llm_handler.go`

| 工具名 | 说明 | 输入 | 输出 |
|--------|------|------|------|
| `llm.summarize` | 对长文本生成摘要 | `text`, `max_length?`（默认 200 字） | `summary` |

实现要点：
- 复用 `harness.LLMClient`，构造摘要 prompt 调一次 LLM
- 这是 Agent 调 LLM 的嵌套调用（Agent → Tool → LLM），技术上值得讲
- 不单独计费，走同一个 API

#### 2.2.6 image.generate — 图片生成

文件：`internal/agent/workers/image_handler.go`

| 工具名 | 说明 | 输入 | 输出 |
|--------|------|------|------|
| `image.generate` | 根据文本描述生成图片 | `prompt`, `width?`, `height?`, `style?` | `image_path`, `width`, `height` |

实现方案：
- mock 模式：生成纯色占位图（Go `image/png` 标准库）
- real 模式：对接免费 API（如 Pollinations.ai 或本地 SD WebUI）
- 输出保存到 workspace

#### 2.2.7 注册到 RegisterAll

`register_all.go` 扩展：

```go
// General-purpose tools (6)
{FileReadDef(), NewFileReadHandler(cfg)},
{FileWriteDef(), NewFileWriteHandler(cfg)},
{FileListDef(), NewFileListHandler(cfg)},
{WebSearchDef(), NewWebSearchHandler(cfg)},
{WebFetchDef(), NewWebFetchHandler(cfg)},
{CodeExecuteDef(), NewCodeExecuteHandler(cfg)},
{DataQueryDef(), NewDataQueryHandler(cfg)},
{LLMSummarizeDef(), NewLLMSummarizeHandler(cfg)},
{ImageGenerateDef(), NewImageGenerateHandler(cfg)},
```

工具总数：18（视频）+ 9（通用）= **27 个工具**。

新增 Category：
- `"file"` — 文件操作
- `"web"` — 网络检索
- `"code"` — 代码执行
- `"data"` — 数据查询
- `"llm"` — LLM 子任务
- `"image"` — 图片生成

---

## 三、Phase AE-3：RAG + Memory

### 3.1 RAG 混合检索（M3）

#### 3.1.1 存储

数据库迁移：`deploy/migrations/003_rag.sql`

```sql
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE documents (
    id          TEXT PRIMARY KEY,
    content     TEXT NOT NULL,
    metadata    JSONB DEFAULT '{}',
    embedding   vector(1536),   -- OpenAI ada-002 维度，可调
    created_at  TIMESTAMPTZ DEFAULT now()
);

-- 向量索引（IVFFlat，数据量小时 HNSW 也行）
CREATE INDEX idx_documents_embedding ON documents
    USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100);

-- 全文检索索引
CREATE INDEX idx_documents_content_fts ON documents
    USING gin (to_tsvector('english', content));
```

#### 3.1.2 Embedder

文件：`internal/agent/rag/embedder.go`

```go
type Embedder interface {
    Embed(ctx context.Context, text string) ([]float32, error)
    EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
}

// LLMEmbedder 通过 LLM API 的 embedding 端点获取向量
type LLMEmbedder struct {
    client  *http.Client
    baseURL string  // bmc-llm-relay
    model   string  // "text-embedding-ada-002" 或其他
}
```

#### 3.1.3 混合检索

文件：`internal/agent/rag/hybrid.go`

```go
type HybridRetriever struct {
    pool     *pgxpool.Pool
    embedder Embedder
}

func (r *HybridRetriever) Search(ctx context.Context, query string, topK int) ([]Document, error) {
    // 1. 向量检索
    embedding, _ := r.embedder.Embed(ctx, query)
    vectorResults := r.vectorSearch(ctx, embedding, topK*2)

    // 2. BM25 全文检索
    bm25Results := r.bm25Search(ctx, query, topK*2)

    // 3. RRF 融合
    return reciprocalRankFusion(vectorResults, bm25Results, topK), nil
}
```

**RRF 公式**：`score(d) = Σ 1/(k + rank_i(d))`，k=60（标准值）

#### 3.1.4 注册为 Agent 工具（Agentic RAG）

文件：`internal/agent/rag/tool.go`

```go
// 注册为 "knowledge.search" 工具
// Agent 主动决定什么时候搜索（不是每次都搜）
func KnowledgeSearchDef() *workers.ToolDef {
    return &workers.ToolDef{
        Name:        "knowledge.search",
        Description: "Search the knowledge base for relevant documents, tool usage examples, or past experience.",
        InputSchema: map[string]workers.ParamDef{
            "query": {Type: "string", Description: "Search query", Required: true},
            "top_k": {Type: "integer", Description: "Number of results (default: 5)"},
        },
        // ...
    }
}
```

#### 3.1.5 初始知识库内容

预填充：
- 18+9 个工具的详细文档（ToolDef 的 Description + InputSchema → 自然语言文档）
- FFmpeg 常用参数速查
- 常见错误和解决方案

### 3.2 Memory 记忆系统（M5）

#### 3.2.1 短期记忆（Redis）

文件：`internal/agent/memory/shortterm.go`

```go
type ShortTermMemory struct {
    redis *redis.Client
    ttl   time.Duration  // 24h
}

// key 格式：forge:memory:short:{session_id}:{key}
func (m *ShortTermMemory) Save(ctx, sessionID, key string, value any) error
func (m *ShortTermMemory) Get(ctx, sessionID, key string) (any, error)
func (m *ShortTermMemory) GetAll(ctx, sessionID string) (map[string]any, error)
```

用途：
- 当前 session 的上下文变量（"用户提到的那个视频路径"）
- 中间结果缓存
- 对话摘要

#### 3.2.2 长期记忆（pgvector）

文件：`internal/agent/memory/longterm.go`

```go
type LongTermMemory struct {
    pool     *pgxpool.Pool
    embedder rag.Embedder
}

// 复用 RAG 的 documents 表，category = "memory"
func (m *LongTermMemory) Save(ctx context.Context, entry MemoryEntry) error
func (m *LongTermMemory) Search(ctx context.Context, query string, topK int) ([]MemoryEntry, error)
```

#### 3.2.3 自动经验提取

在 `harness/loop.go` 的 `Run()` 末尾，任务完成后：

```go
// 用 LLM 提取本次任务的经验
lesson := l.extractLesson(ctx, userInput, result)
// "这次视频换脸任务，先 probe 发现分辨率太大，降采样后成功"
l.memory.SaveLongTerm(ctx, MemoryEntry{
    Content:  lesson,
    Category: "experience",
})
```

已有 `saveMemory()` 方法预留，扩展即可。

---

## 四、Phase AE-4：Guardrails + Checkpointing + Reflexion

### 4.1 Guardrails 安全护栏（M6）

#### 4.1.1 Prompt Injection 检测

文件：`internal/agent/guardrails/injection.go`

```go
type InjectionDetector struct {
    patterns []*regexp.Regexp  // 预编译的危险模式
}

func (d *InjectionDetector) Check(ctx context.Context, input string) error
```

检测模式（规则匹配）：
- `ignore previous instructions`
- `system prompt`
- `you are now`
- `pretend you are`
- `disregard all`
- `override your`
- 大量重复字符（洪水攻击）
- 不可见 Unicode 字符注入

返回 `ErrInjectionDetected` 时 Agent 拒绝执行。

#### 4.1.2 输出内容过滤

文件：`internal/agent/guardrails/content.go`

```go
type ContentFilter struct {
    patterns []*regexp.Regexp  // API key、密码等模式
}

func (f *ContentFilter) Check(ctx context.Context, output string) (string, error)
```

过滤规则：
- API key 模式：`sk-...`, `AKIA...`, `ghp_...` → 替换为 `[REDACTED]`
- 密码模式：`password=...`, `pwd=...`
- Email 地址（可选）
- 内网 IP 地址

#### 4.1.3 Token 预算熔断

文件：`internal/agent/guardrails/budget.go`

```go
type BudgetEnforcer struct {
    redis  *redis.Client
    limits map[string]int64  // session → max tokens
    defaultLimit int64       // 默认 100k tokens
}

func (b *BudgetEnforcer) Check(ctx context.Context, sessionID string) error
func (b *BudgetEnforcer) Record(ctx context.Context, sessionID string, tokens int64) error
```

- 每次 LLM 调用后 `Record()` 累加消耗
- 每次调用前 `Check()` 检查是否超限
- 超限返回 `ErrBudgetExceeded`，Agent 优雅停止
- Redis key: `forge:budget:{session_id}`, TTL 24h

#### 4.1.4 集成到 Harness

已有插槽：`loop.go` 里的 `inputGuard`, `outputGuard`, `budget` 字段。
只需在 Agent 组装时 `WithInputGuard()`, `WithOutputGuard()`, `WithBudget()` 注入。

### 4.2 Checkpointing 状态持久化（M12）

#### 4.2.1 存储

数据库迁移：`deploy/migrations/004_checkpoint.sql`

```sql
CREATE TABLE agent_checkpoints (
    id          TEXT PRIMARY KEY,
    session_id  TEXT NOT NULL,
    step_index  INTEGER NOT NULL,
    messages    JSONB NOT NULL,       -- 完整对话历史
    metadata    JSONB DEFAULT '{}',   -- 额外状态
    created_at  TIMESTAMPTZ DEFAULT now(),
    
    CONSTRAINT uq_checkpoint_session_step UNIQUE (session_id, step_index)
);

CREATE INDEX idx_checkpoints_session ON agent_checkpoints (session_id, step_index DESC);
```

#### 4.2.2 CheckpointStore

文件：`internal/agent/checkpoint/store.go`

```go
type PGCheckpointStore struct {
    pool *pgxpool.Pool
}

func (s *PGCheckpointStore) Save(ctx context.Context, cp *core.Checkpoint) error
func (s *PGCheckpointStore) Load(ctx context.Context, id string) (*core.Checkpoint, error)
func (s *PGCheckpointStore) Latest(ctx context.Context, sessionID string) (*core.Checkpoint, error)
```

- `Save()`：UPSERT（ON CONFLICT session_id+step_index DO UPDATE）
- `Latest()`：按 step_index DESC LIMIT 1
- 只保留最近 N 个 checkpoint（默认 20），旧的自动清理

#### 4.2.3 崩溃恢复

```go
func (l *AgentLoop) RecoverFromCheckpoint(ctx context.Context, sessionID string) (*RunResult, error) {
    cp, err := l.checkpoint.Latest(ctx, sessionID)
    if err != nil {
        return nil, err // 无 checkpoint，需要重新开始
    }
    // 从 checkpoint 的 messages 恢复，继续循环
    return l.runFromMessages(ctx, sessionID, cp.Messages, cp.StepIndex)
}
```

### 4.3 Reflexion 自纠错

#### 4.3.1 设计

在 ReAct Loop 中，工具调用失败时插入一个 **Reflect** 步骤：

```
现有: Think → Act → Observe(失败) → Think(盲目重试)
改为: Think → Act → Observe(失败) → **Reflect** → Re-plan → Act(改进后的参数)
```

#### 4.3.2 实现

扩展 `harness/loop.go`：

```go
// 在 toolResult.Error != "" 时触发
func (l *AgentLoop) reflect(ctx context.Context, messages []core.Message, 
    action *structured.ToolCallRequest, toolError string) (string, error) {
    
    reflectPrompt := fmt.Sprintf(
        `The tool call "%s" failed with error: %s

Reflect on why this failed. Consider:
1. Were the parameters correct?
2. Is there a prerequisite step that was missed?
3. Should a different tool be used instead?
4. What specific changes would fix this?

Provide a brief analysis and revised plan.`, 
        action.Name, toolError)
    
    reflectMsgs := append(messages, core.Message{
        Role: "user", Content: reflectPrompt,
    })
    
    return l.llm.Chat(ctx, reflectMsgs)
}
```

loop.go 中的修改点（伪代码）：

```go
// 原来：工具失败 → 直接把错误当 observation 喂回去
// 改后：工具失败 → reflect → 把反思结果也喂回去

if toolResult.Error != "" {
    // 触发 Reflexion
    reflection, err := l.reflect(ctx, messages, agentResp.Action, toolResult.Error)
    if err == nil {
        // 把反思加入对话历史
        messages = append(messages, core.Message{
            Role: "user",
            Content: fmt.Sprintf("[Reflection]: %s", reflection),
        })
    }
}
```

#### 4.3.3 StepRecord 扩展

```go
type StepRecord struct {
    Step       int
    Thought    string
    Action     *structured.ToolCallRequest
    Result     *core.ToolResult
    Answer     string
    Reflection string  // 新增：非空表示该步触发了自纠错
}
```

---

## 五、A2A 协议预留（不实现，留接口）

在 `core/interfaces.go` 中已有 MCPManager 接口。新增 A2A 相关接口预留：

```go
// A2AClient discovers and communicates with remote agents. (Future: A2A Protocol)
// Not implemented in AE-2~4. Interface reserved for multi-agent scenarios.
type A2AClient interface {
    // Discover fetches the Agent Card from a remote agent.
    Discover(ctx context.Context, agentURL string) (*AgentCard, error)
    // SendTask sends a task to a remote agent and returns the task ID.
    SendTask(ctx context.Context, agentURL string, task *A2ATask) (string, error)
    // GetTaskStatus polls the status of a remote task.
    GetTaskStatus(ctx context.Context, agentURL string, taskID string) (*A2ATaskStatus, error)
}

// AgentCard describes this agent's capabilities (A2A Agent Card spec).
type AgentCard struct {
    Name         string   `json:"name"`
    Description  string   `json:"description"`
    URL          string   `json:"url"`
    Capabilities []string `json:"capabilities"`
    Version      string   `json:"version"`
}
```

面试话术：_"架构预留了 A2A 接口，当前单 Agent 满足业务需求，多 Agent 协作是确定的下一步。"_

---

## 六、文件清单

### 新增文件

| 文件 | Phase | 说明 |
|------|-------|------|
| `internal/agent/mcp/jsonrpc.go` | AE-2 | JSON-RPC 2.0 编解码 |
| `internal/agent/mcp/transport_stdio.go` | AE-2 | stdio 传输层 |
| `internal/agent/mcp/client.go` | AE-2 | MCP Client |
| `internal/agent/mcp/manager.go` | AE-2 | MCP Server 生命周期管理 |
| `internal/agent/mcp/bridge.go` | AE-2 | MCP → ToolRegistry 桥接 |
| `internal/agent/workers/file_handler.go` | AE-2 | file.read/write/list |
| `internal/agent/workers/web_handler.go` | AE-2 | web.search/fetch |
| `internal/agent/workers/code_handler.go` | AE-2 | code.execute |
| `internal/agent/workers/data_handler.go` | AE-2 | data.query |
| `internal/agent/workers/llm_handler.go` | AE-2 | llm.summarize |
| `internal/agent/workers/image_handler.go` | AE-2 | image.generate |
| `internal/agent/rag/embedder.go` | AE-3 | Embedding 接口 |
| `internal/agent/rag/hybrid.go` | AE-3 | 混合检索 |
| `internal/agent/rag/tool.go` | AE-3 | knowledge.search 工具 |
| `internal/agent/memory/shortterm.go` | AE-3 | Redis 短期记忆 |
| `internal/agent/memory/longterm.go` | AE-3 | pgvector 长期记忆 |
| `internal/agent/guardrails/injection.go` | AE-4 | Prompt Injection 检测 |
| `internal/agent/guardrails/content.go` | AE-4 | 输出内容过滤 |
| `internal/agent/guardrails/budget.go` | AE-4 | Token 预算熔断 |
| `internal/agent/checkpoint/store.go` | AE-4 | Checkpoint 持久化 |
| `deploy/migrations/003_rag.sql` | AE-3 | pgvector 表 |
| `deploy/migrations/004_checkpoint.sql` | AE-4 | checkpoint 表 |
| `test/agent_mcp_test.go` | AE-2 | MCP 集成测试 |
| `test/agent_rag_test.go` | AE-3 | RAG 测试 |
| `test/agent_guardrails_test.go` | AE-4 | Guardrails 测试 |

### 修改文件

| 文件 | Phase | 改动 |
|------|-------|------|
| `internal/agent/workers/register_all.go` | AE-2 | 注册 9 个新工具 |
| `internal/agent/workers/registry.go` | AE-2 前置 | 合入 FormatForPrompt/FindSimilar/编辑距离(#5,#9) |
| `internal/agent/tools/registry.go` | AE-2 前置 | **删除**（合并到 workers）(#5) |
| `internal/agent/tools/registry_test.go` | AE-2 前置 | **迁移**到 workers/ (#5) |
| `internal/agent/planning/parser.go` | AE-2 前置 | extractJSON → 复用 structured.ExtractJSONObject (#2) |
| `internal/agent/planning/planner.go` | AE-2 Day3 | DAG 模板 Sprintf → struct+yaml.Marshal (#6) |
| `internal/agent/planning/dag_validate.go` | AE-2 前置 | import 改 workers (#5) |
| `internal/agent/planning/dag_gen.go` | AE-2 前置 | import 改 workers (#5) |
| `internal/agent/structured/validator.go` | AE-2 前置 | 导出 ExtractJSONObject (#2) |
| `internal/agent/harness/tool_router.go` | AE-2 前置 | 加 adaptParams 类型适配 (#4)，import 改 (#5) |
| `internal/agent/harness/loop.go` | AE-3,AE-4 | 扩展 saveMemory + Reflexion |
| `internal/agent/harness/llm.go` | AE-3 | 加重试 + TokenUsage (#1) |
| `internal/agent/harness/context.go` | AE-3 | 压缩后二次检查 (#3) |
| `internal/agent/core/types.go` | AE-3 | +TokenUsage struct, LLMClient 扩展 (#1) |
| `internal/agent/core/interfaces.go` | AE-2,AE-4 | +A2A 预留接口 |
| `internal/agent/agent.go` | AE-2~4 | +Run() 入口 (#8) + WithMCP/WithRAG/WithGuardrails |
| `internal/agent/session/session.go` | AE-4 | +OnTransition 回调 (#7) |
| `internal/agent/planning/dag_gen_test.go` | AE-4 | 补集成测试 (#10) |

---

## 七、评估标准汇总

| Phase | 编号 | 评估项 | 通过条件 |
|-------|------|--------|----------|
| 前置 | E0a | extractJSON 修复 | parser 和 validator 共用同一个 JSON 提取函数 |
| 前置 | E0b | 类型适配 | ToolRouter 传 integer 参数时 handler 不 panic |
| 前置 | E0c | Registry 合并 | `tools/` 包删除，`workers.ToolRegistry` 包含 FormatForPrompt |
| AE-2 | E1 | MCP stdio | MCPClient 与 mock server 成功交换 JSON-RPC |
| AE-2 | E2 | 工具发现 | ListTools 返回 mock server 的工具列表 |
| AE-2 | E3 | 工具调用 | CallTool 成功调用并收到正确响应 |
| AE-2 | E4 | Bridge | MCP 工具出现在 ToolRegistry 中 |
| AE-2 | E5 | file 工具 | read/write/list 在 workspace 内正常工作 |
| AE-2 | E6 | web 工具 | search 返回搜索结果，fetch 返回网页内容 |
| AE-2 | E7 | code 工具 | Python 代码执行并返回 stdout |
| AE-2 | E8 | data 工具 | SELECT 查询返回结果，非 SELECT 被拒绝 |
| AE-2 | E9 | Agent.Run | 完整 Parse→Plan→Execute 链路可走通（#8） |
| AE-2 | E10 | DAG 模板 | yaml.Marshal 生成的 YAML 能被 ParseDAG 解析（#6） |
| AE-3 | E11 | 向量检索 | 文档索引后语义相关查询能检索到 |
| AE-3 | E12 | 混合检索 | RRF 融合结果比单路更好 |
| AE-3 | E13 | 短期记忆 | session 内上下文正确存取 |
| AE-3 | E14 | 长期记忆 | 跨 session 经验可检索 |
| AE-3 | E15 | LLM 重试 | 模拟 429 → 重试成功（#1） |
| AE-3 | E16 | TokenUsage | Chat 调用后能获得 token 消耗数据（#1） |
| AE-3 | E17 | Context 二次检查 | 长 tool result 被截断后 token 数在限制内（#3） |
| AE-4 | E18 | Injection | "ignore previous instructions" 被拦截 |
| AE-4 | E19 | 内容过滤 | API key 被替换为 [REDACTED] |
| AE-4 | E20 | 预算熔断 | 超限后 Agent 停止 |
| AE-4 | E21 | Checkpoint 保存 | 每步后 PG 中有对应记录 |
| AE-4 | E22 | Checkpoint 恢复 | 从 checkpoint 恢复后不重复已完成步骤 |
| AE-4 | E23 | Reflexion | 工具失败后 Agent 分析原因并调整策略重试 |
| AE-4 | E24 | Session 回调 | 状态变化触发注册的回调函数（#7） |
| ALL | E25 | 全量编译 | `go build ./...` ✅ |
| ALL | E26 | 全量测试 | `go test ./...` ✅ |

---

## 八、详细执行计划（按天）

### AE-2 前置修复（Day 0，半天）

| 任务 | 改进# | 文件 | 说明 |
|------|-------|------|------|
| extractJSON bug 修复 | #2 | `planning/parser.go`, `structured/validator.go` | 导出 extractJSONObject 并复用 |
| float64→int 类型适配 | #4 | `harness/tool_router.go` | Call() 里加 adaptParams |
| Registry 合并 | #5 | `tools/registry.go`→删, `workers/registry.go`→合入 | FormatForPrompt/FindSimilar 合入 workers |

### AE-2 Day 1：MCP 协议

| 任务 | 文件 |
|------|------|
| JSON-RPC 编解码 | `internal/agent/mcp/jsonrpc.go` |
| StdioTransport | `internal/agent/mcp/transport_stdio.go` |
| MCPClient | `internal/agent/mcp/client.go` |
| MCPManager | `internal/agent/mcp/manager.go` |
| MCPBridge | `internal/agent/mcp/bridge.go` |
| MCP 集成测试 | `test/agent_mcp_test.go` |

### AE-2 Day 2：通用工具 + Agent.Run()

| 任务 | 改进# | 文件 |
|------|-------|------|
| file.read/write/list | — | `workers/file_handler.go` |
| web.search/fetch | — | `workers/web_handler.go` |
| code.execute | — | `workers/code_handler.go` |
| data.query | — | `workers/data_handler.go` |
| llm.summarize | — | `workers/llm_handler.go` |
| image.generate | — | `workers/image_handler.go` |
| 更新 register_all.go（18→27） | — | `workers/register_all.go` |
| **Agent.Run() 入口** | **#8** | `agent.go` |

### AE-2 Day 3：测试 + 打磨

| 任务 | 改进# | 文件 |
|------|-------|------|
| DAG 模板改 struct+yaml.Marshal | #6 | `planning/planner.go` |
| FindSimilar 加编辑距离 | #9 | `workers/registry.go` |
| 全量 go build / go vet / go test | — | — |

### AE-3 Day 4：RAG + LLM 重试

| 任务 | 改进# | 文件 |
|------|-------|------|
| pgvector 迁移 | — | `deploy/migrations/003_rag.sql` |
| Embedder 接口 + LLM 实现 | — | `internal/agent/rag/embedder.go` |
| 混合检索（向量+BM25+RRF） | — | `internal/agent/rag/hybrid.go` |
| knowledge.search 工具注册 | — | `internal/agent/rag/tool.go` |
| **LLM 重试 + TokenUsage** | **#1** | `harness/llm.go`, `core/types.go` |

### AE-3 Day 5：Memory + Context 优化

| 任务 | 改进# | 文件 |
|------|-------|------|
| 短期记忆（Redis） | — | `internal/agent/memory/shortterm.go` |
| 长期记忆（pgvector） | — | `internal/agent/memory/longterm.go` |
| 扩展 saveMemory（LLM 提取经验） | — | `harness/loop.go` |
| **Context 压缩后二次检查** | **#3** | `harness/context.go` |
| RAG + Memory 测试 | — | `test/agent_rag_test.go` |

### AE-4 Day 6：Guardrails + Checkpointing

| 任务 | 改进# | 文件 |
|------|-------|------|
| Prompt Injection 检测 | — | `guardrails/injection.go` |
| 输出内容过滤 | — | `guardrails/content.go` |
| Token 预算熔断 | — | `guardrails/budget.go` |
| checkpoint 迁移 | — | `deploy/migrations/004_checkpoint.sql` |
| CheckpointStore | — | `internal/agent/checkpoint/store.go` |
| **Session.OnTransition 回调** | **#7** | `session/session.go` |

### AE-4 Day 7：Reflexion + 集成 + 收尾

| 任务 | 改进# | 文件 |
|------|-------|------|
| Reflexion 自纠错 | — | `harness/loop.go` |
| WithXxx 注入所有新模块 | — | `agent.go` |
| A2A 预留接口 | — | `core/interfaces.go` |
| **planning 包集成测试** | **#10** | `planning/dag_gen_test.go` |
| Guardrails 测试 | — | `test/agent_guardrails_test.go` |
| 全量 go build / go test | — | — |
| 更新状态同步文档 | — | `docs/Forge项目开发状态同步.md` |

### 时间预估

| 阶段 | 内容 | 预估 |
|------|------|------|
| AE-2 前置 | Bug 修复 + Registry 合并 | 半天 |
| AE-2 | MCP + 9 工具 + Agent.Run + 打磨 | 2-3 天 |
| AE-3 | RAG + Memory + LLM 重试 + Context 优化 | 1.5-2 天 |
| AE-4 | Guardrails + Checkpointing + Reflexion + 收尾 | 1.5-2 天 |
| | **合计** | **6-8 天** |

---

> 本方案待 CastWell 确认后开始实施。按惯例一次一个 Phase，人工 Review 是硬性门禁。
