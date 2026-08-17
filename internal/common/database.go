// File database.go: MySQL 连接池创建与 URL→DSN 解析，对齐 Python database.py 语义。
package common

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// NewDB 创建并配置 MySQL 连接池。
// 解析 DATABASE_URL（mysql://user:pass@host:port/db）为 MySQL DSN，
// 设置连接池参数后返回 *sql.DB。
func NewDB(settings Settings) (*sql.DB, error) {
	dsn, err := parseMySQLURL(settings.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	// ConnMaxLifetime 远小于 MySQL wait_timeout（默认 8h），作为连接复用时过期连接的
	// 缓解措施（database/sql 无 pool_pre_ping，这是 Go 侧等价实践，M3）。
	db.SetConnMaxLifetime(30 * time.Minute)
	return db, nil
}

// parseMySQLURL 将 mysql://user:pass@host:port/db 格式的 URL 转换为 MySQL DSN。
// 使用 mysql.Config.FormatDSN 构造，正确转义密码中的特殊字符（@:/ 等），避免手拼 DSN 畸形（M2）。
func parseMySQLURL(databaseURL string) (string, error) {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return "", fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme != "mysql" {
		return "", fmt.Errorf("expected mysql scheme, got %q", u.Scheme)
	}
	user := u.User.Username()
	pass, _ := u.User.Password()
	host := u.Host
	dbName := strings.TrimPrefix(u.Path, "/")
	if host == "" {
		return "", fmt.Errorf("missing host in database url")
	}
	if dbName == "" {
		return "", fmt.Errorf("missing database name in database url")
	}
	cfg := mysql.NewConfig()
	cfg.User = user
	cfg.Passwd = pass
	cfg.Net = "tcp"
	cfg.Addr = host
	cfg.DBName = dbName
	cfg.Params = map[string]string{"parseTime": "true", "loc": "Local", "charset": "utf8mb4"}
	return cfg.FormatDSN(), nil
}

// Ping 检查数据库连接是否可用，5 秒超时。
func Ping(db *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return db.PingContext(ctx)
}
