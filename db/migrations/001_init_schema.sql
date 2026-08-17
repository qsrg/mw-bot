-- 企业内部智能问答系统初始化 schema（MySQL 8，UTF8MB4）
-- 主键统一 INT 自增；uuid 为对外标识扩展字段，用于 object_key 等不可枚举场景
-- 通过手写版本化 SQL 管理，不使用 Alembic；ORM model 与本脚本必须同步变更

CREATE DATABASE IF NOT EXISTS ai_qa DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
USE ai_qa;

-- 用户表
CREATE TABLE IF NOT EXISTS users (
  id INT AUTO_INCREMENT PRIMARY KEY COMMENT '用户ID',
  uuid CHAR(36) NOT NULL UNIQUE DEFAULT (UUID()) COMMENT '对外标识',
  username VARCHAR(128) NOT NULL UNIQUE COMMENT '用户名',
  password_hash VARCHAR(255) NOT NULL COMMENT '密码哈希',
  role VARCHAR(32) NOT NULL COMMENT '角色：admin/user',
  status VARCHAR(32) NOT NULL DEFAULT 'active' COMMENT '状态',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间'
) COMMENT='系统用户';

-- 知识库表
CREATE TABLE IF NOT EXISTS knowledge_bases (
  id INT AUTO_INCREMENT PRIMARY KEY COMMENT '知识库ID',
  uuid CHAR(36) NOT NULL UNIQUE DEFAULT (UUID()) COMMENT '对外标识',
  name VARCHAR(255) NOT NULL COMMENT '名称',
  visibility_scope VARCHAR(64) NOT NULL DEFAULT 'all_users' COMMENT '可见范围',
  access_policy JSON NULL COMMENT '访问策略',
  created_by INT NOT NULL COMMENT '创建人(user.id)',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间'
) COMMENT='知识库';

-- 文档表
CREATE TABLE IF NOT EXISTS documents (
  id INT AUTO_INCREMENT PRIMARY KEY COMMENT '文档ID',
  uuid CHAR(36) NOT NULL UNIQUE DEFAULT (UUID()) COMMENT '对外标识，用于 object_key',
  knowledge_base_id INT NOT NULL COMMENT '所属知识库(knowledge_bases.id)',
  file_name VARCHAR(512) NOT NULL COMMENT '文件名',
  content_type VARCHAR(128) NOT NULL COMMENT '内容类型',
  file_size BIGINT NOT NULL COMMENT '文件大小(字节)',
  file_hash VARCHAR(128) NOT NULL COMMENT '文件哈希',
  storage_backend VARCHAR(64) NOT NULL COMMENT '存储后端：db（内容入库）/local/minio',
  bucket VARCHAR(255) NULL COMMENT '存储桶',
  object_key VARCHAR(1024) NOT NULL COMMENT '对象键',
  content MEDIUMTEXT NULL COMMENT 'Markdown 文档内容（storage_backend=db 时有值）',
  index_status VARCHAR(32) NOT NULL COMMENT '索引状态：pending/indexing/indexed/failed',
  index_error TEXT NULL COMMENT '索引错误信息',
  uploaded_by INT NOT NULL COMMENT '上传人(users.id)',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  INDEX idx_documents_kb_status (knowledge_base_id, index_status)
) COMMENT='知识库文档';

-- 会话表
CREATE TABLE IF NOT EXISTS conversations (
  id INT AUTO_INCREMENT PRIMARY KEY COMMENT '会话ID',
  uuid CHAR(36) NOT NULL UNIQUE DEFAULT (UUID()) COMMENT '对外标识',
  user_id INT NOT NULL COMMENT '用户ID(users.id)',
  title VARCHAR(255) NOT NULL COMMENT '标题',
  archived TINYINT(1) NOT NULL DEFAULT 0 COMMENT '是否归档',
  summary TEXT NULL COMMENT '摘要',
  summarized_up_to INT NULL COMMENT '摘要已覆盖到的消息ID(含)',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  INDEX idx_conversations_user_updated (user_id, updated_at)
) COMMENT='问答会话';

-- 消息表
CREATE TABLE IF NOT EXISTS messages (
  id INT AUTO_INCREMENT PRIMARY KEY COMMENT '消息ID',
  uuid CHAR(36) NOT NULL UNIQUE DEFAULT (UUID()) COMMENT '对外标识',
  conversation_id INT NOT NULL COMMENT '会话ID(conversations.id)',
  role VARCHAR(32) NOT NULL COMMENT '角色：user/assistant',
  content MEDIUMTEXT NOT NULL COMMENT '内容',
  used_model_inference TINYINT(1) NOT NULL DEFAULT 0 COMMENT '是否模型推断',
  token_usage JSON NULL COMMENT 'token 用量',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  INDEX idx_messages_conversation_created (conversation_id, created_at)
) COMMENT='会话消息';

-- 消息引用表
CREATE TABLE IF NOT EXISTS message_references (
  id INT AUTO_INCREMENT PRIMARY KEY COMMENT '引用ID',
  uuid CHAR(36) NOT NULL UNIQUE DEFAULT (UUID()) COMMENT '对外标识',
  message_id INT NOT NULL COMMENT '消息ID(messages.id)',
  document_id INT NOT NULL COMMENT '文档ID(documents.id)',
  chunk_id VARCHAR(128) NOT NULL COMMENT '分块标识(非表关联)',
  file_name VARCHAR(512) NOT NULL COMMENT '文件名',
  location_label VARCHAR(255) NULL COMMENT '位置标签(页码/标题)',
  score FLOAT NOT NULL COMMENT '相关性分数',
  snippet TEXT NOT NULL COMMENT '摘要片段',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  INDEX idx_message_references_message (message_id)
) COMMENT='消息引用来源';

-- 工具调用记录表
CREATE TABLE IF NOT EXISTS tool_calls (
  id INT AUTO_INCREMENT PRIMARY KEY COMMENT '调用ID',
  uuid CHAR(36) NOT NULL UNIQUE DEFAULT (UUID()) COMMENT '对外标识',
  message_id INT NULL COMMENT '关联消息ID(messages.id)',
  user_id INT NOT NULL COMMENT '调用用户(users.id)',
  tool_name VARCHAR(255) NOT NULL COMMENT '工具名',
  server_id INT NOT NULL COMMENT 'MCP Server ID(mcp_servers.id)',
  input JSON NULL COMMENT '输入参数',
  output JSON NULL COMMENT '输出结果摘要',
  status VARCHAR(32) NOT NULL COMMENT '状态',
  error TEXT NULL COMMENT '错误信息',
  duration_ms INT NULL COMMENT '耗时(毫秒)',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  INDEX idx_tool_calls_message (message_id)
) COMMENT='MCP 工具调用记录';

-- 用户长期记忆表
CREATE TABLE IF NOT EXISTS user_memories (
  id INT AUTO_INCREMENT PRIMARY KEY COMMENT '记忆ID',
  uuid CHAR(36) NOT NULL UNIQUE DEFAULT (UUID()) COMMENT '对外标识',
  user_id INT NOT NULL COMMENT '用户ID(users.id)',
  memory_type VARCHAR(64) NOT NULL COMMENT '记忆类型',
  content TEXT NOT NULL COMMENT '内容',
  enabled TINYINT(1) NOT NULL DEFAULT 1 COMMENT '是否启用',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  INDEX idx_user_memories_user_enabled (user_id, enabled)
) COMMENT='用户长期记忆';

-- MCP Server 注册表
CREATE TABLE IF NOT EXISTS mcp_servers (
  id INT AUTO_INCREMENT PRIMARY KEY COMMENT 'Server ID',
  uuid CHAR(36) NOT NULL UNIQUE DEFAULT (UUID()) COMMENT '对外标识',
  name VARCHAR(128) NOT NULL UNIQUE COMMENT '名称',
  base_url VARCHAR(1024) NOT NULL COMMENT '地址',
  enabled TINYINT(1) NOT NULL DEFAULT 1 COMMENT '是否启用',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间'
) COMMENT='MCP Server 注册';

-- MCP 工具策略表
CREATE TABLE IF NOT EXISTS mcp_tools (
  id INT AUTO_INCREMENT PRIMARY KEY COMMENT '工具ID',
  uuid CHAR(36) NOT NULL UNIQUE DEFAULT (UUID()) COMMENT '对外标识',
  server_id INT NOT NULL COMMENT '所属 Server(mcp_servers.id)',
  tool_name VARCHAR(255) NOT NULL COMMENT '工具名',
  description TEXT NOT NULL COMMENT '描述',
  input_schema JSON NOT NULL COMMENT '输入 schema',
  read_only TINYINT(1) NOT NULL DEFAULT 1 COMMENT '是否只读',
  destructive TINYINT(1) NOT NULL DEFAULT 0 COMMENT '是否破坏性',
  requires_approval TINYINT(1) NOT NULL DEFAULT 0 COMMENT '是否需要二次确认',
  enabled TINYINT(1) NOT NULL DEFAULT 0 COMMENT '是否启用',
  allowed_roles JSON NOT NULL COMMENT '允许角色',
  timeout_seconds INT NOT NULL DEFAULT 10 COMMENT '超时(秒)',
  rate_limit VARCHAR(64) NOT NULL DEFAULT '60/minute' COMMENT '限流',
  result_size_limit INT NOT NULL DEFAULT 8192 COMMENT '结果大小限制(字节)',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  UNIQUE KEY uq_mcp_tool (server_id, tool_name)
) COMMENT='MCP 工具策略';

-- 审计事件表
CREATE TABLE IF NOT EXISTS audit_events (
  id INT AUTO_INCREMENT PRIMARY KEY COMMENT '事件ID',
  uuid CHAR(36) NOT NULL UNIQUE DEFAULT (UUID()) COMMENT '对外标识',
  event_type VARCHAR(128) NOT NULL COMMENT '事件类型',
  actor_user_id INT NULL COMMENT '操作者ID(users.id)，登录失败时为空',
  actor_role VARCHAR(32) NULL COMMENT '操作者角色',
  request_id VARCHAR(128) NULL COMMENT '请求追踪ID',
  resource_type VARCHAR(128) NULL COMMENT '资源类型',
  resource_id VARCHAR(128) NULL COMMENT '资源标识(混合用途，可为 id 或用户名)',
  action VARCHAR(128) NOT NULL COMMENT '动作',
  status VARCHAR(32) NOT NULL COMMENT '状态',
  metadata JSON NULL COMMENT '附加信息',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  INDEX idx_audit_type_created (event_type, created_at),
  INDEX idx_audit_actor_created (actor_user_id, created_at)
) COMMENT='审计事件';
