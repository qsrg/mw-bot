# Tasks

- [x] Task 1: 初始化 Go 项目骨架与资源复用
  - [ ] 创建 `go.mod`（module 名 `mw-bot`，Go 版本 1.22+）与基础依赖
  - [ ] 创建目录结构 `cmd/backend-api/`、`cmd/rocketmq-mcp-server/`、`internal/{auth,chat,knowledge,mcp_gateway,rag,ingestion,audit,common}/`、`db/migrations/`、`deploy/`
  - [ ] 从 Python 项目复制 `backend/db/migrations/*.sql` 到 `db/migrations/`
  - [ ] 从 Python 项目整体复制 `frontend/`（保持不变）
  - [ ] 创建 `.gitignore`、`README.md`（中文，描述启动方式与配置）

- [x] Task 2: 实现通用基础设施（`internal/common/`）
  - [ ] `config.go`：`Settings` 结构体 + `os.Getenv` 加载，字段对齐 Python `config.py`（含新增 `CHROMA_BASE_URL`，移除 `CHROMA_PERSIST_PATH`），敏感项缺失启动报错
  - [ ] `database.go`：`database/sql` + `github.com/go-sql-driver/mysql`，连接池 + `GetSession` 等价（`*sql.DB` 注入）
  - [ ] `errors.go`：`AppError` 类型 + 错误码常量（`AUTH_001`/`AUTH_002`/`BIZ_001`/`BIZ_404`/`SYS_001`）+ 构造函数（`Forbidden`/`Unauthorized`/`BusinessError`/`NotFound`/`SystemError`）+ 统一响应 `{"detail":{"code":...,"message":...}}`
  - [ ] `security.go`：bcrypt 哈希/校验（72 字节截断）+ JWT 签发/解析（HS256，claims `sub`/`username`/`role`/`exp`）+ 角色权限映射（`ROLE_PERMISSIONS`）
  - [ ] `logging.go`：结构化 JSON 日志（`request_id`/`timestamp`/`level`/`message`/`fields`）+ `RequestContextMiddleware`（生成 `request_id` 并注入 context）
  - [ ] `request_context.go`：`context.Context` 透传 `request_id`（`WithRequestID`/`RequestIDFromContext`）
  - [ ] `tokens.go`：字符启发式 token 估算（对齐 Python `tokens.py`）
  - [ ] `middleware.go`：中间件识别（URL 剔除 + 归一化 + 子串匹配 + 编辑距离 ≤1 容错），函数 `MiddlewareList`/`DetectMiddlewares`/`MatchCandidate`

- [ ] Task 3: 实现文件存储抽象（`internal/common/storage.go`）
  - [ ] `FileStorage` 接口（`Save(ctx, objectKey, reader) error` / `Open(ctx, objectKey) (io.ReadCloser, error)` / `Delete(ctx, objectKey) error`）
  - [ ] `LocalFileStorage` 实现（`local_storage_root` + object_key = uuid，路径拼接与 Python 一致）
  - [ ] `MinioFileStorage` 实现（`github.com/minio/minio-go/v7`，bucket = `minio_bucket`）
  - [ ] 工厂函数 `NewFileStorage(settings)` 按 `STORAGE_BACKEND` 选择后端

- [ ] Task 4: 实现向量库适配器（`internal/common/vector_store.go`）
  - [ ] `VectorStore` 接口（`Add`/`Search`/`DeleteByDocumentID`/`Warmup`/`GetAll`）
  - [ ] `ChromemVectorStore` 实现：封装 `github.com/philippgille/chromem-go`，通过 `CHROMA_PERSIST_PATH` 持久化，支持 collection 创建、向量 upsert、相似度查询含 metadata where 过滤、按 metadata 删除
  - [ ] `InMemoryVectorStore` 实现（测试用）
  - [ ] 单例工厂 + 启动预热（从 chromem-go 拉全量构建 BM25）

- [ ] Task 5: 实现 BM25 索引（`internal/common/bm25_index.go`）
  - [ ] `BM25Index` 类型（k1=1.5, b=0.75 标准参数）
  - [ ] `github.com/go-ego/gse` 分词（纯 Go jieba 端口，`seg.LoadDict("zh")` 加载中文词典）
  - [ ] `AddDocuments` / `Search(query, topK)` 方法
  - [ ] 启动时从 chromem-go 重建逻辑（`Warmup` 调用）

- [ ] Task 6: 实现混合检索 RRF 融合（`internal/common/hybrid_search.go`）
  - [ ] RRF 融合算法（k=60，top_k=`RETRIEVE_LIMIT`=5）
  - [ ] 分数阈值过滤（`RETRIEVE_SCORE_THRESHOLD`=0.3，融合前过滤）
  - [ ] BM25 不可用降级为纯向量检索
  - [ ] 按 `mw_<name>=true` metadata 过滤

- [ ] Task 7: 实现模型 Provider（`internal/common/model_provider.go`）
  - [ ] `EmbeddingProvider` 接口 + `HttpEmbeddingProvider`（POST `{base_url}/embeddings`，OpenAI 兼容请求/响应）
  - [ ] `LLMProvider` 接口 + `HttpLLMProvider`（POST `{base_url}/chat/completions`，支持流式 SSE 解析）
  - [ ] `ENVIRONMENT=local` 时 `FakeEmbeddingProvider`（伪向量，确定性 hash→向量）
  - [ ] `EMBEDDING_BASE_URL` 为空时回退到 `LLM_BASE_URL`/`LLM_API_KEY`

- [ ] Task 8: 实现 Auth 模块（`internal/auth/`）
  - [ ] `models.go`：`User` 模型 + `users` 表映射（id/uuid/username/password_hash/role/status/created_at/updated_at）
  - [ ] `schemas.go`：`LoginRequest`/`LoginResponse`/`UserInfo`
  - [ ] `dependencies.go`：`IdentityContext` + `CurrentUser` middleware + `RequirePermission(perm)` middleware
  - [ ] `service.go`：`AuthService`（`CreateUser`/`Authenticate`/`IssueToken`/`VerifyToken`）
  - [ ] `router.go`：`POST /api/auth/login` / `GET /api/auth/me` / `POST /api/auth/refresh`
  - [ ] 登录成功/失败、刷新写入审计事件（`login_success`/`login_failed`/`token_refreshed`）

- [ ] Task 9: 实现 Knowledge 模块（`internal/knowledge/`）
  - [ ] `models.go`：`Document`/`KnowledgeBase` 模型
  - [ ] `schemas.go`：`DocumentResponse`
  - [ ] `service.go`：`KnowledgeService`（`Upload`/`List`/`Delete`）
  - [ ] `router.go`：`POST /api/knowledge/documents`（multipart, `document.upload`）/ `GET /api/knowledge/documents`（`document.upload`）/ `DELETE /api/knowledge/documents/{id}`（`document.delete`）
  - [ ] 上传：保存文件 → 创建 `pending` 文档记录 → 投递后台索引任务 → 写 `document_uploaded` 审计
  - [ ] 删除：清理向量库 chunk → 删除存储文件 → 删除 DB 记录 → 写 `document_deleted` 审计

- [ ] Task 10: 实现文档解析器（`internal/ingestion/parsers.go`）
  - [ ] `ParsePDF`（`github.com/ledongthuc/pdf`）→ 提取文本 + 页码
  - [ ] `ParseDOCX`（`archive/zip` + `encoding/xml` 手写）→ 提取文本 + 标题路径
  - [ ] `ParseMarkdown`（`github.com/yuin/goldmark`）→ 提取文本 + 标题路径
  - [ ] 统一返回 `ParsedDocument{Text, Metadata, Sections}`

- [ ] Task 11: 实现分块策略（`internal/ingestion/chunking.go`）
  - [ ] `RecursiveCharacterTextSplitter` 等价 Go 实现（chunk_size=800, overlap=120, 分隔符优先级 `["\n\n","\n","。","；","！","？",". ","! ","? "," ",""]`, keep_separator=true, strip_whitespace=true）
  - [ ] 保留页码/标题路径元数据
  - [ ] `chunk_text(parsed, opts) []DocumentChunk`

- [ ] Task 12: 实现摄入任务（`internal/ingestion/tasks.go`）
  - [ ] goroutine 工作池（2 workers，等价 Python `ThreadPoolExecutor(max_workers=2)`）
  - [ ] 状态机 `pending`→`indexing`→`indexed`|`failed`
  - [ ] 中间件打标：`DetectMiddlewares(全文+文件名)` → 每个 chunk 写 `mw_<name>=True` 元数据
  - [ ] 启动恢复：`indexing`→`pending` 重置 + 全部 `pending` 重投递
  - [ ] 幂等：写入前 `DeleteByDocumentID` 清旧向量再 upsert
  - [ ] 失败重试（最多 3 次，间隔 30s），失败写 `document_index_failed` 审计
  - [ ] 成功写 `document_indexed` 审计
  - [ ] `reindex_all` 等价 Go 命令（重新索引所有 `indexed` 文档，补打 `mw_*` 标签）

- [ ] Task 13: 实现 MCP Gateway 模块（`internal/mcp_gateway/`）
  - [ ] `models.go`：`McpServer`/`McpTool` 模型
  - [ ] `schemas.go`：`ServerRegisterRequest`/`ServerUpdateRequest`/`ServerResponse`/`ServerListItem`/`ToolResponse`/`ToolPolicyUpdate`/`ToolInvokeRequest`/`ToolInvokeResponse`
  - [ ] `service.go`：`McpGatewayService`（`RegisterServer`/`UpdateServer`/`DeleteServer`/`RefreshTools`/`InvokeTool`）
  - [ ] 工具调用流程：检查启用 → 检查角色 → schema 校验 → 检查 `confirmed`（`requires_approval=true` 时）→ 限流（固定窗口内存桶，解析 `rate_limit` 如 `60/minute`）→ 超时（`timeout_seconds`）→ 调用 MCP Server `POST /tools/{name}` → 结果大小限制（`result_size_limit`=8192 字节）→ 标准化（不泄露凭证/堆栈）→ 写 `tool_calls` 表 + `tool_called` 审计
  - [ ] `router.go`：`POST /api/mcp/servers` / `GET /api/mcp/servers` / `PATCH /api/mcp/servers/{id}` / `DELETE /api/mcp/servers/{id}` / `POST /api/mcp/servers/{id}/refresh` / `GET /api/mcp/tools` / `PATCH /api/mcp/tools/{id}` / `POST /api/mcp/tools/{id}/invoke`

- [ ] Task 14: 实现 Chat 模块（`internal/chat/`）
  - [ ] `models.go`：`Conversation`/`Message`/`MessageReference`/`UserMemory`/`ToolCall` 模型
  - [ ] `schemas.go`：`ChatRequest`/`ChatResponse`/`CitationItem`/`ConversationSummary`/`MessageItem`/`MemoryItem`
  - [ ] `service.go`：`ChatService`（`Ask`/`AskStream`/`ListConversations`/`DeleteConversation`/`ListMessages`）+ `MemoryService`（`List`/`Delete`/`SetEnabled`）
  - [ ] `router.go`：`POST /api/chat/messages` / `POST /api/chat/messages/stream`（SSE）/ `GET /api/chat/conversations` / `DELETE /api/chat/conversations/{id}` / `GET /api/chat/conversations/{id}/messages` / `GET /api/chat/memories` / `DELETE /api/chat/memories/{id}` / `PATCH /api/chat/memories/{id}`
  - [ ] 短期记忆压缩：超 `HISTORY_TOKEN_BUDGET` 时折叠旧消息进 `summary`（LLM 摘要），更新 `summarized_up_to`
  - [ ] 长期记忆：注入启用记忆到 prompt（`memory_block` 标注）
  - [ ] 删除会话硬删除（会话+消息+引用），写 `memory_changed` 审计（记忆变更）

- [ ] Task 15: 实现 RAG 编排（`internal/rag/`）
  - [ ] `service.go`：`RagService`（`Ask`/`AskStream`）
  - [ ] `graph.go`：手写状态机，节点链 `query_rewrite` → `retrieve_knowledge` → `decide_tool` → `call_mcp_if_needed` → `generate_answer` → `persist_trace`
  - [ ] `intent.go`：意图检测，规则快速通道（"你是谁"/"重新回答"/"详细点"/"简短点" 等精确短句 + "你是…" 开头的身份/能力询问）始终生效不调 LLM；`INTENT_DETECTION=true` 时未命中规则再调 LLM 精判（knowledge/followup/chat）；`false` 时默认按 knowledge 处理
  - [ ] `retrieval.go`：混合检索（dense + BM25 RRF）+ 跨中间件逐中间件检测（每个中间件独立过滤检索，复用同一 embedding，命中 ≥3 片段视为该中间件相关；≥2 个中间件相关时模板反问，不调 LLM、无引用）
  - [ ] `tool_loop.go`：工具循环 ≤ `MAX_TOOL_ITERATIONS` 轮，通过 `mcp_gateway` 调用（不直接调 MCP Server）
  - [ ] `streaming.go`：SSE 事件生成（`meta`/`reasoning`/`delta`/`done`），模型网关流式失败降级为兜底文案
  - [ ] `prompts.go`：系统提示、意图判断、工具决策、摘要生成 prompt 模板（对齐 Python）
  - [ ] `used_model_inference` 标志：检索空 + 无工具调用时为 true

- [ ] Task 16: 实现 Audit 模块（`internal/audit/`）
  - [ ] `models.go`：`AuditEvent` 模型（`audit_events` 表）
  - [ ] `service.go`：`AuditService.RecordEvent(eventType, actorUserID, actorRole, requestID, resourceType, resourceID, action, status, metadata)`
  - [ ] 在所有原 Python 写审计事件的位置同步写入：登录/刷新/上传/删除/索引成功/索引失败/问答/检索/工具调用/记忆变更

- [ ] Task 17: 实现 RocketMQ MCP Server（`cmd/rocketmq-mcp-server/`）
  - [ ] Go HTTP 服务，监听 10914
  - [ ] `GET /health` → `{"status":"ok","real_mode":bool,"namesrv":string|null}`
  - [ ] `GET /tools` → 5 个工具 schema 列表（`rocketmq_list_clusters`/`rocketmq_get_broker_status`/`rocketmq_get_topic_config`/`rocketmq_get_consumer_group_status`/`rocketmq_get_consumer_lag`，含 name/read_only/description/input_schema）
  - [ ] `POST /tools/{name}` → 调用 handler，未配置 `ROCKETMQ_NAMESRV` 时返回 mock 数据（与 Python mock 结构一致）
  - [ ] 配置 `ROCKETMQ_NAMESRV` 环境变量（默认 `localhost:9876`）

- [ ] Task 18: 实现后端 main 与启动流程（`cmd/backend-api/main.go`）
  - [ ] 加载配置 → 初始化 DB → 执行 schema 校验（可选执行迁移）→ 初始化存储/向量库/模型 Provider/BM25 → 注册中间件 → 注册路由（auth/knowledge/chat/mcp_gateway + `/health`）→ 启动 HTTP 服务
  - [ ] 启动时恢复未完成索引任务（best-effort）
  - [ ] 启动时预热 BM25（best-effort）
  - [ ] `GET /health` → `{"status":"ok","service":"backend-api"}`

- [ ] Task 19: Dockerfile 与 Compose 编排
  - [ ] `Dockerfile.backend`（多阶段：`golang:1.22-alpine` 编译 → `alpine` 运行，暴露 8080，挂载 `data/` 卷）
  - [ ] `Dockerfile.mcp-server`（同上，暴露 10914）
  - [ ] `deploy/docker-compose.yml`：`backend-api`、`rocketmq-mcp-server`、`frontend`（复用原 Dockerfile），无 chroma sidecar
  - [ ] `deploy/env.example` 保持 `CHROMA_PERSIST_PATH=data/chroma`（chromem-go 持久化目录）
  - [ ] backend-api 挂载 `data/` 卷（chromem-go 持久化 + 本地文件存储）

- [ ] Task 20: 测试
  - [ ] 单元测试：`errors`、`security`（bcrypt 互验、JWT 签发解析）、`tokens`、`middleware` 识别（含 URL 剔除、归一化、编辑距离）、`bm25`、`hybrid_search`（RRF 融合 + 阈值过滤 + 降级）、`chunking`（分隔符优先级 + overlap）
  - [ ] 集成测试：auth 登录刷新流程、knowledge 上传+异步索引、chat 同步/流式问答、mcp server 注册+工具刷新+调用
  - [ ] 端到端：`docker compose up` 后前端登录→上传文档→等待索引完成→问答→展示引用 全流程

# Task Dependencies
- Task 2（common）阻塞 Tasks 3-16
- Task 3（storage）阻塞 Task 9, 12
- Task 4（vector）阻塞 Task 12, 15
- Task 5（bm25）阻塞 Task 15
- Task 6（hybrid_search）阻塞 Task 15
- Task 7（model_provider）阻塞 Task 12, 15
- Task 8（auth）阻塞 Tasks 9, 13, 14
- Task 9（knowledge）阻塞 Task 12
- Tasks 10, 11（parsers, chunking）阻塞 Task 12
- Task 12（ingestion）阻塞 Task 18
- Task 13（mcp_gateway）阻塞 Task 15
- Task 14（chat）阻塞 Tasks 15, 18
- Task 15（rag）阻塞 Task 18
- Task 16（audit）阻塞 Tasks 8, 9, 13, 14, 15
- Task 17（rocketmq mcp）独立
- Task 18（main）依赖所有后端模块
- Task 19（deploy）依赖 Tasks 17, 18
- Task 20（tests）依赖所有

# 可并行任务
- Task 17（rocketmq mcp）可与 Tasks 2-16 并行
- Task 1 的子任务（复制 migrations、复制 frontend）可与 Task 2 并行
- Tasks 10, 11（parsers, chunking）可与 Tasks 8, 13 并行（均依赖 Task 2）
