-- 002: 为 mcp_servers.base_url 增加唯一约束，从 DB 层防止同一后端被重复注册。
-- 与 ORM model (McpServer.base_url unique=True) 同步。
-- base_url 为 VARCHAR(1024)，utf8mb4 下超出 InnoDB 3072 字节键长限制，
-- 故使用前缀索引(767 字符≈3068 字节)；URL 长度远小于此，不影响去重语义。
-- 若已存在重复 base_url，需先清理后再执行本脚本。
--
-- MySQL 8 不支持 ADD INDEX IF NOT EXISTS，用 information_schema 守卫 + 动态 SQL
-- 保证幂等，重复执行时跳过而非报 Duplicate key name。

SET @idx_exists := (
  SELECT COUNT(DISTINCT INDEX_NAME) FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'mcp_servers' AND INDEX_NAME = 'uq_mcp_server_base_url'
);
SET @ddl := IF(@idx_exists = 0,
  'ALTER TABLE mcp_servers ADD UNIQUE KEY uq_mcp_server_base_url (base_url(767))',
  'SELECT ''uq_mcp_server_base_url already exists, skip'' AS info');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
