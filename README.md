# 企业内部智能问答系统（mw-bot）

基于 RAG 的企业内部中间件知识问答系统：知识库文档上传 → 异步解析分块 → 向量检索 → 引用问答，集成 MCP 工具网关（RocketMQ 运维查询）与用户长期记忆。

## 目录结构

```
cmd/                        Go 入口
  backend-api/              后端 API 服务
  rocketmq-mcp-server/      RocketMQ MCP Server（对应 Python mcp-servers/rocketmq/）
internal/                   Go 内部包
  auth/                     认证（登录/刷新/JWT）
  chat/                     聊天（问答/会话/记忆）
  knowledge/                知识库（Markdown 文档上传/在线新建与编辑/列表/删除，内容入库）
  mcp_gateway/              MCP 工具网关
  rag/                      RAG 编排（状态机替代 LangGraph）
  ingestion/                文档摄入（解析/分块/异步索引）
  audit/                    审计
  common/                   通用基础设施（配置/DB/错误/安全/日志/存储/向量库/BM25/模型Provider）
db/migrations/              SQL 迁移脚本
deploy/                     Docker Compose 编排
frontend/                   Vue 3 前端（
```

## 技术栈

- 后端：Go 1.22+、`net/http` 标准库路由、`database/sql` + `go-sql-driver/mysql`、`golang-jwt`、`golang.org/x/crypto/bcrypt`、`go-ego/gse`（中文分词）、`ledongthuc/pdf`（PDF）、`minio-go/v7`（MinIO）
- 向量库：chromem-go（进程内持久化，无需独立部署）
- 检索：dense(chromem-go) + BM25(应用层，gse 中文分词) + RRF 融合
- 前端：Vue 3、TypeScript、Vite、Element Plus、Pinia（未改动）
- 存储：MySQL 8（业务数据）、chromem-go 内嵌向量库、本地文件系统 / MinIO（文档）
- 模型：企业内部 OpenAI 兼容网关（LLM + Embedding）

## 前置依赖

| 依赖 | 说明 |
|---|---|
| MySQL 8 | 业务数据库，宿主机或可访问地址运行 |
| 模型网关 | OpenAI 兼容接口，提供 chat completions 与 embeddings（embedding 网关未单独配置时复用 LLM 网关） |
| Go 1.22+ | 后端编译 |
| Node 22+ | 前端构建 |

## 数据库初始化

迁移约定：`001_init_schema.sql` 始终保持**最新全量结构**；`002+` 为增量迁移，
供已有数据库升级用。增量迁移均做了幂等处理（`information_schema` 守卫 + 动态 SQL，
兼容 MySQL 8），已包含的变更会输出 `skip` 并跳过，重复执行不报错。

```bash
# 1. 全新数据库：只需 001 + 002
#    （003/004 的列已并入 001；即使按序全跑 001~004 也安全，003/004 会自动跳过）
mysql -h 127.0.0.1 -uroot -p < db/migrations/001_init_schema.sql
mysql -h 127.0.0.1 -uroot -p < db/migrations/002_mcp_server_base_url_unique.sql

# 2. 已有数据库升级：按序补跑尚未执行的迁移（重复执行自动跳过）
#    mysql -h 127.0.0.1 -uroot -p ai_qa < db/migrations/00N_xxx.sql

# 3. 建应用账号
mysql -h 127.0.0.1 -uroot -p -e "CREATE USER IF NOT EXISTS 'ai_qa'@'%' IDENTIFIED BY 'ai_qa'; GRANT ALL ON ai_qa.* TO 'ai_qa'@'%'; FLUSH PRIVILEGES;"

# 4. 创建首个管理员（backend-api 自动加载当前目录 .env，无需手动导出环境变量）
#    用法：go run ./cmd/backend-api [-config <path>] -create-admin <username> <password> <role>
go run ./cmd/backend-api -create-admin admin admin123 admin
```

> **注意**：001 内含 `CREATE DATABASE ai_qa; USE ai_qa;`，固定作用于 `ai_qa` 库，
> 与命令行是否指定其他库无关。若要改库名，需同步修改 001 开头两行与
> `DATABASE_URL`。另外 `002` 的唯一索引未并入 001（MySQL 不支持幂等建索引，
> 并入后全新库按序执行会报 Duplicate key name），故新库必须额外执行 002。

## 本地启动

> **配置加载**：backend-api 启动时自动加载 KEY=VALUE 格式配置文件，优先 `-config <path>` 指定的路径，未指定时默认读当前目录 `.env`。
> 已存在的环境变量不会被文件值覆盖（compose/k8s 注入环境变量场景不受影响）；默认 `.env` 不存在时按纯环境变量模式启动。

### 方式一：分别启动（开发）

```bash
# 0. 准备 .env（首次）；backend-api 启动时自动加载，无需手动 source
cp deploy/env.example .env   # 按环境修改 DATABASE_URL/LLM_API_KEY/MINIO/JWT_SECRET 等

# 1. 后端（默认 8080，向量库 chromem-go 内嵌进程，无需单独启动）
go run ./cmd/backend-api

# 2. 前端（新终端；默认 5173，dev server 代理 /api 到 8080）
cd frontend && npm install && npm run dev

# 3. RocketMQ MCP Server（可选；默认 10914）
go run ./cmd/rocketmq-mcp-server
```

启动顺序：MySQL -> 后端 -> 前端。embedding 始终走真实模型网关；EMBEDDING_BASE_URL/EMBEDDING_API_KEY 未配置时复用 LLM_BASE_URL/LLM_API_KEY。

### 方式二：Docker Compose

MySQL 与 Redis 假定已在宿主机运行，容器通过 `host.docker.internal` 访问：

```bash
cp deploy/env.example .env  # 按环境修改
docker compose -f deploy/docker-compose.yml --env-file .env up -d --build
```

## 配置

后端配置由环境变量读取，启动时会先加载 KEY=VALUE 格式配置文件作为基础值：优先 `-config <path>` 指定的路径，未指定时默认当前目录 `.env`；已存在的环境变量优先于文件值（docker compose 可继续用 `--env-file` 或直接注入环境变量）。**所有含密码/key 的项均为必填，无默认值，未配置时启动报错。**

| 变量 | 默认值 | 说明 |
|---|---|---|
| `DATABASE_URL` | **必填** | MySQL 连接串（`mysql://user:pass@host:3306/db`） |
| `REDIS_URL` | `""` | Redis 连接串（预留） |
| `STORAGE_BACKEND` | `local` | 文件存储：`local` / `minio` |
| `LOCAL_STORAGE_ROOT` | `data/uploads` | 本地存储根目录 |
| `CHROMA_PERSIST_PATH` | `data/chroma` | chromem-go 持久化目录（进程内向量库） |
| `HYBRID_SEARCH` | `true` | 是否启用混合检索 |
| `ENVIRONMENT` | `local` | 运行环境标识（embedding 始终用真实网关，不因 local 降级） |
| `LLM_BASE_URL` | **必填** | 模型网关地址 |
| `LLM_API_KEY` | **必填** | 网关凭证 |
| `LLM_MODEL` | `qwen-plus` | 模型名称 |
| `EMBEDDING_MODEL` / `EMBEDDING_DIMENSION` | `text-embedding-v4` / `1024` | embedding 模型与维度 |
| `MINIO_ACCESS_KEY` | **必填** | MinIO 访问密钥 |
| `MINIO_SECRET_KEY` | **必填** | MinIO 密钥 |
| `JWT_SECRET` | **必填** | JWT 签名密钥 |
| `JWT_ALGORITHM` / `ACCESS_TOKEN_MINUTES` | `HS256` / `120` | JWT 算法与过期时间 |

## 技术边界（MVP）

- **单进程低并发**：BM25 索引在应用进程内，启动时从 Chroma 全量重建。
- **无独立 worker**：文档索引在 backend-api 进程内 goroutine 工作池异步执行（状态持久化到 MySQL，支持启动恢复）。
- **无独立 refresh token**：以仍有效的 access token 调用 `/api/auth/refresh` 续期。
- **应用层混合检索**：Chroma 无原生混合检索，由进程内 BM25Index 实现 dense+BM25 RRF 融合。
- **MCP 工具策略**：启用/角色/schema/二次确认/超时/限流，结果标准化不泄露凭证与堆栈。
- **长期记忆提取时机**：每轮问答后由 LLM 决策提取用户偏好；模型网关兜底回复与跨中间件歧义反问为固定模板，跳过提取。提取失败不影响回答，但会写 `memory_extraction` failed 审计，并在响应（`memory_extraction_failed`）与前端轻提示中暴露。
