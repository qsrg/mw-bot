-- 004: Markdown 文档内容入库，支持在线新增与编辑。
-- storage_backend 新增取值 'db'：内容存于 content 列，不再落文件存储；
-- 历史文件型文档（local/minio）的 content 保持 NULL，读取仍走文件存储。
--
-- 注意：001_init_schema.sql 已包含该列。MySQL 8 不支持 ADD COLUMN IF NOT EXISTS
-- （该语法仅 MariaDB 提供），此处用 information_schema 守卫 + 动态 SQL 保证幂等，
-- 全新库依次执行 001+004 时跳过而非报 Duplicate column。

SET @col_exists := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'documents' AND COLUMN_NAME = 'content'
);
SET @ddl := IF(@col_exists = 0,
  'ALTER TABLE documents ADD COLUMN content MEDIUMTEXT NULL COMMENT ''Markdown 文档内容（storage_backend=db 时有值）'' AFTER object_key',
  'SELECT ''content already exists, skip'' AS info');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
