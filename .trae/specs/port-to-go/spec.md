# 企业内部智能问答系统 Go 重写 Spec

## Why

现有系统 `/Users/wcf/python-project/ai-worker` 使用 Python (FastAPI + LangGraph + Chroma 内嵌) 实现企业内部中间件智能问答（mw-bot）。目标项目 `/Users/wcf/go-project/mw-bot` 当前为空目录，需使用 Go 语言重写整个后端 API 与 RocketMQ MCP Server，保持与现有功能、HTTP API 契约、数据库 schema、配置语义、审计事件、混合检索算法、RAG 编排流程完全一致。动机：统一技术栈到 Go（mw-bot 生态）、单二进制部署、降低运行时开销、改善并发模型；前端 Vue 与 MySQL/MinIO/模型网关等外部依赖保持不变。

## What Changes

- **新增 Go 后端 API 服务**：替换 `backend/app/**`（FastAPI/uvicorn）为 Go HTTP 服务（`cmd/backend-api/` + `internal/**`），复用相同 API 路径、方法、请求/响应 JSON schema、错误码、审计事件、SSE 事件格式。
- **新增 Go RocketMQ MCP Server**：替换 `mcp-servers/rocketmq/`（FastAPI）为 Go HTTP 服务（`cmd/rocketmq-mcp-server/`），暴露相同 5 个查询工具与 `/health`、`/tools`、`/tools/{name}` 端点。
- **保持数据库 schema 不变**：直接复用 `backend/db/migrations/001_init_schema.sql`、`002_mcp_server_base_url_unique.sql`、`003_add_conversation_summarized_up_to.sql` 三个迁移文件（10 张表：users / knowledge_bases / documents / conversations / messages / message_references / tool_calls / user_memories / mcp_servers / mcp_tools / audit_events）。
- **保持前端不变**：Vue 3 前端通过原有 `/api/*` 契约与 Go 后端通信，前端代码整体复制不改动。
- **保持配置语义不变**：所有环境变量名、默认值、必填规则与 Python `Settings` 完全一致（`DATABASE_URL`、`REDIS_URL`、`STORAGE_BACKEND`、`LLM_BASE_URL`、`LLM_API_KEY`、`EMBEDDING_MODEL`、`EMBEDDING_DIMENSION`、`MINIO_ACCESS_KEY`、`MINIO_SECRET_KEY`、`JWT_SECRET`、`JWT_ALGORITHM=HS256`、`ACCESS_TOKEN_MINUTES=120`、`HYBRID_SEARCH=true`、`RETRIEVE_LIMIT=5`、`RETRIEVE_SCORE_THRESHOLD=0.3`、`INTENT_DETECTION=false`、`MIDDLEWARES=rocketmq,kafka,rabbitmq,pulsar,redis,nacos`、`HISTORY_TOKEN_BUDGET=100000`、`HISTORY_RECENT_RATIO=0.7`、`ENVIRONMENT=local` 等）。
- **保持部署拓扑不变**：向量库由 Python 版 Chroma 内嵌 PersistentClient 改为 Go 版 `chromem-go` 内嵌（进程内，本地文件持久化），仍是单进程无 sidecar。MySQL、Redis、MinIO、模型网关接入方式不变。
- **BREAKING：运行时变更**：后端从 `uvicorn app.main:app` 改为 Go 单二进制 `./backend-api`；MCP Server 同理。配置项 `CHROMA_PERSIST_PATH` 语义保留（chromem-go 持久化目录）。
- **保持混合检索算法**：dense(chromem-go 余弦相似度) + BM25(Go 应用层，`gse` 分词) + RRF 融合（k=60，top_k=`RETRIEVE_LIMIT`=5，分数阈值 `RETRIEVE_SCORE_THRESHOLD`=0.3），BM25 不可用降级纯向量。
- **保持 RAG 编排逻辑**：Python 版 `rag/graph.py` 定义的 LangGraph 图实际未被调用（死代码），真正编排在 `rag/service.py` 的普通方法链 `_prepare()`/`answer()`/`answer_stream()`/`run_tool_loop()` 中；Go 版直接用普通方法链实现，行为对齐。意图检测规则快速通道（"你是谁"/"重新回答"/"详细点"/"简短点" 等精确短句及 "你是…" 开头的身份询问）始终生效，`INTENT_DETECTION=true` 时未命中规则再调 LLM 精判；跨中间件歧义反问（未指定中间件 + ≥2 中间件各命中 ≥3 片段时模板反问，不调 LLM、无引用）；工具循环 ≤ `MAX_TOOL_ITERATIONS`=3 轮；SSE 事件 `meta`/`reasoning`/`delta`/`done` 格式不变；`used_model_inference` 标识（检索空 + 无工具调用时为 true）。
- **保持短期记忆 token 预算与摘要压缩**：`HISTORY_TOKEN_BUDGET=100000`、`HISTORY_RECENT_RATIO=0.7`，超预算时折叠旧消息进 `conversations.summary`（覆盖至 `summarized_up_to`），保留近期窗口。
- **保持审计事件模型与字段**：所有 `event_type` 字符串（`login_success`/`login_failed`/`token_refreshed`/`document_uploaded`/`document_deleted`/`document_indexed`/`document_index_failed`/`chat_asked`/`retrieval`/`tool_called`/`memory_changed` 等）与字段（`actor_user_id`/`actor_role`/`request_id`/`resource_type`/`resource_id`/`action`/`status`/`metadata`）一致。
- **保持错误码体系**：`AUTH_001`（forbidden, 403）、`AUTH_002`（unauthorized, 401）、`BIZ_001`（business, 400）、`BIZ_404`（not_found, 404）、`SYS_001`（system, 500），响应体 `{"detail":{"code":...,"message":...}}`。
- **保持 JWT 结构**：HS256 签名，claims `sub`(user_id 字符串)/`username`/`role`/`exp`，bcrypt 密码哈希（72 字节截断，与 Python 互验兼容）。
- **保持角色权限映射**：`admin` = `{document.upload, document.delete, mcp.server.register, mcp.tool.manage, mcp.tool.call, chat.ask, memory.manage}`；`user` = `{chat.ask, mcp.tool.call, memory.manage}`。
- **保持分块参数**：`RecursiveCharacterTextSplitter` 等价 Go 实现，`chunk_size=800`、`overlap=120`、分隔符优先级 `["\n\n","\n","。","；","！","？",". ","! ","? "," ",""]`、`keep_separator=true`、`strip_whitespace=true`。
- **保持中间件识别算法**：URL 剔除（`https?://\S+`）+ 归一化（去非字母数字）+ 子串匹配 + 编辑距离 ≤1 容错（`match_candidate` 用于反问澄清回复匹配）。

## Impact

- **受影响的 specs**（来自 `/Users/wcf/python-project/ai-worker/openspec/specs/`）：
  - `project-runtime`：运行时从 Python 改为 Go 单二进制；部署拓扑保持单进程（向量库 chromem-go 内嵌）
  - `local-auth`：实现语言变更（FastAPI → Go net/http），契约不变
  - `knowledge-ingestion`：解析库替换（pypdf→`github.com/ledongthuc/pdf`，python-docx→手写 archive/zip+encoding/xml，markdown-it-py→`github.com/yuin/goldmark`），分块与打标算法不变
  - `rag-chat`：Python 版 `rag/graph.py` 的 LangGraph 图为死代码（未调用），真正编排在 `rag/service.py` 普通方法链；Go 版直接用普通方法链实现，无状态机替换
  - `mcp-tooling`：实现语言变更，契约不变
  - `audit-observability`：实现语言变更，事件模型与字段不变
- **受影响的代码**：
  - 完全重写：`backend/app/**`（Python → Go `internal/**`）、`mcp-servers/rocketmq/**`（Python → Go `cmd/rocketmq-mcp-server/`）
  - 复用：`backend/db/migrations/*.sql`（复制到 `db/migrations/`）、`frontend/**`（整体复制不改动）
  - 新增：Go 项目骨架（`go.mod`、`cmd/`、`internal/`、`Dockerfile`、`deploy/docker-compose.yml` 调整）

## ADDED Requirements

### Requirement: Go 后端必须暴露与 Python 后端一致的 HTTP API
系统 MUST 在 Go 后端中实现所有原有 HTTP 端点，路径、方法、请求体、响应体、状态码与 Python 实现完全一致，前端 Vue 应用无需任何改动即可切换到 Go 后端。

#### Scenario: 前端切换到 Go 后端
- **WHEN** 前端将 `VITE_API_BASE_URL` 指向 Go 后端
- **THEN** 登录、上传、问答（含流式）、MCP 管理、记忆管理等所有流程正常工作

#### Scenario: 健康检查
- **WHEN** 调用 `GET /health`
- **THEN** 返回 `{"status":"ok","service":"backend-api"}`，HTTP 200

### Requirement: Go 项目结构必须遵循标准布局
系统 MUST 使用 `cmd/backend-api/main.go`、`cmd/rocketmq-mcp-server/main.go`、`internal/{auth,chat,knowledge,mcp_gateway,rag,ingestion,audit,common}/` 目录结构，模块边界与 Python `app/<module>/` 一一对应。

#### Scenario: 目录结构校验
- **WHEN** 开发者查看 Go 项目根
- **THEN** 存在 `cmd/`、`internal/`、`db/migrations/`、`deploy/`、`frontend/`、`go.mod`、`go.sum`、`Dockerfile`

### Requirement: 必须使用 Go 标准库与少量必要第三方库
系统 MUST 优先使用 Go 标准库（`net/http`、`database/sql`、`encoding/json`、`crypto/*`、`archive/zip`、`encoding/xml`、`regexp`），数据库访问使用 `database/sql` + `github.com/go-sql-driver/mysql`（不引入 GORM/Ent 等重型 ORM），HTTP 路由使用 `net/http` + 轻量路由器（如 `github.com/go-chi/chi/v5`），JWT 使用 `github.com/golang-jwt/jwt/v5`，bcrypt 使用 `golang.org/x/crypto/bcrypt`，MinIO 使用 `github.com/minio/minio-go/v7`，jieba 分词使用 `github.com/yanyiwu/gojieba`，Markdown 解析使用 `github.com/yuin/goldmark`，PDF 解析使用 `github.com/ledongthuc/pdf`，DOCX 解析使用 `archive/zip` + `encoding/xml` 手写最小提取器。

#### Scenario: 依赖最小化
- **WHEN** 查看 `go.mod`
- **THEN** 只包含上述必要依赖，不引入 ORM、Web 框架（gin/echo/fiber）、LangGraph 等价物等重型库

### Requirement: Go 后端必须使用 chromem-go 内置向量库
系统 MUST 使用 `github.com/philippgille/chromem-go` 作为进程内嵌向量库（替代 Python 版 Chroma PersistentClient），通过 `CHROMA_PERSIST_PATH`（默认 `data/chroma`）指定本地持久化目录。collection 创建、向量 upsert、相似度检索（含 metadata where 过滤）、按 `document_id` 删除均通过 chromem-go API 完成。BM25 索引由 Go 应用层在启动时从 chromem-go 全量拉取重建（复用同一 embedding，不重新 embedding）。

#### Scenario: 启动时重建 BM25
- **WHEN** Go 后端启动且 chromem-go 持久化目录可读写
- **THEN** 后端从 chromem-go 拉取全部 chunk 文本与 metadata，构建内存 BM25Index

#### Scenario: 持久化目录不可写
- **WHEN** `CHROMA_PERSIST_PATH` 指向的目录不可读写
- **THEN** 后端启动失败并在日志中明确报告依赖缺失

### Requirement: 必须实现与 Python 互验兼容的密码哈希与 JWT
系统 MUST 使用 `golang.org/x/crypto/bcrypt`（默认 cost）进行密码哈希，明文密码编码为 UTF-8 后截断至 72 字节再哈希；JWT 使用 `HS256` 算法，claims 包含 `sub`(user_id 字符串)、`username`、`role`、`exp`，密钥来自 `JWT_SECRET`。同一密码哈希值在 Python bcrypt 与 Go bcrypt 之间可互验。

#### Scenario: 跨语言密码互验
- **WHEN** Python 生成的 bcrypt 哈希存入 DB，Go 后端使用 `bcrypt.CompareHashAndPassword` 校验
- **THEN** 校验通过

#### Scenario: JWT 跨语言解析
- **WHEN** Python 签发的 JWT 由 Go 后端解析
- **THEN** claims 一致

## MODIFIED Requirements

### Requirement: 本地部署必须使用 Docker Compose（修订）
系统 MUST 提供 Docker Compose 配置，启动以下服务：
- `backend-api`：Go 单二进制容器，监听 8080，挂载 `data/` 卷用于 chromem-go 持久化与本地文件存储
- `rocketmq-mcp-server`：Go 单二进制容器，监听 10914
- `frontend`：原 Vue 镜像不变，监听 80→5173，依赖 backend-api

MySQL 与 Redis 假定在宿主机运行，容器通过 `host.docker.internal` 访问。向量库 chromem-go 内嵌于 backend-api 进程，无独立 sidecar。

#### Scenario: 校验 Compose 配置
- **WHEN** 执行 `docker compose -f deploy/docker-compose.yml config`
- **THEN** 配置解析成功，包含 backend-api、rocketmq-mcp-server、frontend 三个服务

#### Scenario: 向量数据持久化
- **WHEN** 执行 `docker compose down`（不带 `-v`）
- **THEN** `data` 卷保留，重启后 chromem-go 从持久化文件加载向量数据

### Requirement: 配置必须通过统一 Settings 读取（修订）
后端 MUST 使用 Go 结构体 + `os.Getenv`/`envconfig` 读取所有配置项，字段名、环境变量名、默认值、必填规则与 Python `Settings` 完全一致。`CHROMA_PERSIST_PATH`（默认 `data/chroma`）保留为 chromem-go 持久化目录。其余配置项（`DATABASE_URL`/`REDIS_URL`/`STORAGE_BACKEND`/`LOCAL_STORAGE_ROOT`/`HYBRID_SEARCH`/`RETRIEVE_LIMIT`/`RETRIEVE_SCORE_THRESHOLD`/`INTENT_DETECTION`/`MIDDLEWARES`/`HISTORY_TOKEN_BUDGET`/`HISTORY_RECENT_RATIO`/`LLM_BASE_URL`/`LLM_API_KEY`/`LLM_MODEL`/`EMBEDDING_MODEL`/`EMBEDDING_DIMENSION`/`EMBEDDING_BASE_URL`/`EMBEDDING_API_KEY`/`MINIO_*`/`JWT_SECRET`/`JWT_ALGORITHM`/`ACCESS_TOKEN_MINUTES`/`ENVIRONMENT`）保持原语义。

#### Scenario: 本地默认配置
- **WHEN** 未提供生产环境变量
- **THEN** 后端使用本地默认值（`STORAGE_BACKEND=local`、`HYBRID_SEARCH=true`、`ENVIRONMENT=local`、`CHROMA_PERSIST_PATH=data/chroma` 等），敏感项（`DATABASE_URL`/`LLM_API_KEY`/`MINIO_ACCESS_KEY`/`MINIO_SECRET_KEY`/`JWT_SECRET`）缺失时启动报错退出

#### Scenario: 配置语义对齐
- **WHEN** 使用原 `deploy/env.example` 配置启动 Go 后端
- **THEN** 后端行为与 Python 后端一致

### Requirement: 项目运行时（修订）
后端 MUST 编译为单二进制 `backend-api`，启动命令 `./backend-api`，监听端口由 `APP_PORT`（默认 8080）控制。MCP Server 同理编译为 `rocketmq-mcp-server`。日志输出到 stdout，使用结构化 JSON 格式（含 `request_id`、`timestamp`、`level`、`message`、`fields`）。启动时执行索引恢复（`indexing`→`pending` 重置 + 全部 `pending` 重投递）与 BM25 预热，best-effort 不阻断启动。

#### Scenario: 本地启动
- **WHEN** 执行 `go run ./cmd/backend-api`
- **THEN** 服务监听 8080，启动时恢复未完成索引任务并预热 BM25

## REMOVED Requirements

### Requirement: 后端使用 Python + FastAPI + uvicorn
**Reason**: 重写为 Go。
**Migration**: 原 `backend/` 目录保留为参考（不删除）；新代码在 `cmd/backend-api/` + `internal/`。Python 依赖（`pyproject.toml`、`uv.lock`）不再使用，前端原 `deploy/docker-compose.yml` 中的 backend-api 构建上下文改为 Go Dockerfile。

### Requirement: Chroma 内嵌于 backend 进程（PersistentClient）
**Reason**: Go 版改用 `chromem-go` 内嵌向量库（API 与 Chroma 类似，纯 Go 无 CGO）。
**Migration**: 配置项 `CHROMA_PERSIST_PATH` 语义保留（指向 chromem-go 持久化目录）；向量数据格式不兼容，首次启动需重新索引（由 `reindex_all` 等价 Go 命令完成），后续重启从 chromem-go 持久化文件加载并重建 BM25（不重新 embedding）。
