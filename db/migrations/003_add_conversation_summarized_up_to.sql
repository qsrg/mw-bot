-- 003: 为 conversations 增加摘要覆盖边界标记，支撑短期记忆 token 预算压缩。
-- summarized_up_to 记录会话摘要已折叠覆盖到的消息自增ID(含)，其后的消息为
-- 近期上下文窗口；为 NULL 表示尚未压缩（全部消息均在窗口内）。
-- 与 ORM model (Conversation.summarized_up_to) 同步。
--
-- 注意：001_init_schema.sql 已包含该列。MySQL 8 不支持 ADD COLUMN IF NOT EXISTS
-- （该语法仅 MariaDB 提供），此处用 information_schema 守卫 + 动态 SQL 保证幂等，
-- 全新库依次执行 001+003 时跳过而非报 Duplicate column。

SET @col_exists := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'conversations' AND COLUMN_NAME = 'summarized_up_to'
);
SET @ddl := IF(@col_exists = 0,
  'ALTER TABLE conversations ADD COLUMN summarized_up_to INT NULL COMMENT ''摘要已覆盖到的消息ID(含)''',
  'SELECT ''summarized_up_to already exists, skip'' AS info');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
