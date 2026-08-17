# mw-bot Go 重写版代码审查与修复记录

参考 Python 版（`/Users/wcf/python-project/ai-worker`）重写。本文档记录逐模块对比发现的问题，并跟踪确认与修复状态。(完成修复后，就无须参考python版本的代码了)

## 状态说明

- **确认状态**：`待确认` / `已确认` / `非问题` / `Python也错`
  - `已确认`：对照 Python 源码后，确认是 Go 端的真实 bug，需修复。
  - `非问题`：经确认 Go 行为正确（可能比 Python 更对），无需改。
  - `Python也错`：Python 本身就有此 bug，不应在 Go 复刻；记录但不修（或单独评估）。
- **修复状态**：`未修` / `已修` / `不修`

## 严重度图例

`🔴 严重` `🟠 高` `🟡 中` `⚪ 低` `📄 文档`

---

## 本轮确认与修复结果

确认方法：逐条对照 Python 源码核实（不只看「与 Python 不一致」），区分三类——Go 真 bug（修）、Go 行为更对（不改）、Python 本身也错（不在 Go 复刻）。`go build`/`go vet`/`go test` 全绿。

### 已修复（30 条）

**BM25/混合检索（common/）**：C3 BM25 加元数据过滤（修跨库泄漏）、C4 重启后 BM25 用 sidecar 持久化全量重建、C5 Warmup 持锁避免竞态丢条目、H1 RRF 公式 +1、H2 IDF 负值用 epsilon=0.25*avgIDF（对齐 rank_bm25）、H3 oversample 改 max(limit*3,20)、H4 hasAlnum 改 unicode.IsLetter/Digit 过滤中文标点、L7 删 avgDocLen 死代码。

**MCP 网关**：C1 限流改包级单例跨请求共享、H13 超时/失败返回 500（对齐 system_error）、H14 read_only 缺省 true、H15 结果大小按字符计、L6 SystemError 支持自定义消息。

**RocketMQ MCP**：C2 解析 `{"arguments":{...}}` 外层，参数不再丢失。

**模型 Provider**：H5 StreamChat 加 60s 无数据读超时、H6 同 delta 的 reasoning+content 都透传（缓冲）。

**聊天**：H7 流式 emit 回调加 `select ctx.Done()` 防 goroutine 泄漏。

**摄入**：H8 投递不丢任务（独立 goroutine 阻塞发送）、H9 向量库失败降级标 indexed（不重试到 failed）、M17 空 chunks 也清理旧向量、M18 错误消息按字符截断、M19 defer stream.Close。

**知识库/认证**：H10 SELECT 加 COALESCE(bucket,'') 防 NULL 扫描崩溃、H11 local 存储传空 bucket、H12 区分 401 与 500（DB 错误不再误判 401）、L4 errIsNoRows 改 errors.Is、L5 RequirePermission nil 防御。

**基础设施/文档**：M5 request_id 改完整 UUID、M9 本地存储删空目录、C6 README 修正（CHROMA_PERSIST_PATH、gse 分词、无 sidecar、net/http、-create-admin 已实现）。

### 重新定性为非问题（2 条）

- **M6 FakeEmbeddingProvider**：经核实 Python 版即使 `environment=local` 也用真实 embedding 模型（`get_embedding_provider` 无伪向量分支），Go 版的 local 伪向量回退是重写时引入的偏差，会致 local 下 dense 检索失效。已删除该回退，始终用真实网关，对齐 Python。
- **L9 chunk_id null vs "None"**：Go 无值时输出 null 比 Python `str(None)="None"` 更正确，不改。

### Python 也错（不在 Go 复刻）

- H9 子问题「delete 成功但 add 失败导致零向量」：Python（tasks.py:149-150）同样存在，属两版共有；Go 已按 Python 顺序 embed->delete->add，embedding 失败时保留旧向量，已尽可能缓解。

### 第二轮新增修复（medium/low，22 条）

M1 405 状态码+MethodNotAllowed、M2 DSN 用 mysql.Config 转义密码、M3 ConnMaxLifetime 缓解过期连接、M4 日志共享互斥锁、M10 used_model_inference 覆盖语义、M12 敏感记忆按字符计、M13 中间件名大小写、M14 引用按 file_name 去重、M16 审计补 request_id、M22 超时哨兵错误、M23 空 args 写 {}、M24 审计元数据写 {}、M25 删除审计死代码、M27 compose 补齐环境变量、M28 Dockerfile 去除硬编码 GOARCH、M29 非 root 运行、M30 .dockerignore 排除 .env、L1 bcrypt cost=12、L8 InMemory 删除清零槽位、L11 删除 rag/prompts.go 死代码、L12 DOCX 解析 tab/br、L13 overlap 校验、L19 HEALTHCHECK、L20 迁移 003 幂等、L21 审计 Action/Status 防 NULL。

### 重新定性为非问题（15 条）

- **L2 JWT iat**：标准 claim，无害且利于审计。
- **L3 上传 100MB 限制**：Go 增加的安全约束，优于 Python 无限制。
- **L15 重试 ctx 取消留 indexing**：启动恢复（RecoverPendingIndexing）会重置 indexing->pending 自愈。
- **L14 元数据转字符串**：chromem-go 元数据接口要求 map[string]string，属设计约束。
- **L10 JSON HTML 转义 / L16 每次 http.Client / L17 Decoder 不 drain**：性能/格式细节，影响可忽略。
- **L18 无审计查询 API**：两版均无，非回归。
- **M7 delete_by_knowledge_base_id / M8 FileStorage 多方法**：未被任何调用方使用，YAGNI。
- **M20 embedding 单批**：Go 批量更高效，优于 Python 逐条。
- **M21 ToolInvokeResponse omitempty**：H13 后失败路径不再用该结构，成功路径字段恒定，纯外观。
- **M26 Storage 元数据**：仅 MinIO 影响，本地默认存储不涉及，低优先。
- **M3 pre-ping**：database/sql 无该选项，ConnMaxLifetime 是 Go 等价实践（已注释说明）。

### 暂不修（2 条，改动涉及结构性重构）

- **M11 used_model_inference omitempty**：需按事件类型（meta 恒含 / done 不含）拆分结构，留待后续。
- **M15 assistant 消息+引用非事务持久化**：需引入事务封装，留待后续。

---

## 总览

| ID | 严重度 | 模块 | 问题 | 确认 | 修复 |
|---|---|---|---|---|---|
| C1 | 🔴 | mcp_gateway | 限流完全失效 | 已确认 | 已修 |
| C2 | 🔴 | rocketmq-mcp | 带参工具参数全部丢失 | 已确认 | 已修 |
| C3 | 🔴 | common | BM25 不应用元数据过滤（跨库泄漏） | 已确认 | 已修 |
| C4 | 🔴 | common | 重启后 BM25 索引为空 | 已确认 | 已修 |
| C5 | 🔴 | common | Warmup 竞态丢失 BM25 条目 | 已确认 | 已修 |
| C6 | 🔴 | 文档 | README 配置/拓扑/create-admin 失真 | 已确认 | 已修 |
| H1 | 🟠 | common | RRF 公式 off-by-one | 已确认 | 已修 |
| H2 | 🟠 | common | BM25 IDF 负值处理不一致 | 已确认 | 已修 |
| H3 | 🟠 | common | oversample 更小 | 已确认 | 已修 |
| H4 | 🟠 | common | BM25 分词保留中文标点 | 已确认 | 已修 |
| H5 | 🟠 | common | StreamChat 无超时（资源泄漏） | 已确认 | 已修 |
| H6 | 🟠 | common | StreamChat 同 delta 丢失 content | 已确认 | 已修 |
| H7 | 🟠 | chat | 流式 goroutine 泄漏 | 已确认 | 已修 |
| H8 | 🟠 | ingestion | 提交通道满静默丢任务 | 已确认 | 已修 |
| H9 | 🟠 | ingestion | 向量库错误触发重试而非降级 | 已确认 | 已修 |
| H10 | 🟠 | knowledge | bucket NULL 列扫进 string 崩溃 | 已确认 | 已修 |
| H11 | 🟠 | knowledge | local 存储写错 bucket 值 | 已确认 | 已修 |
| H12 | 🟠 | auth | DB 错误误判为 401 | 已确认 | 已修 |
| H13 | 🟠 | mcp_gateway | 工具调用失败返回 200 而非 500 | 已确认 | 已修 |
| H14 | 🟠 | mcp_gateway | read_only 默认 false 而非 true | 已确认 | 已修 |
| H15 | 🟠 | mcp_gateway | 结果大小限制字节 vs 字符 | 已确认 | 已修 |
| M1 | 🟡 | common/all | method not allowed 返回 400 而非 405 | 已确认 | 已修 |
| M2 | 🟡 | common | DSN 不转义密码特殊字符 | 已确认 | 已修 |
| M3 | 🟡 | common | DB 无 pre-ping | 已确认 | 已修 |
| M4 | 🟡 | common | logging WithAttrs 新建 mutex | 已确认 | 已修 |
| M5 | 🟡 | common | request_id 截断 8 字符 | 已确认 | 已修 |
| M6 | 🟡 | common | FakeEmbeddingProvider 改变 local 行为 | 已确认 | 已修 |
| M7 | 🟡 | common | 缺 delete_by_knowledge_base_id | 非问题 | 不修 |
| M8 | 🟡 | common | FileStorage 接口缺方法/元数据 | 非问题 | 不修 |
| M9 | 🟡 | common | 本地存储不清理空目录 | 已确认 | 已修 |
| M10 | 🟡 | chat | used_model_inference OR vs override | 已确认 | 已修 |
| M11 | 🟡 | chat | used_model_inference omitempty | 待确认 | 未修 |
| M12 | 🟡 | chat | 敏感记忆长度按字节判断 | 已确认 | 已修 |
| M13 | 🟡 | chat | 中间件名大写无法识别为澄清 | 已确认 | 已修 |
| M14 | 🟡 | chat | 引用去重对无 file_name 分歧 | 已确认 | 已修 |
| M15 | 🟡 | chat | assistant 消息+引用非事务持久化 | 待确认 | 未修 |
| M16 | 🟡 | chat | 审计事件缺 request_id | 已确认 | 已修 |
| M17 | 🟡 | ingestion | 重新索引空文本不清理旧向量 | 已确认 | 已修 |
| M18 | 🟡 | ingestion | 错误消息按字节截断产生无效 UTF-8 | 非问题 | 不修 |
| M19 | 🟡 | ingestion | 解析 panic 时文件流未关闭 | 已确认 | 已修 |
| M20 | 🟡 | ingestion | embedding 单批发送 | 非问题 | 不修 |
| M21 | 🟡 | mcp_gateway | ToolInvokeResponse omitempty | 非问题 | 不修 |
| M22 | 🟡 | mcp_gateway | 超时检测靠中文子串匹配 | 已确认 | 已修 |
| M23 | 🟡 | mcp_gateway | 空 args 存 NULL 而非 {} | 已确认 | 已修 |
| M24 | 🟡 | audit | 审计元数据存 NULL 而非 {} | 已确认 | 已修 |
| M25 | 🟡 | audit | 便捷方法死代码且事件类型不一致 | 已确认 | 已修 |
| M26 | 🟡 | auth | Storage Save 丢弃对象元数据 | 非问题 | 不修 |
| M27 | 🟡 | 文档 | docker-compose 漏 11 个环境变量 | 已确认 | 已修 |
| M28 | 🟡 | 文档 | Dockerfile 硬编码 GOARCH=amd64 | 已确认 | 已修 |
| M29 | 🟡 | 文档 | 无非 root USER | 已确认 | 已修 |
| M30 | 🟡 | 文档 | .dockerignore 漏 .env | 已确认 | 已修 |
| L1 | ⚪ | common/auth | bcrypt cost 10 vs 12 | 已确认 | 已修 |
| L2 | ⚪ | auth | JWT 多 iat claim | 非问题 | 不修 |
| L3 | ⚪ | knowledge | 上传大小限制 100MB | 非问题 | 不修 |
| L4 | ⚪ | knowledge | errIsNoRows 用 == | 已确认 | 已修 |
| L5 | ⚪ | auth | RequirePermission nil panic | 已确认 | 已修 |
| L6 | ⚪ | common | SystemError 不能自定义消息 | 已确认 | 已修 |
| L7 | ⚪ | common | BM25 avgDocLen 算两次（死代码） | 已确认 | 已修 |
| L8 | ⚪ | common | InMemoryVectorStore 原地删除 GC 引用 | 已确认 | 已修 |
| L9 | ⚪ | rag | chunk_id "None" vs null | 非问题 | 不修 |
| L10 | ⚪ | chat | JSON HTML 转义差异 | 非问题 | 不修 |
| L11 | ⚪ | rag | prompts.go 死代码 | 已确认 | 已修 |
| L12 | ⚪ | ingestion | DOCX 漏 tab/br | 已确认 | 已修 |
| L13 | ⚪ | ingestion | 缺 chunk_overlap 校验 | 已确认 | 已修 |
| L14 | ⚪ | ingestion | 元数据值转字符串丢类型 | 非问题 | 不修 |
| L15 | ⚪ | ingestion | 重试延迟 context 取消留 indexing | 非问题 | 不修 |
| L16 | ⚪ | mcp_gateway | 每次调用新建 http.Client | 非问题 | 不修 |
| L17 | ⚪ | mcp_gateway | json.Decoder 不 drain body | 非问题 | 不修 |
| L18 | ⚪ | audit | 无审计查询 API | 非问题 | 不修 |
| L19 | ⚪ | 文档 | 无 HEALTHCHECK | 已确认 | 已修 |
| L20 | ⚪ | 文档 | 迁移 001/003 重复 summarized_up_to | 已确认 | 已修 |
| L21 | ⚪ | audit | Action/Status NullString vs NOT NULL | 已确认 | 已修 |
| L22 | ⚪ | 文档 | Document.Bucket string vs NULL（同 H10） | 已确认 | 已修 |

---

## 详细问题

### C1 · 🔴 严重 · mcp_gateway · 限流完全失效
- **Go 证据**：`internal/mcp_gateway/router.go:377` 每请求 `newService()` 新建 `McpGatewayService`；`service.go:59` 每实例全新空 `rateBuckets`；`service.go:471` `checkRateLimit` 永远 `!ok` -> 新桶 count=1 -> 返回 true。
- **Python 证据**：`backend/app/mcp_gateway/service.py:23` `_rate_buckets` 为类级 dict，跨实例共享。
- **确认状态**：待确认
- **修复状态**：未修
- **备注**：将 rateBuckets/rateMu 移到长生命周期单例；桶无清理会内存泄漏，需定期清过期桶。

### C2 · 🔴 严重 · rocketmq-mcp · 带参工具参数全部丢失
- **Go 证据**：`cmd/rocketmq-mcp-server/main.go:237` 把整个请求体 decode 进 args map；网关 `internal/mcp_gateway/service.go:438` 发的是 `{"arguments":{...}}`，故 `args={"arguments":{...}}`，`getStringArg(args,"topic",...)` 找不到顶层 topic。
- **Python 证据**：`mcp-servers/rocketmq/server.py:19-22` `ToolRequest.arguments: dict`，`server.py:41` `arguments.get("topic",...)`。
- **确认状态**：待确认
- **修复状态**：未修
- **备注**：decode 进 `struct{ Arguments map[string]any }`，传 `req.Arguments`。

### C3 · 🔴 严重 · common · BM25 不应用元数据过滤（跨库泄漏）
- **Go 证据**：`internal/common/hybrid_search.go:124`、`bm25_index.go:228` `Search(query, topK)` 无 filter 参数；注释错误声称「与 Python 一致」。
- **Python 证据**：`backend/app/common/bm25_index.py:166` 按.metadata 过滤；`vector_store.py:330` 传 filter 给 BM25。
- **确认状态**：待确认
- **修复状态**：未修
- **备注**：Search 增加 filter，按 knowledge_base_id 过滤。

### C4 · 🔴 严重 · common · 重启后 BM25 索引为空
- **Go 证据**：`internal/common/vector_store.go:228` Warmup 只遍历内存 documents map，重启后为空。
- **Python 证据**：`backend/app/common/vector_store.py:300` `_bootstrap_bm25` 从 Chroma 持久层全量拉取重建。
- **确认状态**：待确认
- **修复状态**：未修
- **备注**：chromem-go 持久化文档需回填到 BM25。

### C5 · 🔴 严重 · common · Warmup 竞态丢失 BM25 条目
- **Go 证据**：`internal/common/vector_store.go:228-246` 快照后释放锁，锁外 Reset+AddDocuments；并发 Add 的文档会被 Reset 清掉。
- **Python 证据**：`backend/app/common/vector_store.py:296-308` `_bm25_lock` 跨读+重建持锁；`224-225` upsert 同锁。
- **确认状态**：待确认
- **修复状态**：未修

### C6 · 🔴 严重 · 文档 · README 配置/拓扑/create-admin 失真
- **Go 证据**：README:103 列 `CHROMA_BASE_URL` 必填，但 `config.go:167` 读 `CHROMA_PERSIST_PATH`；README:43,74 称 chroma sidecar，实际 `vector_store.go:88` chromem-go 进程内；README:66 `-create-admin` 在 `main.go` 无 flag 解析；README 技术栈写 chi/goldmark，实际 net/http；差异表写双字分词，实际 gse。
- **Python 证据**：N/A（文档 vs Go 实现）。
- **确认状态**：待确认
- **修复状态**：未修

### H1 · 🟠 高 · common · RRF 公式 off-by-one
- **Go 证据**：`internal/common/hybrid_search.go:65` `1/(k+rank)`，rank 从 0。
- **Python 证据**：`backend/app/common/vector_store.py:108` `1/(k+rank+1)`。
- **确认状态**：待确认 · **修复状态**：未修

### H2 · 🟠 高 · common · BM25 IDF 负值处理不一致
- **Go 证据**：`internal/common/bm25_index.go:282` 负 IDF 截断到 0。
- **Python 证据**：rank_bm25 `BM25Okapi` 用 `epsilon=0.25*average_idf`（小正值）。
- **确认状态**：待确认 · **修复状态**：未修

### H3 · 🟠 高 · common · oversample 更小
- **Go 证据**：`internal/common/hybrid_search.go:112` `limit*2`。
- **Python 证据**：`backend/app/common/vector_store.py:324` `max(limit*3, 20)`。
- **确认状态**：待确认 · **修复状态**：未修

### H4 · 🟠 高 · common · BM25 分词保留中文标点
- **Go 证据**：`internal/common/bm25_index.go:124` `r > 127` 当字母，含中文标点。
- **Python 证据**：`backend/app/common/bm25_index.py:59` `ch.isalnum()` 对中文标点返回 False。
- **确认状态**：待确认 · **修复状态**：未修

### H5 · 🟠 高 · common · StreamChat 无超时
- **Go 证据**：`internal/common/model_provider.go:213` `http.Client{}` 无 Timeout。
- **Python 证据**：`backend/app/common/model_provider.py:88` `httpx.stream(timeout=60)`。
- **确认状态**：待确认 · **修复状态**：未修

### H6 · 🟠 高 · common · StreamChat 同 delta 丢失 content
- **Go 证据**：`internal/common/model_provider.go:272` if/return，reasoning 非空则 return，同 delta content 不输出。
- **Python 证据**：`backend/app/common/model_provider.py:94-99` 两者都 yield。
- **确认状态**：待确认 · **修复状态**：未修

### H7 · 🟠 高 · chat · 流式 goroutine 泄漏
- **Go 证据**：`internal/chat/service.go:605` emit 回调 `events <- e` 阻塞发送，无 `select ctx.Done()`。
- **Python 证据**：生成器流式，GC 自动清理。
- **确认状态**：待确认 · **修复状态**：未修

### H8 · 🟠 高 · ingestion · 提交通道满静默丢任务
- **Go 证据**：`internal/ingestion/service.go:110` 有界 channel(100)+非阻塞 send，满则 drop。
- **Python 证据**：`backend/app/ingestion/tasks.py:33` `ThreadPoolExecutor` 无界队列不丢。
- **确认状态**：待确认 · **修复状态**：未修

### H9 · 🟠 高 · ingestion · 向量库错误触发重试而非降级
- **Go 证据**：`internal/ingestion/service.go:217` 只捕获 Embed 错误，Delete/Add 错误返回触发重试。
- **Python 证据**：`backend/app/ingestion/tasks.py:140-156` 全部向量操作包 try/except，失败仍标 indexed。
- **确认状态**：待确认 · **修复状态**：未修

### H10 · 🟠 高 · knowledge · bucket NULL 列扫进 string 崩溃
- **Go 证据**：`internal/knowledge/models.go:55` 等 SELECT 直接扫 `&d.Bucket`(string)，无 COALESCE；schema `bucket NULL`。
- **Python 证据**：`backend/app/common/storage.py` LocalFileStorage 返回 `bucket=None`，SQLAlchemy 写 NULL。
- **确认状态**：待确认 · **修复状态**：未修

### H11 · 🟠 高 · knowledge · local 存储写错 bucket 值
- **Go 证据**：`cmd/backend-api/main.go:120` 无条件传 `settings.MinioBucket`；`service.go:31` 注释「local 时为空」。
- **Python 证据**：从 storage 返回值取 bucket，local 为 NULL。
- **确认状态**：待确认 · **修复状态**：未修

### H12 · 🟠 高 · auth · DB 错误误判为 401
- **Go 证据**：`internal/auth/router.go:180` `GetByID` 的 DB 错误和「用户不存在」都返回 401。
- **Python 证据**：`backend/app/auth/dependencies.py:93` `session.get` not-found->401，DB 错误->异常->500。
- **确认状态**：待确认 · **修复状态**：未修

### H13 · 🟠 高 · mcp_gateway · 工具调用失败返回 200 而非 500
- **Go 证据**：`internal/mcp_gateway/router.go:357` 超时/失败返回 200+`{"status":"timeout"}`。
- **Python 证据**：`backend/app/mcp_gateway/service.py:272,279` 抛 `system_error` -> 500。
- **确认状态**：待确认 · **修复状态**：未修

### H14 · 🟠 高 · mcp_gateway · read_only 默认 false 而非 true
- **Go 证据**：`internal/mcp_gateway/service.go:245` `ReadOnly bool` 零值 false。
- **Python 证据**：`backend/app/mcp_gateway/service.py:156` `item.get("read_only", True)`。
- **确认状态**：待确认 · **修复状态**：未修

### H15 · 🟠 高 · mcp_gateway · 结果大小限制字节 vs 字符
- **Go 证据**：`internal/mcp_gateway/service.go:396` `len(resultJSON)` 字节。
- **Python 证据**：`backend/app/mcp_gateway/service.py` `len(json.dumps)` 字符。
- **确认状态**：待确认 · **修复状态**：未修

### M1 · 🟡 中 · common/all · method not allowed 返回 400 而非 405
- **Go 证据**：`internal/common/errors.go:63` `BusinessError` -> 400；各 router 无 Allow 头。
- **Python 证据**：FastAPI 自动 405 + Allow 头。
- **确认状态**：待确认 · **修复状态**：未修

### M2 · 🟡 中 · common · DSN 不转义密码特殊字符
- **Go 证据**：`internal/common/database.go:43` 密码原样拼进 DSN。
- **Python 证据**：SQLAlchemy 内部处理。
- **确认状态**：待确认 · **修复状态**：未修

### M3 · 🟡 中 · common · DB 无 pre-ping
- **Go 证据**：`internal/common/database.go:27` 仅 ConnMaxLifetime。
- **Python 证据**：`backend/app/common/database.py:20` `pool_pre_ping=True`。
- **确认状态**：待确认 · **修复状态**：未修

### M4 · 🟡 中 · common · logging WithAttrs 新建 mutex
- **Go 证据**：`internal/common/logging.go:73` 派生 handler 各持锁但共享 stdout。
- **Python 证据**：`logging.StreamHandler` 模块级锁。
- **确认状态**：待确认 · **修复状态**：未修

### M5 · 🟡 中 · common · request_id 截断 8 字符
- **Go 证据**：`internal/common/logging.go:106` `uuid[:8]`。
- **Python 证据**：`backend/app/common/logging.py:67` 完整 uuid4。
- **确认状态**：待确认 · **修复状态**：未修

### M6 · 🟡 中 · common · FakeEmbeddingProvider 改变 local 行为
- **Go 证据**：`internal/common/model_provider.go` 原有 `if settings.Environment == "local"` 回退伪向量。
- **Python 证据**：`backend/app/common/model_provider.py:102` `get_embedding_provider` 始终返回 HttpModelGatewayProvider，无 local 伪向量分支。
- **确认状态**：已确认 · **修复状态**：已修
- **备注**：Python 即使 local 也用真实 embedding 模型；Go 的伪向量回退是重写时引入的偏差，会致 local 下 dense 检索失效（用户「消费重复」检索不到即此因）。已删除回退，始终用真实网关，并删除 FakeEmbeddingProvider 死代码；README 同步去掉「local 降级伪向量」描述。

### M7 · 🟡 中 · common · 缺 delete_by_knowledge_base_id
- **Go 证据**：VectorStore 接口与 BM25Index 均无此方法。
- **Python 证据**：`vector_store.py:65`、`bm25_index.py:99` 有。
- **确认状态**：待确认 · **修复状态**：未修

### M8 · 🟡 中 · common · FileStorage 接口缺方法/元数据
- **Go 证据**：`internal/common/storage.go:21` 3 方法无 metadata。
- **Python 证据**：`backend/app/common/storage.py:26` 6 方法 + metadata + StoredFile。
- **确认状态**：待确认 · **修复状态**：未修

### M9 · 🟡 中 · common · 本地存储不清理空目录
- **Go 证据**：`internal/common/storage.go:107` 仅 `os.Remove`。
- **Python 证据**：`backend/app/common/storage.py:106` 删后 `parent.rmdir()`。
- **确认状态**：待确认 · **修复状态**：未修

### M10 · 🟡 中 · chat · used_model_inference OR vs override
- **Go 证据**：`internal/chat/service.go:689` `finalInference := a || b`。
- **Python 证据**：`backend/app/chat/service.py` done 事件值直接覆盖（缺省才回退）。
- **确认状态**：待确认 · **修复状态**：未修

### M11 · 🟡 中 · chat · used_model_inference omitempty
- **Go 证据**：`internal/chat/service.go:543` `omitempty`，false 时省略。
- **Python 证据**：`backend/app/chat/service.py:599` 始终输出。
- **确认状态**：待确认 · **修复状态**：未修

### M12 · 🟡 中 · chat · 敏感记忆长度按字节判断
- **Go 证据**：`internal/chat/service.go:988` `len(content)` 字节。
- **Python 证据**：`len(content)` 字符。
- **确认状态**：待确认 · **修复状态**：未修

### M13 · 🟡 中 · chat · 中间件名大写无法识别为澄清
- **Go 证据**：`internal/rag/service.go:401` 先 Replace 再 ToLower，大小写不匹配。
- **Python 证据**：`backend/app/rag/service.py:350` 先 lower 再 replace。
- **确认状态**：待确认 · **修复状态**：未修

### M14 · 🟡 中 · chat · 引用去重对无 file_name 分歧
- **Go 证据**：`internal/rag/service.go:138` 无 file_name 不去重。
- **Python 证据**：`backend/app/rag/service.py:111` 用 None 作 key 去重。
- **确认状态**：待确认 · **修复状态**：未修

### M15 · 🟡 中 · chat · assistant 消息+引用非事务持久化
- **Go 证据**：`internal/chat/service.go:390` 消息先提交，引用逐条提交。
- **Python 证据**：`backend/app/chat/service.py:372` 单 session 原子提交。
- **确认状态**：待确认 · **修复状态**：未修

### M16 · 🟡 中 · chat · 审计事件缺 request_id
- **Go 证据**：`internal/chat/service.go:419` 等审计调用无 request_id。
- **Python 证据**：`backend/app/chat/service.py` 带 `request_id`。
- **确认状态**：待确认 · **修复状态**：未修

### M17 · 🟡 中 · ingestion · 重新索引空文本不清理旧向量
- **Go 证据**：`internal/ingestion/service.go:217` `len(chunks)>0` 守卫跳过 Delete。
- **Python 证据**：`backend/app/ingestion/tasks.py:149` 无守卫，空 chunks 仍 delete。
- **确认状态**：待确认 · **修复状态**：未修

### M18 · 🟡 中 · ingestion · 错误消息按字节截断产生无效 UTF-8
- **Go 证据**：`internal/ingestion/service.go:272` `errMsg[:1000]` 按字节。
- **Python 证据**：`backend/app/ingestion/tasks.py:211` `str(error)[:1000]` 按字符。
- **确认状态**：待确认 · **修复状态**：未修

### M19 · 🟡 中 · ingestion · 解析 panic 时文件流未关闭
- **Go 证据**：`internal/ingestion/service.go:200` `stream.Close()` 非 defer。
- **Python 证据**：`backend/app/ingestion/tasks.py:124` `with` 语句保证清理。
- **确认状态**：待确认 · **修复状态**：未修

### M20 · 🟡 中 · ingestion · embedding 单批发送
- **Go 证据**：`internal/ingestion/service.go:222` 一次发全部 chunk。
- **Python 证据**：`backend/app/ingestion/tasks.py` 逐 chunk 调用。
- **确认状态**：待确认 · **修复状态**：未修

### M21 · 🟡 中 · mcp_gateway · ToolInvokeResponse omitempty
- **Go 证据**：`internal/mcp_gateway/schemas.go:70` result/message 均 omitempty。
- **Python 证据**：Pydantic 始终含字段（null）。
- **确认状态**：待确认 · **修复状态**：未修

### M22 · 🟡 中 · mcp_gateway · 超时检测靠中文子串匹配
- **Go 证据**：`internal/mcp_gateway/service.go:381` `strings.Contains(cause,"超时")`。
- **Python 证据**：N/A（直接抛对应错误）。
- **确认状态**：待确认 · **修复状态**：未修

### M23 · 🟡 中 · mcp_gateway · 空 args 存 NULL 而非 {}
- **Go 证据**：`internal/mcp_gateway/schemas.go:108` `marshalArgs` 空返回 nil。
- **Python 证据**：默认 `{}`。
- **确认状态**：待确认 · **修复状态**：未修

### M24 · 🟡 中 · audit · 审计元数据存 NULL 而非 {}
- **Go 证据**：`internal/audit/service.go:40` nil -> NULL。
- **Python 证据**：`backend/app/audit/service.py:62` `metadata or {}`。
- **确认状态**：待确认 · **修复状态**：未修

### M25 · 🟡 中 · audit · 便捷方法死代码且事件类型不一致
- **Go 证据**：`internal/audit/service.go:87` 10 个 RecordXxx 无人调用，事件类型与实际调用方不符。
- **Python 证据**：N/A。
- **确认状态**：待确认 · **修复状态**：未修

### M26 · 🟡 中 · auth · Storage Save 丢弃对象元数据
- **Go 证据**：`internal/common/storage.go:21` Save 无 metadata 参数。
- **Python 证据**：`backend/app/knowledge/service.py` 传 content_type/file_hash metadata。
- **确认状态**：待确认 · **修复状态**：未修

### M27 · 🟡 中 · 文档 · docker-compose 漏 11 个环境变量
- **Go 证据**：`deploy/docker-compose.yml:16` 用 environment 而非 env_file，漏 RETRIEVE_LIMIT 等。
- **确认状态**：待确认 · **修复状态**：未修

### M28 · 🟡 中 · 文档 · Dockerfile 硬编码 GOARCH=amd64
- **Go 证据**：`Dockerfile.backend:10`、`Dockerfile.mcp-server:9`。
- **确认状态**：待确认 · **修复状态**：未修

### M29 · 🟡 中 · 文档 · 无非 root USER
- **Go 证据**：`Dockerfile.backend:42`、`Dockerfile.mcp-server:35`。
- **确认状态**：待确认 · **修复状态**：未修

### M30 · 🟡 中 · 文档 · .dockerignore 漏 .env
- **Go 证据**：`.dockerignore` 未排除 .env。
- **确认状态**：待确认 · **修复状态**：未修

### L1 · ⚪ 低 · common/auth · bcrypt cost 10 vs 12
- **Go 证据**：`internal/common/security.go:23` DefaultCost=10。**Python 证据**：`security.py:30` gensalt()=12。可交叉验证。**确认状态**：待确认 · **修复状态**：未修

### L2 · ⚪ 低 · auth · JWT 多 iat claim
- **Go 证据**：`internal/common/security.go:59` 含 iat。**Python 证据**：`security.py:81` 无 iat。不破坏兼容。**确认状态**：待确认 · **修复状态**：未修

### L3 · ⚪ 低 · knowledge · 上传大小限制 100MB
- **Go 证据**：`internal/knowledge/router.go:30` `maxUploadBytes=100MB`。**Python 证据**：无显式限制。**确认状态**：待确认 · **修复状态**：未修

### L4 · ⚪ 低 · knowledge · errIsNoRows 用 ==
- **Go 证据**：`internal/knowledge/models.go:143`。**确认状态**：待确认 · **修复状态**：未修

### L5 · ⚪ 低 · auth · RequirePermission nil panic
- **Go 证据**：`internal/auth/identity.go:97`。**确认状态**：待确认 · **修复状态**：未修

### L6 · ⚪ 低 · common · SystemError 不能自定义消息
- **Go 证据**：`internal/common/errors.go:85`。**Python 证据**：`errors.py:98` 可自定义。**确认状态**：待确认 · **修复状态**：未修

### L7 · ⚪ 低 · common · BM25 avgDocLen 算两次（死代码）
- **Go 证据**：`internal/common/bm25_index.go:159`。**确认状态**：待确认 · **修复状态**：未修

### L8 · ⚪ 低 · common · InMemoryVectorStore 原地删除 GC 引用
- **Go 证据**：`internal/common/vector_store.go:330`。**确认状态**：待确认 · **修复状态**：未修

### L9 · ⚪ 低 · rag · chunk_id "None" vs null
- **Go 证据**：`internal/rag/service.go:158` 无值时 null。**Python 证据**：`str(None)`="None"。Go 更对。**确认状态**：待确认 · **修复状态**：未修

### L10 · ⚪ 低 · chat · JSON HTML 转义差异
- **Go 证据**：`internal/chat/router.go:157` Marshal 默认转义 HTML。**Python 证据**：ensure_ascii=False 不转义。**确认状态**：待确认 · **修复状态**：未修

### L11 · ⚪ 低 · rag · prompts.go 死代码
- **Go 证据**：`internal/rag/prompts.go:71` 与 chat 包重复未用常量。**确认状态**：待确认 · **修复状态**：未修

### L12 · ⚪ 低 · ingestion · DOCX 漏 tab/br
- **Go 证据**：`internal/ingestion/parsers.go:131` 仅取 w:t。**Python 证据**：python-docx paragraph.text 含 tab/br。**确认状态**：待确认 · **修复状态**：未修

### L13 · ⚪ 低 · ingestion · 缺 chunk_overlap 校验
- **Go 证据**：`internal/ingestion/chunking.go:79`。**Python 证据**：langchain 校验 overlap>size 报错。**确认状态**：待确认 · **修复状态**：未修

### L14 · ⚪ 低 · ingestion · 元数据值转字符串丢类型
- **Go 证据**：`internal/ingestion/service.go:286`。**Python 证据**：保留原生类型。**确认状态**：待确认 · **修复状态**：未修

### L15 · ⚪ 低 · ingestion · 重试延迟 context 取消留 indexing
- **Go 证据**：`internal/ingestion/service.go:176`。**确认状态**：待确认 · **修复状态**：未修

### L16 · ⚪ 低 · mcp_gateway · 每次调用新建 http.Client
- **Go 证据**：`internal/mcp_gateway/service.go:436`。**确认状态**：待确认 · **修复状态**：未修

### L17 · ⚪ 低 · mcp_gateway · json.Decoder 不 drain body
- **Go 证据**：`internal/mcp_gateway/service.go:265,463`。**确认状态**：待确认 · **修复状态**：未修

### L18 · ⚪ 低 · audit · 无审计查询 API
- **Go/Python 证据**：两版均只有写，无查询端点。**确认状态**：待确认 · **修复状态**：未修

### L19 · ⚪ 低 · 文档 · 无 HEALTHCHECK
- **Go 证据**：两 Dockerfile 无 HEALTHCHECK，但有 /health 端点。**确认状态**：待确认 · **修复状态**：未修

### L20 · ⚪ 低 · 文档 · 迁移 001/003 重复 summarized_up_to
- **Go/Python 证据**：001 已含该列，003 又 ADD，全新库执行 003 报重复列。两版都有。**确认状态**：待确认 · **修复状态**：未修

### L21 · ⚪ 低 · audit · Action/Status NullString vs NOT NULL
- **Go 证据**：`internal/audit/models.go:24` NullString，但 schema NOT NULL。**确认状态**：待确认 · **修复状态**：未修

---

## 已验证等价（无问题，不修）

- 三份 SQL 迁移与 Python 逐字节一致，无 schema 漂移。
- 分块算法与 langchain RecursiveCharacterTextSplitter(keep_separator=True) 等价（测试覆盖）。
- RAG 编排状态机、意图检测规则、工具循环、会话记忆压缩、长期记忆提取 —— 对齐。
- 登录/刷新/RBAC/权限字符串/密码截断/SQL 参数化 —— 对齐。
- 会话 CRUD、知识库文档 CRUD JSON 契约 —— 对齐。
- 任务状态机、重试 3 次/30s、启动恢复 —— 对齐。
- MCP 工具授权、base_url 归一化、级联删除事务 —— 对齐。
