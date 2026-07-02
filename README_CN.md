<p align="center">
  <img src="https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat-square&logo=go&logoColor=white" alt="Go Version">
  <img src="https://img.shields.io/badge/PostgreSQL-16+-4169E1?style=flat-square&logo=postgresql&logoColor=white" alt="PostgreSQL">
  <img src="https://img.shields.io/badge/pgvector-✅-4B8BBE?style=flat-square" alt="pgvector">
  <img src="https://img.shields.io/badge/Redis-7+-DC382D?style=flat-square&logo=redis&logoColor=white" alt="Redis">
  <img src="https://img.shields.io/badge/License-MIT-green?style=flat-square" alt="License">
</p>

<h1 align="center">🧠 Agent Memory OS</h1>
<p align="center"><strong>面向 LLM 与 AI Agent 的自托管长期记忆基础设施</strong></p>

<p align="center">
  <a href="README.md">📖 English</a> &nbsp;|&nbsp;
  <a href="#快速开始">🚀 快速开始</a> &nbsp;|&nbsp;
  <a href="#架构设计">🏗 架构设计</a> &nbsp;|&nbsp;
  <a href="#api-文档">📡 API</a> &nbsp;|&nbsp;
  <a href="#sdk-使用">💻 SDK</a>
</p>

---

## ❓ 什么是 Agent Memory OS？

Agent Memory OS 是一个**生产级记忆后端**，专为 LLM 和 AI Agent 提供持久化、可搜索、自我演化的记忆能力。它将记忆视为一等基础设施，而非简单地给向量数据库打个补丁。

### 为什么不直接用向量数据库？

纯向量检索存在明显缺陷：遗漏关键词匹配、忽略时间上下文、不理解实体关系、且将所有记忆视为同等重要。

**Agent Memory OS 采用混合检索**——五种检索信号的加权融合：

| 检索信号 | 权重 | 说明 |
|----------|:----:|------|
| **向量相似度** | 30% | 基于语义 Embedding（pgvector） |
| **关键词匹配** | 25% | 精确与模糊文本匹配（PostgreSQL tsvector + pg_trgm） |
| **图谱遍历** | 20% | 实体关系接近度（知识图谱） |
| **时间衰减** | 15% | 近期和高频访问的记忆得分更高 |
| **重要性评分** | 10% | LLM 分配的显著性评分（0.0–1.0） |

检索结果再经由 **LLM 交叉编码器重排序**，确保最高精准度。

---

## 🏗 架构设计

```
                        ┌──────────────────┐
                        │   LLM / Agent     │
                        └────────┬─────────┘
                                 │
                        ┌────────▼─────────┐
                        │   Memory SDK      │  Go、Python（计划中）
                        └────────┬─────────┘
                                 │
                    ┌────────────▼────────────┐
                    │   HTTP / gRPC API        │
                    │   认证：JWT + API Key     │
                    │   频率限制                │
                    │   TLS 支持                │
                    └────────────┬────────────┘
                                 │
            ┌────────────────────▼────────────────────┐
            │              Memory Core (Go)            │
            │  ┌──────────┐  ┌──────────┐  ┌───────┐ │
            │  │  事件溯源  │  │  混合检索  │  │插件系统│ │
            │  │  引擎     │  │  引擎     │  │       │ │
            │  └─────┬─────┘  └─────┬─────┘  └───┬───┘ │
            │        │              │             │     │
            │  ┌─────▼──────────────▼─────────────▼──┐ │
            │  │       异步任务队列                    │ │
            │  │  ┌──────────┐  ┌──────────────────┐ │ │
            │  │  │ 实体提取  │  │ 摘要引擎（LLM）   │ │ │
            │  │  ├──────────┤  ├──────────────────┤ │ │
            │  │  │ 向量嵌入  │  │ 知识图谱引擎     │ │ │
            │  │  ├──────────┤  ├──────────────────┤ │ │
            │  │  │ 重要性    │  │ 记忆衰减调度器   │ │ │
            │  │  │ 评分器    │  │                  │ │ │
            │  │  ├──────────┤  ├──────────────────┤ │ │
            │  │  │ 冲突解决  │  │ 结果重排序       │ │ │
            │  │  └──────────┘  └──────────────────┘ │ │
            │  └──────────────────────────────────────┘ │
            └────────────────────┬──────────────────────┘
                                 │
            ┌────────────────────▼──────────────────────┐
            │              存储层                         │
            │  ┌──────────┐  ┌──────────┐  ┌─────────┐  │
            │  │  Redis   │  │  SQLite  │  │PostgreSQL│  │
            │  │  热缓存   │→ │ 会话缓存  │→ │ +pgvector│  │
            │  │  <1ms    │  │  <5ms    │  │  <50ms   │  │
            │  └──────────┘  └──────────┘  └────┬────┘  │
            │                                    │        │
            │                          ┌─────────▼──────┐ │
            │                          │  对象存储       │ │
            │                          │ （原始载荷）    │ │
            │                          └────────────────┘ │
            └─────────────────────────────────────────────┘
```

### 记忆生命周期

每一条信息流经 **7 个阶段的蒸馏流水线**：

```
原始事件 → 情节 → 事实 → 偏好 → 性格 → 知识 → 归档
  (0)      (1)    (2)    (3)    (4)    (5)    (6)
```

| 层级 | 描述 | 触发方式 |
|------|------|----------|
| **原始事件** | 未处理的输入文本 | 写入时即时生成 |
| **情节** | 连贯的叙事摘要 | 摘要引擎（异步） |
| **事实** | 客观的原子陈述 | 实体提取 + 去重 |
| **偏好** | 用户的喜欢/不喜欢/选择 | 模式检测 |
| **性格** | 稳定的个性特征观察 | 长期分析 |
| **知识** | 跨领域通用信息 | 跨会话综合 |
| **归档** | 冷存储，极少访问 | 记忆衰减 + 时效 |

---

## 🚀 快速开始

### 环境要求

- **Go** 1.22+
- **PostgreSQL** 14+ 并安装 [pgvector](https://github.com/pgvector/pgvector) 扩展
- **Redis** 7+（可选，但强烈推荐）
- **Docker**（可选，用于一键启动）

### Docker 一键部署

```bash
git clone https://github.com/agent-memoryos/memory-core.git
cd memory-core
make docker-up
```

这将同时启动 PostgreSQL（pgvector）、Redis 和 Memory Core。

### 手动部署

```bash
# 1. 克隆仓库
git clone https://github.com/agent-memoryos/memory-core.git
cd memory-core

# 2. 配置环境变量
cp .env.example .env
# 编辑 .env —— 至少需要设置 AUTH_JWT_SECRET

# 3. 生成安全的 JWT 密钥
openssl rand -base64 64  # 将输出粘贴到 .env 的 AUTH_JWT_SECRET

# 4. 执行数据库迁移
make migrate DB_USER=memoryos DB_HOST=localhost DB_NAME=memoryos

# 5. 编译并运行
make build
./bin/memory-core

# 6. 验证服务
curl http://localhost:8080/health
# → {"status":"ok","timestamp":"2026-07-02T..."}
```

---

## 📡 API 文档

### 认证方式

除 `/health` 外，所有接口均需认证：

- **JWT Bearer Token**：`Authorization: Bearer <token>`
- **API Key**：`X-API-Key: mcos_<hex>`

### 接口列表

| 方法 | 路径 | 认证 | 说明 |
|--------|------|:----:|-------------|
| `GET` | `/health` | ✗ | 健康检查 |
| `POST` | `/api/v1/ingest` | ✓ | 写入原始文本到记忆系统 |
| `GET` | `/api/v1/memories/{id}` | ✓ | 按 ID 获取单条记忆 |
| `POST` | `/api/v1/search` | ✓ | 混合检索 |
| `GET` | `/api/v1/stats` | ✓ | 记忆统计信息 |

### 写入示例

```bash
curl -X POST http://localhost:8080/api/v1/ingest \
  -H "Content-Type: application/json" \
  -H "X-API-Key: mcos_your_api_key_here" \
  -d '{
    "user_id": "550e8400-e29b-41d4-a716-446655440000",
    "text": "今天和 Alice 讨论了 Q3 路线图，决定优先推进 Android 客户端。",
    "source": "web"
  }'

# 响应（HTTP 201）
{
  "event_id": "a1b2c3d4-...",
  "memory_id": "e5f6a7b8-...",
  "status": "queued"
}
```

### 检索示例

```bash
curl -X POST http://localhost:8080/api/v1/search \
  -H "Content-Type: application/json" \
  -H "X-API-Key: mcos_your_api_key_here" \
  -d '{
    "user_id": "550e8400-e29b-41d4-a716-446655440000",
    "query": "Q3 路线图 Android",
    "top_k": 5
  }'

# 响应（HTTP 200）
{
  "results": [
    {
      "memory": { "id": "...", "content": "...", "level": "episode", ... },
      "score": 0.872,
      "score_breakdown": {
        "vector_score": 0.285,
        "keyword_score": 0.220,
        "graph_score": 0.150,
        "time_score": 0.120,
        "importance_bonus": 0.097,
        "final_score": 0.872
      }
    }
  ],
  "took_ms": 12
}
```

---

## 💻 SDK 使用

### Go SDK

```go
import "github.com/agent-memoryos/memory-core/sdk"

func main() {
    client := sdk.NewClient("http://localhost:8080", "mcos_your_api_key_here")

    // 写入记忆
    result, _ := client.Ingest(ctx, &sdk.IngestInput{
        UserID: "user-123",
        Text:   "记住：我在所有应用中都偏好深色模式。",
        Source: "agent",
    })
    fmt.Printf("记忆 ID: %s\n", result.MemoryID)

    // 搜索记忆
    search, _ := client.Search(ctx, &sdk.SearchInput{
        UserID: "user-123",
        Query:  "深色模式偏好",
        TopK:   5,
    })
    for _, r := range search.Results {
        fmt.Printf("[%.3f] %s\n", r.Score, r.Memory.Content)
    }
}
```

---

## 🔌 插件系统

Memory Core 通过插件从任意数据源采集数据。每个插件实现 `SourcePlugin` 接口，将外部数据转换为记忆事件。

### 内置插件

| 插件 | 状态 | 说明 |
|--------|:------:|-------------|
| **Discord** | ✅ 已实现 | 基于 Bot 的消息采集 |
| Telegram | 🚧 计划中 | Bot API 集成 |
| QQ | 🚧 计划中 | 群聊和私聊消息 |
| 微信 | 🚧 计划中 | 公众号消息 |
| Slack | 🚧 计划中 | 工作区事件 |
| 邮件 | 🚧 计划中 | IMAP 收件箱监听 |
| GitHub | 🚧 计划中 | Issues、PR、Commits |
| Obsidian | 🚧 计划中 | 仓库同步 |
| Notion | 🚧 计划中 | 数据库同步 |

### 开发自定义插件

```go
package myapp

import (
    "context"
    "github.com/agent-memoryos/memory-core/plugins"
)

type MyPlugin struct{}

func (p *MyPlugin) Name() string                    { return "myapp" }
func (p *MyPlugin) Start(ctx context.Context) error  { /* 连接数据源 */ return nil }
func (p *MyPlugin) Stop() error                       { /* 断开连接 */ return nil }
func (p *MyPlugin) Health(ctx context.Context) error  { return nil }

// 注册插件
registry := plugins.NewRegistry(eventEngine)
registry.Register(&MyPlugin{})
registry.StartAll(ctx)
```

---

## 🔒 安全设计

Agent Memory OS 将安全性作为一等设计目标：

- ✅ **全参数化查询** —— SQL 注入零暴露面
- ✅ **JWT HS256** —— 强制 32+ 字节密钥，签名算法验证
- ✅ **API Key** —— SHA-256 哈希存储，恒定时间比较
- ✅ **频率限制** —— 基于客户端 IP 的令牌桶算法（可配置）
- ✅ **请求体大小限制** —— 防止内存耗尽 DoS 攻击
- ✅ **OWASP 安全头** —— CSP、HSTS、X-Frame-Options 等
- ✅ **Panic 恢复** —— 单请求异常不会导致服务崩溃
- ✅ **错误信息脱敏** —— 内部错误绝不泄露给客户端
- ✅ **CORS 来源反射** —— 不使用通配符来源
- ✅ **TLS 支持** —— 可选 HTTPS，可配置证书
- ✅ **非 root 容器** —— Docker 镜像以非特权用户运行
- ✅ **二进制裁剪** —— 生产构建去除调试符号

---

## 📂 项目结构

```
memory-core/
├── cmd/server/main.go        # 入口——组装所有组件
├── config/config.go          # 基于环境变量的配置
├── types/                    # 领域类型
│   ├── memory.go             # 记忆生命周期与层级
│   └── event.go              # 事件溯源事件定义
├── storage/                  # 多层存储
│   ├── interfaces.go         # MemoryStore、EventStore、HotCache、SessionCache
│   ├── postgres.go           # PostgreSQL + pgvector + tsvector
│   ├── redis.go              # Redis 热缓存
│   └── sqlite.go             # SQLite 会话缓存
├── event/engine.go           # 事件溯源引擎（仅追加）
├── embedding/engine.go       # OpenAI / Ollama / Local 向量嵌入
├── graph/engine.go           # 知识图谱（实体 + 关系 + 图遍历）
├── retrieval/hybrid.go       # 混合检索（五信号融合）
├── reranker/reranker.go      # LLM 交叉编码器重排序
├── summary/engine.go         # LLM 记忆蒸馏
├── parser/parser.go          # 实体提取 + 关键词分析
├── scheduler/worker.go       # 异步任务池（支持重试）
├── api/                      # HTTP 层
│   ├── handler.go            # 请求处理器
│   ├── router.go             # 路由定义
│   └── middleware.go         # 安全中间件栈
├── auth/auth.go              # JWT + API Key 认证
├── sdk/client.go             # Go SDK 客户端
├── plugins/                  # 数据源插件
│   ├── registry.go           # 插件注册与生命周期管理
│   ├── discord/plugin.go     # Discord 集成
│   └── ...
├── migration/001_initial.sql # 数据库 Schema
├── Dockerfile                # Distroless 多阶段构建
├── docker-compose.yml        # 本地开发环境
├── .env.example              # 配置参考
├── Makefile                  # 构建自动化
└── go.mod / go.sum           # Go 模块
```

---

## ⚙️ 配置参考

<details>
<summary>点击展开所有配置变量</summary>

| 变量 | 默认值 | 说明 |
|----------|---------|-------------|
| `HTTP_PORT` | `8080` | HTTP 服务端口 |
| `GRPC_PORT` | `9090` | gRPC 服务端口 |
| `TLS_ENABLED` | `false` | 启用 HTTPS |
| `TLS_CERT_FILE` | — | TLS 证书路径 |
| `TLS_KEY_FILE` | — | TLS 私钥路径 |
| `DB_HOST` | `localhost` | PostgreSQL 主机 |
| `DB_PORT` | `5432` | PostgreSQL 端口 |
| `DB_USER` | `memoryos` | PostgreSQL 用户 |
| `DB_PASSWORD` | — | PostgreSQL 密码 |
| `DB_NAME` | `memoryos` | PostgreSQL 数据库名 |
| `DB_SSLMODE` | `require` | PostgreSQL SSL 模式 |
| `REDIS_HOST` | `localhost` | Redis 主机 |
| `REDIS_PORT` | `6379` | Redis 端口 |
| `EMBEDDING_PROVIDER` | `openai` | 可选：`openai`、`ollama`、`local` |
| `EMBEDDING_MODEL` | `text-embedding-3-small` | Embedding 模型名称 |
| `EMBEDDING_API_KEY` | — | Embedding 服务的 API Key |
| `LLM_PROVIDER` | `openai` | LLM 服务商 |
| `LLM_MODEL` | `gpt-4o-mini` | 用于摘要生成的 LLM 模型 |
| `AUTH_JWT_SECRET` | **必填** | JWT 签名密钥（至少 32 字节） |
| `SEC_RATE_LIMIT_RPS` | `100` | 每 IP 每秒最大请求数 |
| `SEC_RATE_LIMIT_BURST` | `200` | 突发允许量 |
| `WORKER_CONCURRENCY` | `4` | 异步任务并发数 |

</details>

---

## 📄 开源协议

MIT © Agent Memory OS 贡献者

---

<p align="center">
  <sub>为 AI Agent 生态而构建 ❤️</sub>
</p>
