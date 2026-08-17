// Package auth 实现用户认证与身份上下文，对齐 Python app/auth 模块语义。
//
// 包含 User 实体（对应 users 表）、AuthService（登录校验/用户查询/创建）、
// IdentityContext（标准身份上下文，业务模块只依赖它）、HTTP 处理器
// （/api/auth/login、/api/auth/me、/api/auth/refresh）与 AuthMiddleware
// （解析 JWT 并注入 IdentityContext 到请求 ctx）。
package auth

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// User 系统用户实体，对应数据库 users 表。
// 字段映射与 db/migrations/001_init_schema.sql 中 users 列定义对齐。
type User struct {
	ID           int64     `json:"id"`            // 主键ID（INT AUTO_INCREMENT）
	UUID         string    `json:"uuid"`          // 对外标识（CHAR(36)，数据库 DEFAULT (UUID())）
	Username     string    `json:"username"`      // 用户名（VARCHAR(128) UNIQUE）
	PasswordHash string    `json:"-"`             // 密码哈希（bcrypt），不序列化到 JSON
	Role         string    `json:"role"`          // 角色：admin/user
	Status       string    `json:"status"`        // 状态：active/disabled
	CreatedAt    time.Time `json:"created_at"`    // 创建时间（数据库 DEFAULT CURRENT_TIMESTAMP）
	UpdatedAt    time.Time `json:"updated_at"`    // 更新时间（ON UPDATE CURRENT_TIMESTAMP）
}

// GetUserByUsername 按用户名查询用户。未找到返回 sql.ErrNoRows。
//
// 参数：
//   - ctx: 请求上下文，用于超时与取消。
//   - db: 数据库连接池。
//   - username: 用户名。
//
// 返回：
//   - *User: 用户实例。
//   - error: 查询失败或未找到。
func GetUserByUsername(ctx context.Context, db *sql.DB, username string) (*User, error) {
	const query = `SELECT id, uuid, username, password_hash, role, status, created_at, updated_at
		FROM users WHERE username = ?`
	row := db.QueryRowContext(ctx, query, username)
	return scanUser(row)
}

// GetUserByID 按 ID 查询用户。未找到返回 sql.ErrNoRows。
//
// 参数：
//   - ctx: 请求上下文。
//   - db: 数据库连接池。
//   - id: 用户ID。
//
// 返回：
//   - *User: 用户实例。
//   - error: 查询失败或未找到。
func GetUserByID(ctx context.Context, db *sql.DB, id int64) (*User, error) {
	const query = `SELECT id, uuid, username, password_hash, role, status, created_at, updated_at
		FROM users WHERE id = ?`
	row := db.QueryRowContext(ctx, query, id)
	return scanUser(row)
}

// rowScanner 抽象 *sql.Row 与 *sql.Rows 的 Scan 接口，便于复用 scanUser。
type rowScanner interface {
	Scan(dest ...any) error
}

// scanUser 将一行扫描到 *User，未找到返回 sql.ErrNoRows。
func scanUser(row rowScanner) (*User, error) {
	u := &User{}
	err := row.Scan(&u.ID, &u.UUID, &u.Username, &u.PasswordHash, &u.Role, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("scan user: %w", err)
	}
	return u, nil
}
