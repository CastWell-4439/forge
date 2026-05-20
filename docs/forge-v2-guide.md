# Forge V2 使用指南

## 概述

Forge V2 是一个 AI 驱动的工作流自动化引擎，专为代码仓库管理设计。核心功能：

- **DAG 工作流编排** — 定义多步骤任务，支持条件分支、循环重试、结果路由
- **Human-In-The-Loop (HITL)** — 关键操作需人工审批，通过飞书/OpenClaw 交互
- **多 Worker 架构** — AI、Git、Shell、Database、Claude Code 等多种执行器
- **CEL 表达式引擎** — 动态条件判断，基于上游任务结果做分支决策

## 快速开始

### 1. 项目配置

在 `projects/` 目录下创建项目 YAML：

```yaml
project:
  name: my_project
  repo: git@github.com:org/repo.git
  local_path: /path/to/local/clone
  default_branch: develop

databases:
  primary:
    type: postgres
    host: db.example.com
    port: 5432
    database: mydb
    user: readonly
    password_env: FORGE_DB_PASSWORD
```

### 2. 工作流定义

在 `workflows/` 目录下创建工作流 YAML：

```yaml
name: my_workflow
version: "2.0"
timeout: 1h

tasks:
  step_1:
    handler: ai
    params:
      model: claude-sonnet-4-20250514
      input: "分析这个问题"
    timeout: 60s

  step_2:
    handler: hitl
    depends_on: [step_1]
    params:
      action: request_approval
      message: "请审批"
      options: [approve, reject]
```

### 3. 触发工作流

```bash
# HTTP 触发
curl -X POST http://localhost:8080/api/workflows/my_workflow/trigger \
  -H "Content-Type: application/json" \
  -d '{"bug_id": "123", "bug_title": "fix crash"}'

# Cron 自动触发（配置在 workflow YAML 中）
triggers:
  - type: cron
    schedule: "*/5 * * * *"
```

## Worker 类型

### AI Worker
调用 LLM 进行分析、生成、判断。

```yaml
handler: ai
params:
  model: claude-sonnet-4-20250514
  system_prompt: "你是一个代码审查专家"
  input: "{{ .results.prev_step.output }}"
  output_format: json  # text | json
```

### Git Worker
Git 操作：创建分支、commit、push、创建 MR。

```yaml
handler: git
params:
  action: create_branch | commit | push | push_and_mr
  base: develop
  name: "feature/xxx"
```

### Shell Worker
执行白名单命令。

```yaml
handler: shell
params:
  action: execute
  command: "go test ./..."
  working_dir: "{{ .project.local_path }}"
```

允许的命令在项目配置 `conventions.allowed_shell_commands` 中定义。

### Database Worker
只读数据库查询。

```yaml
handler: database
params:
  action: query_pg | query_redis
  connection: primary
  query: "SELECT id, name FROM users WHERE status = 'active'"
```

**安全限制**：
- PostgreSQL: 仅 SELECT，自动追加 LIMIT 100
- Redis: 仅 GET / KEYS
- 超时: 30 秒

### Claude Code Worker
调用 Claude Code CLI 进行代码修改。

```yaml
handler: claude_code
params:
  action: execute
  prompt: "修复这个 bug..."
  working_dir: "{{ .project.local_path }}"
  branch: "fix/bug-123"
```

### HITL Worker
人工审批/通知。

```yaml
handler: hitl
params:
  action: notify | request_approval | request_input | notify_and_wait
  message: "需要你的决定"
  options: [approve, reject, modify]
  timeout: 4h
```

## DAG 高级特性

### 条件执行 (CEL)

```yaml
tasks:
  deploy:
    handler: shell
    condition: 'results.tests.exit_code == 0 && results.review.decision == "approve"'
    params:
      command: "make deploy"
```

可用变量：
- `results` — 上游任务结果 map
- `vars` — 工作流变量
- `iteration` — 当前循环次数
- `workflow_id` — 工作流实例 ID

### 结果路由 (on_result)

```yaml
tasks:
  run_tests:
    handler: shell
    params:
      command: "go test ./..."
    on_result:
      success: continue
      failure: "goto:write_fix"    # 失败时跳回修复步骤
      timeout: abort
```

支持的动作：
- `continue` — 继续下一步（默认）
- `abort` — 终止工作流
- `goto:<task_name>` — 跳转到指定任务
- `skip` — 跳过下游任务

### 循环支持

```yaml
tasks:
  write_fix:
    handler: claude_code
    params:
      prompt: "修复 bug"
    loop:
      max_iterations: 3
      break_on: 'results.run_tests.exit_code == 0'
```

硬上限：100 次（防无限循环）。

## HITL 交互

### 审批流程

1. Forge 发送审批请求到飞书
2. 用户收到消息卡片，包含操作选项
3. 用户回复决定

### 回复格式

```
forge respond <request_id> <decision> [feedback]
```

示例：
```
forge respond req_abc123 approve LGTM
forge respond req_abc123 reject 方案有风险，建议用方案 B
```

### API 端点

```
POST /api/hitl/respond   — 提交审批决定
GET  /api/hitl/pending   — 查看待处理请求
POST /api/workflows/<name>/trigger — 触发工作流
```

## 安全策略

| 操作 | 策略 |
|------|------|
| 数据库写入 | ❌ 禁止 |
| 线上环境操作 | ❌ 禁止 |
| 文件删除 | 需 HITL 审批 |
| 代码合并到 master | 需 HITL 审批 + 人工 MR |
| Shell 命令 | 白名单限制 |
| 创建分支/commit | 自动（仅 feature/fix 分支） |

## 配置参考

### 环境变量

```bash
FORGE_PG_PASSWORD_AVP=xxx      # PostgreSQL 密码
FORGE_REDIS_PASSWORD=xxx        # Redis 密码
FORGE_OPENCLAW_URL=http://localhost:3000  # OpenClaw 地址
```

### 数据保留

默认 30 天，可在 `config.yaml` 中调整：

```yaml
retention:
  workflow_history: 30d
  hitl_requests: 90d
  task_logs: 7d
```

## 架构

```
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│   OpenClaw   │────▶│    Forge     │────▶│ Claude Code  │
│  (触发/HITL) │◀────│  (DAG 引擎)  │◀────│  (代码执行)   │
└──────────────┘     └──────────────┘     └──────────────┘
                            │
              ┌─────────────┼─────────────┐
              ▼             ▼             ▼
        ┌──────────┐ ┌──────────┐ ┌──────────┐
        │ AI Worker│ │Git Worker│ │DB Worker │
        └──────────┘ └──────────┘ └──────────┘
```

## 常见问题

**Q: 工作流卡在 HITL 审批怎么办？**
A: 默认 timeout 后自动按配置处理（abort/continue）。也可以手动回复 `forge respond <id> approve`。

**Q: 循环一直不停怎么办？**
A: 硬上限 100 次。如果 break_on 条件设置不当，到达上限后自动终止并报错。

**Q: 如何查看工作流状态？**
A: `GET /api/hitl/pending` 查看待审批；后续 V2-10.5 会加 Dashboard。
