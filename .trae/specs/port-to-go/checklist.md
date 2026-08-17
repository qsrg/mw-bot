# Checklist

## 项目结构与配置
- [ ] Go 项目骨架存在（`go.mod`、`cmd/`、`internal/`、`db/migrations/`、`deploy/`、`frontend/`）
- [ ] `db/migrations/` 包含三个 SQL 迁移文件（001/002/003），内容与 Python 项目一致
- [ ] `frontend/` 整体从 Python 项目复制，未改动
- [ ] 所有原 Python 环境变量在 Go `Settings` 中均有对应字段，默认值/必填规则一致
- [ ] 保留 `CHROMA_PERSIST_PATH` 配置项（默认 `data/chroma`，chromem-go 持久化目录）
- [ ] 敏感配置项（`DATABASE_URL`/`LLM_API_KEY`/`MINIO_ACCESS_KEY`/`MINIO_SECRET_KEY`/`JWT_SECRET`）缺失时启动报错

## 错误处理与安全
- [ ] 错误码 `AUTH_001`（403）/`AUTH_002`（401）/`BIZ_001`（400）/`BIZ_404`（404）/`SYS_001`（500）在 Go 中定义且 HTTP 状态码一致
- [ ] 错误响应体格式 `{"detail":{"code":...,"message":...}}`
- [ ] JWT 算法 HS256，claims 含 `sub`/`username`/`role`/`exp`
- [ ] bcrypt 密码哈希与 Python 互验兼容（同一哈希可被 Go `bcrypt.CompareHashAndPassword` 校验）
- [ ] 密码 72 字节截断处理
- [ ] 角色权限映射：admin 7 项权限，user 3 项权限

## Auth 模块
- [ ] `POST /api/auth/login` 返回 `access_token` + `token_type=bearer` + `user_id` + `username` + `role`
- [ ] 登录失败返回 `AUTH_002`，写 `login_failed` 审计事件
- [ ] 登录成功写 `login_success` 审计事件
- [ ] `GET /api/auth/me` 返回 `user_id` + `username` + `role` + `permissions`
- [ ] `POST /api/auth/refresh` 接受有效 token 换发新 token，写 `token_refreshed` 审计事件
- [ ] 过期/无效 token 刷新返回 401

## Knowledge 模块
- [ ] `POST /api/knowledge/documents` 上传成功，返回文档 id + `index_status=pending`
- [ ] 上传需 `document.upload` 权限，普通用户被拒（403）
- [ ] `GET /api/knowledge/documents` 返回文档列表（支持 `knowledge_base_id` 过滤）
- [ ] `DELETE /api/knowledge/documents/{id}` 删除文档 + 清理向量库 chunk + 删除存储文件 + 删除 DB 记录
- [ ] 删除需 `document.delete` 权限
- [ ] 上传/删除写审计事件

## Ingestion 模块
- [ ] 文档解析支持 PDF/DOCX/Markdown，提取文本 + 页码/标题路径
- [ ] 分块使用递归字符分割，`chunk_size=800`、`overlap=120`、分隔符优先级与 Python 一致
- [ ] 中间件打标：URL 剔除后子串匹配，多中间件文档写多个 `mw_<name>=True`
- [ ] URL 内中间件名（如 `https://ai.nacos.io/...`）不误标
- [ ] 摄入任务状态机 `pending`→`indexing`→`indexed`|`failed`
- [ ] 重启恢复：`indexing` 重置为 `pending`，全部 `pending` 重投递
- [ ] 向量写入幂等（重索引不产生重复向量）
- [ ] 失败重试最多 3 次，间隔 30s
- [ ] 索引成功/失败写审计事件
- [ ] `reindex_all` 等价 Go 命令可补打 `mw_*` 标签

## Chat 模块
- [ ] `POST /api/chat/messages` 返回 `message_id` + `conversation_id` + `content` + `citations` + `used_model_inference`
- [ ] `POST /api/chat/messages/stream` 返回 SSE 流，事件类型 `meta`/`reasoning`/`delta`/`done`，末尾 `data: [DONE]`
- [ ] 模型网关流式失败降级为兜底文案，不中断流
- [ ] `GET /api/chat/conversations` 返回当前用户会话列表
- [ ] `DELETE /api/chat/conversations/{id}` 硬删除会话+消息+引用
- [ ] `GET /api/chat/conversations/{id}/messages` 返回消息+引用
- [ ] `GET /api/chat/memories` 返回长期记忆列表
- [ ] `DELETE /api/chat/memories/{id}` 删除记忆
- [ ] `PATCH /api/chat/memories/{id}?enabled=bool` 启用/关闭记忆
- [ ] 短期记忆超 `HISTORY_TOKEN_BUDGET` 折叠旧消息进 `summary`
- [ ] 长期记忆注入 prompt（`memory_block` 标注）

## RAG 编排
- [ ] 意图检测规则快速通道："你是谁"/"重新回答"/"详细点"/"简短点" 等精确短句不调 LLM
- [ ] "你是…" 开头的身份/能力询问不检索知识库，直接回答
- [ ] `INTENT_DETECTION=true` 时未命中规则调 LLM 精判
- [ ] `INTENT_DETECTION=false` 时默认按 knowledge 处理
- [ ] followup 指令用上一条用户问题检索重答
- [ ] 跨中间件歧义：未指定中间件且 ≥2 中间件各命中 ≥3 片段时反问，不调 LLM、无引用
- [ ] 反问后用户回复中间件名，用上一条用户问题 + `mw_<name>` 过滤检索回答
- [ ] 提问指定中间件时按 `mw_<name>=true` 过滤检索
- [ ] 混合检索：dense + BM25 + RRF（k=60，top_k=5）
- [ ] 分数阈值 0.3 过滤（融合前）
- [ ] BM25 不可用降级纯向量检索
- [ ] 工具循环 ≤ `MAX_TOOL_ITERATIONS` 轮，通过 `mcp_gateway` 调用
- [ ] `used_model_inference`：检索空 + 无工具调用时为 true
- [ ] 引用来源去重（同 file_name 保留最高分）
- [ ] RAG 编排只在 `internal/rag/` 内部，不直接调 MCP Server

## MCP Gateway 模块
- [ ] `POST /api/mcp/servers` 注册 MCP Server（`mcp.server.register` 权限）
- [ ] `GET /api/mcp/servers` 列出 Server 含工具数
- [ ] `PATCH /api/mcp/servers/{id}` 更新名称/地址/启用
- [ ] `DELETE /api/mcp/servers/{id}` 删除 Server 及名下工具
- [ ] `POST /api/mcp/servers/{id}/refresh` 刷新工具 schema（`mcp.tool.manage` 权限）
- [ ] `GET /api/mcp/tools` 列出所有工具
- [ ] `PATCH /api/mcp/tools/{id}` 更新工具策略（启用/二次确认/角色/超时）
- [ ] `POST /api/mcp/tools/{id}/invoke` 调用工具（`mcp.tool.call` 权限）
- [ ] 工具调用检查：启用 / 角色 / schema / `confirmed`（`requires_approval=true` 时）/ 限流 / 超时
- [ ] 限流：固定窗口内存桶，解析 `rate_limit`（如 `60/minute`）
- [ ] 结果大小限制 8192 字节
- [ ] 工具调用结果标准化，不泄露凭证/连接串/堆栈
- [ ] 工具调用写 `tool_calls` 表 + `tool_called` 审计事件

## Audit 模块
- [ ] `audit_events` 表模型与字段一致（event_type/actor_user_id/actor_role/request_id/resource_type/resource_id/action/status/metadata/created_at）
- [ ] 登录/刷新/上传/删除/索引成功/索引失败/问答/检索/工具调用/记忆变更均写入审计
- [ ] 审计事件包含 `request_id`（从 context 透传）

## RocketMQ MCP Server
- [ ] 监听 10914 端口
- [ ] `GET /health` 返回 `{status, real_mode, namesrv}`
- [ ] `GET /tools` 返回 5 个工具 schema（`rocketmq_list_clusters`/`rocketmq_get_broker_status`/`rocketmq_get_topic_config`/`rocketmq_get_consumer_group_status`/`rocketmq_get_consumer_lag`）
- [ ] `POST /tools/{name}` 调用 handler
- [ ] 未配置 `ROCKETMQ_NAMESRV` 时返回 mock 数据（结构与 Python mock 一致）

## 部署与运维
- [ ] `Dockerfile.backend` 多阶段构建，输出单二进制
- [ ] `Dockerfile.mcp-server` 多阶段构建，输出单二进制
- [ ] `docker-compose.yml` 包含 `backend-api`/`rocketmq-mcp-server`/`frontend` 三个服务（无 chroma sidecar）
- [ ] `backend-api` 挂载 `data/` 卷（chromem-go 持久化 + 本地文件存储）
- [ ] `env.example` 保持 `CHROMA_PERSIST_PATH=data/chroma`
- [ ] Go 后端使用 `chromem-go` 内嵌向量库，通过 `CHROMA_PERSIST_PATH` 持久化
- [ ] 启动时恢复未完成索引任务（best-effort 不阻断）
- [ ] 启动时预热 BM25（best-effort 不阻断）
- [ ] `GET /health` 返回 `{"status":"ok","service":"backend-api"}`
- [ ] 日志结构化 JSON，含 `request_id`/`timestamp`/`level`/`message`/`fields`

## 端到端验证
- [ ] 前端无需改动即可切换到 Go 后端
- [ ] `docker compose up` 一键启动全栈
- [ ] 前端登录 → 上传文档 → 等待索引完成 → 问答 → 展示引用 全流程通过
- [ ] 流式问答 SSE 事件正确解析（meta/reasoning/delta/done）
- [ ] MCP Server 注册 → 刷新工具 → 调用工具 全流程通过
- [ ] 跨中间件歧义反问场景验证
- [ ] 短期记忆压缩场景验证（长会话触发摘要折叠）
