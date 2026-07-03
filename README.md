<p align="center">
  <img src="https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat-square&logo=go&logoColor=white" alt="Go Version">
  <img src="https://img.shields.io/badge/PostgreSQL-16+-4169E1?style=flat-square&logo=postgresql&logoColor=white" alt="PostgreSQL">
  <img src="https://img.shields.io/badge/pgvector-✅-4B8BBE?style=flat-square" alt="pgvector">
  <img src="https://img.shields.io/badge/Redis-7+-DC382D?style=flat-square&logo=redis&logoColor=white" alt="Redis">
  <img src="https://img.shields.io/badge/License-MIT-green?style=flat-square" alt="License">
  <img src="https://img.shields.io/badge/Version-2.0-8B5CF6?style=flat-square" alt="Version">
</p>

<h1 align="center">🧠 Agent Memory OS</h1>
<p align="center"><strong>Self-Hosted Long-Term Memory Infrastructure for LLMs & AI Agents</strong></p>

<p align="center">
  <a href="README_CN.md">📖 中文文档</a> &nbsp;|&nbsp;
  <a href="#quick-start">🚀 Quick Start</a> &nbsp;|&nbsp;
  <a href="#architecture">🏗 Architecture</a> &nbsp;|&nbsp;
  <a href="#api-reference">📡 API</a> &nbsp;|&nbsp;
  <a href="#sdk-usage">💻 SDK</a>
</p>

---

## 🆕 What's New in v2.0

### 🔌 Universal Model Provider Layer

Memory Core now has a unified LLM provider abstraction (`provider/`) supporting:

| Provider | Chat | Embed | Models | Base URL Override |
|----------|:----:|:-----:|:------:|:-----------------:|
| **OpenAI** (GPT-4o, o1, etc.) | ✅ | ✅ | ✅ | ✅ |
| **Anthropic** (Claude 3.5/4) | ✅ | ❌ | ✅ | ✅ |
| **Ollama** (Llama, Mistral, etc.) | ✅ | ✅ | ✅ | ✅ |
| **OneAPI** | ✅ | ✅ | ✅ | ✅ |
| **NewAPI** | ✅ | ✅ | ✅ | ✅ |
| **Any OpenAI-compatible** | ✅ | ✅ | ✅ | ✅ |

All providers support custom `BaseURL` — point to OneAPI, NewAPI, or any vLLM/Ollama endpoint. The old hardcoded HTTP calls in `embedding/` and `summary/` have been replaced with clean provider interfaces.

### 🆕 New Platform Plugins (v2)

| Plugin | Protocol | Auth Method |
|--------|----------|-------------|
| **Telegram** | Bot API long-polling | Bot Token |
| **Slack** | Socket Mode WebSocket | Bot Token + App Token |
| **WeChat** | Official Account webhook | SHA1 signature verification |
| **QQ** | OneBot 11/12 HTTP | Bearer access token |

Agent Memory OS is a **production-grade memory backend** designed to give LLMs and AI Agents persistent, searchable, and self-evolving memory. It treats memory as a first-class infrastructure concern — not an afterthought bolted onto a vector database.

### Why not just use a vector database?

Vector similarity alone is fragile. It misses keyword matches, ignores temporal context, has no understanding of entity relationships, and treats every memory as equally important forever.

**Agent Memory OS uses Hybrid Retrieval** — a weighted fusion of five retrieval signals:

| Signal | Weight | Description |
|--------|--------|-------------|
| **Vector Similarity** | 30% | Semantic meaning via embeddings (pgvector) |
| **Keyword Match** | 25% | Exact & fuzzy text match (PostgreSQL tsvector + pg_trgm) |
| **Graph Traversal** | 20% | Entity-relationship proximity (Knowledge Graph) |
| **Time Decay** | 15% | Recent & frequently-accessed memories score higher |
| **Importance** | 10% | LLM-assigned significance score (0.0–1.0) |

Results are then **re-ranked by an LLM cross-encoder** for maximum precision.

---

## 🏗 Architecture

```
                        ┌──────────────────┐
                        │   LLM / Agent     │
                        └────────┬─────────┘
                                 │
                        ┌────────▼─────────┐
                        │   Memory SDK      │  Go, Python (planned)
                        └────────┬─────────┘
                                 │
                    ┌────────────▼────────────┐
                    │   HTTP / gRPC API        │
                    │   Auth: JWT + API Key    │
                    │   Rate Limited           │
                    │   TLS Support            │
                    └────────────┬────────────┘
                                 │
            ┌────────────────────▼────────────────────┐
            │              Memory Core (Go)            │
            │  ┌──────────┐  ┌──────────┐  ┌───────┐ │
            │  │  Event    │  │ Retrieval │  │Plugin │ │
            │  │  Engine   │  │  Engine   │  │System │ │
            │  └─────┬─────┘  └─────┬─────┘  └───┬───┘ │
            │        │              │             │     │
            │  ┌─────▼──────────────▼─────────────▼──┐ │
            │  │       Async Worker Queue             │ │
            │  │  ┌──────────┐  ┌──────────────────┐ │ │
            │  │  │ Entity   │  │ Summary Engine   │ │ │
            │  │  │Extractor │  │ (LLM-powered)    │ │ │
            │  │  ├──────────┤  ├──────────────────┤ │ │
            │  │  │ Embedding│  │ Knowledge Graph  │ │ │
            │  │  │ Engine   │  │ Engine           │ │ │
            │  │  ├──────────┤  ├──────────────────┤ │ │
            │  │  │Importance│  │ Memory Decay     │ │ │
            │  │  │ Scorer   │  │ Scheduler        │ │ │
            │  │  ├──────────┤  ├──────────────────┤ │ │
            │  │  │ Conflict │  │ Re-ranker        │ │ │
            │  │  │ Resolver │  │                  │ │ │
            │  │  └──────────┘  └──────────────────┘ │ │
            │  └──────────────────────────────────────┘ │
            └────────────────────┬──────────────────────┘
                                 │
            ┌────────────────────▼──────────────────────┐
            │              Storage Layer                  │
            │  ┌──────────┐  ┌──────────┐  ┌─────────┐  │
            │  │  Redis   │  │  SQLite  │  │PostgreSQL│  │
            │  │  (Hot)   │→ │(Session) │→ │ +pgvector│  │
            │  │  <1ms    │  │  <5ms    │  │  <50ms   │  │
            │  └──────────┘  └──────────┘  └────┬────┘  │
            │                                    │        │
            │                          ┌─────────▼──────┐ │
            │                          │ Object Storage │ │
            │                          │  (Raw Payload) │ │
            │                          └────────────────┘ │
            └─────────────────────────────────────────────┘
```

### Memory Lifecycle

Every piece of information flows through a **7-stage distillation pipeline**:

```
Raw Event → Episode → Fact → Preference → Personality → Knowledge → Archive
   (0)        (1)       (2)       (3)          (4)          (5)        (6)
```

| Level | Description | Trigger |
|-------|-------------|---------|
| **Raw Event** | Unprocessed input text | Immediate on ingest |
| **Episode** | A coherent narrative summary | Summary Engine (async) |
| **Fact** | An objective, atomic statement | Entity extraction + dedup |
| **Preference** | User like/dislike/choice | Pattern detection |
| **Personality** | Stable trait observation | Longitudinal analysis |
| **Knowledge** | Domain-general information | Cross-session synthesis |
| **Archive** | Cold storage, rarely accessed | Memory decay + age |

---

## 🚀 Quick Start

### Prerequisites

- **Go** 1.22+
- **PostgreSQL** 14+ with [pgvector](https://github.com/pgvector/pgvector) extension
- **Redis** 7+ (optional but recommended)
- **Docker** (optional, for one-command setup)

### One-Command Docker Setup

```bash
git clone https://github.com/agent-memoryos/memory-core.git
cd memory-core
make docker-up
```

This starts PostgreSQL (pgvector), Redis, and Memory Core together.

### Manual Setup

```bash
# 1. Clone
git clone https://github.com/agent-memoryos/memory-core.git
cd memory-core

# 2. Configure
cp .env.example .env
# Edit .env — at minimum, set AUTH_JWT_SECRET

# 3. Generate a secure JWT secret
openssl rand -base64 64  # paste into .env as AUTH_JWT_SECRET

# 4. Run migration
make migrate DB_USER=memoryos DB_HOST=localhost DB_NAME=memoryos

# 5. Build & Run
make build
./bin/memory-core

# 6. Verify
curl http://localhost:8080/health
# → {"status":"ok","timestamp":"2026-07-02T..."}
```

---

## 📡 API Reference

### Authentication

All API endpoints (except `/health`) require authentication via:

- **JWT Bearer token**: `Authorization: Bearer <token>`
- **API Key**: `X-API-Key: mcos_<hex>`

### Endpoints

| Method | Path | Auth | Description |
|--------|------|:----:|-------------|
| `GET` | `/health` | ✗ | Health check |
| `POST` | `/api/v1/ingest` | ✓ | Ingest raw text into memory |
| `GET` | `/api/v1/memories/{id}` | ✓ | Retrieve a single memory |
| `POST` | `/api/v1/search` | ✓ | Hybrid search across memories |
| `GET` | `/api/v1/stats` | ✓ | Memory statistics |

### Ingest Example

```bash
curl -X POST http://localhost:8080/api/v1/ingest \
  -H "Content-Type: application/json" \
  -H "X-API-Key: mcos_your_api_key_here" \
  -d '{
    "user_id": "550e8400-e29b-41d4-a716-446655440000",
    "text": "Today I discussed the Q3 roadmap with Alice. We decided to prioritize the Android app.",
    "source": "web"
  }'

# Response (HTTP 201)
{
  "event_id": "a1b2c3d4-...",
  "memory_id": "e5f6a7b8-...",
  "status": "queued"
}
```

### Search Example

```bash
curl -X POST http://localhost:8080/api/v1/search \
  -H "Content-Type: application/json" \
  -H "X-API-Key: mcos_your_api_key_here" \
  -d '{
    "user_id": "550e8400-e29b-41d4-a716-446655440000",
    "query": "Q3 roadmap Android",
    "top_k": 5
  }'

# Response (HTTP 200)
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

## 💻 SDK Usage

### Go SDK

```go
import "github.com/agent-memoryos/memory-core/sdk"

func main() {
    client := sdk.NewClient("http://localhost:8080", "mcos_your_api_key_here")

    // Ingest a memory
    result, _ := client.Ingest(ctx, &sdk.IngestInput{
        UserID: "user-123",
        Text:   "Remember that I prefer dark mode in all applications.",
        Source: "agent",
    })
    fmt.Printf("Memory ID: %s\n", result.MemoryID)

    // Search memories
    search, _ := client.Search(ctx, &sdk.SearchInput{
        UserID: "user-123",
        Query:  "dark mode preference",
        TopK:   5,
    })
    for _, r := range search.Results {
        fmt.Printf("[%.3f] %s\n", r.Score, r.Memory.Content)
    }
}
```

---

## 🔌 Plugin System

Memory Core ingests data from any source via plugins. Each plugin implements the `SourcePlugin` interface and converts external data into memory events.

### Built-in Plugins

| Plugin | Status | Description |
|--------|:------:|-------------|
│ **Discord** | ✅ v1 | Bot-based message ingestion |
│ Telegram | ✅ v2 | Bot API long-polling |
│ QQ | ✅ v2 | OneBot 11/12 webhook |
│ WeChat | ✅ v2 | Official account webhook |
│ Slack | ✅ v2 | Socket Mode WebSocket |
| Email | 🚧 Planned | IMAP inbox monitoring |
| GitHub | 🚧 Planned | Issues, PRs, commits |
| Obsidian | 🚧 Planned | Vault sync |
| Notion | 🚧 Planned | Database sync |

### Building a Custom Plugin

```go
package myapp

import (
    "context"
    "github.com/agent-memoryos/memory-core/plugins"
)

type MyPlugin struct{}

func (p *MyPlugin) Name() string                    { return "myapp" }
func (p *MyPlugin) Start(ctx context.Context) error  { /* connect to source */ return nil }
func (p *MyPlugin) Stop() error                       { /* disconnect */ return nil }
func (p *MyPlugin) Health(ctx context.Context) error  { return nil }

// Register
registry := plugins.NewRegistry(eventEngine)
registry.Register(&MyPlugin{})
registry.StartAll(ctx)
```

---

## 🔒 Security

Agent Memory OS is built with security as a first-class concern:

- ✅ **Parameterized queries** throughout — zero SQL injection surface
- ✅ **JWT HS256** with mandatory 32+ byte secret, algorithm verification
- ✅ **API Key** support with SHA-256 hashed storage, constant-time comparison
- ✅ **Rate limiting** — token-bucket per client IP (configurable)
- ✅ **Request body size limits** — prevents memory exhaustion DoS
- ✅ **OWASP security headers** — CSP, HSTS, X-Frame-Options, etc.
- ✅ **Panic recovery** — never crashes on a single request
- ✅ **Error sanitization** — internal errors never leak to clients
- ✅ **CORS origin reflection** — no wildcard origins
- ✅ **TLS support** — optional HTTPS with configurable certificates
- ✅ **Non-root container** — Docker image runs as unprivileged user
- ✅ **Stripped binaries** — no debug symbols in production builds

---

## 📂 Project Structure

```
memory-core/
├── cmd/server/main.go        # Entry point — wires everything together
├── config/config.go          # Environment-based configuration
├── types/                    # Domain types
│   ├── memory.go             # Memory lifecycle & levels
│   └── event.go              # Event Sourcing event definitions
├── storage/                  # Multi-tier storage
│   ├── interfaces.go         # MemoryStore, EventStore, HotCache, SessionCache
│   ├── postgres.go           # PostgreSQL + pgvector + tsvector
│   ├── redis.go              # Redis hot cache
│   └── sqlite.go             # SQLite session cache
├── event/engine.go           # Event Sourcing engine (append-only)
├── embedding/engine.go       # OpenAI / Ollama / Local embedding
├── graph/engine.go           # Knowledge graph (entities + relations + traversal)
├── retrieval/hybrid.go       # Hybrid Search (5-signal fusion)
├── reranker/reranker.go      # LLM cross-encoder reranker
├── summary/engine.go         # LLM memory distillation
├── parser/parser.go          # Entity extraction + keyword analysis
├── scheduler/worker.go       # Async worker pool with retry
├── api/                      # HTTP layer
│   ├── handler.go            # Request handlers
│   ├── router.go             # Route definitions
│   └── middleware.go         # Security middleware stack
├── auth/auth.go              # JWT + API Key authentication
├── sdk/client.go             # Go SDK client
├── plugins/                  # Data source plugins
│   ├── registry.go           # Plugin registration & lifecycle
│   ├── discord/plugin.go     # Discord integration
│   ├── telegram/             # (planned)
│   ├── qq/                   # (planned)
│   └── ...
├── migration/001_initial.sql # Database schema
├── Dockerfile                # Distroless multi-stage build
├── docker-compose.yml        # Local dev stack
├── .env.example              # Configuration reference
├── Makefile                  # Build automation
└── go.mod / go.sum           # Go module
```

---

## ⚙️ Configuration Reference

<details>
<summary>Click to expand all configuration variables</summary>

| Variable | Default | Description |
|----------|---------|-------------|
| `HTTP_PORT` | `8080` | HTTP server port |
| `GRPC_PORT` | `9090` | gRPC server port |
| `TLS_ENABLED` | `false` | Enable HTTPS |
| `TLS_CERT_FILE` | — | TLS certificate path |
| `TLS_KEY_FILE` | — | TLS private key path |
| `DB_HOST` | `localhost` | PostgreSQL host |
| `DB_PORT` | `5432` | PostgreSQL port |
| `DB_USER` | `memoryos` | PostgreSQL user |
| `DB_PASSWORD` | — | PostgreSQL password |
| `DB_NAME` | `memoryos` | PostgreSQL database |
| `DB_SSLMODE` | `require` | PostgreSQL SSL mode |
| `REDIS_HOST` | `localhost` | Redis host |
| `REDIS_PORT` | `6379` | Redis port |
| `EMBEDDING_PROVIDER` | `openai` | `openai`, `ollama`, or `local` |
| `EMBEDDING_MODEL` | `text-embedding-3-small` | Embedding model name |
| `EMBEDDING_API_KEY` | — | API key for embedding provider |
| `LLM_PROVIDER` | `openai` | LLM provider |
| `LLM_MODEL` | `gpt-4o-mini` | LLM model for summarization |
| `AUTH_JWT_SECRET` | **REQUIRED** | JWT signing secret (min 32 bytes) |
| `SEC_RATE_LIMIT_RPS` | `100` | Max requests/second per IP |
| `SEC_RATE_LIMIT_BURST` | `200` | Burst allowance |
| `WORKER_CONCURRENCY` | `4` | Async worker count |

</details>

---

## 📄 License

MIT © Agent Memory OS Contributors

---

<p align="center">
  <sub>Built with ❤️ for the AI agent ecosystem</sub>
</p>
