// File service.go: 认证服务，封装登录校验、用户查询与创建，对齐 Python app/auth/service.py。
package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"mw-bot/internal/common"
)

// 认证相关错误。Authenticate 不区分用户不存在与密码错误，统一返回 ErrInvalidCredentials，
// 避免用户名枚举。
var (
	// ErrInvalidCredentials 用户名或密码错误（含用户不存在、状态非 active、密码不匹配）。
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrUserNotFound 用户不存在（仅 CreateUser 等需要明确区分的场景使用）。
	ErrUserNotFound = errors.New("user not found")
)

// AuthService 认证服务，封装登录校验与用户查询。
// 预留 PasswordVerifier 抽象边界（当前直接用 common.VerifyPassword），
// 后续可替换为 OIDC/SSO 实现而不改业务模块。
type AuthService struct {
	db *sql.DB
}

// NewAuthService 创建认证服务实例。
//
// 参数：
//   - db: 已就绪的 MySQL 连接池。
func NewAuthService(db *sql.DB) *AuthService {
	return &AuthService{db: db}
}

// Authenticate 校验用户名与密码，返回用户或 ErrInvalidCredentials。
// 用户不存在、状态非 active、密码不匹配均返回同一错误，避免用户名枚举。
//
// 参数：
//   - ctx: 请求上下文。
//   - username: 用户名。
//   - password: 明文密码。
//
// 返回：
//   - *User: 校验通过的用户实例。
//   - error: ErrInvalidCredentials 或底层 DB 错误。
func (s *AuthService) Authenticate(ctx context.Context, username, password string) (*User, error) {
	user, err := GetUserByUsername(ctx, s.db, username)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("query user: %w", err)
	}
	if user.Status != "active" {
		return nil, ErrInvalidCredentials
	}
	if err := common.VerifyPassword(password, user.PasswordHash); err != nil {
		return nil, ErrInvalidCredentials
	}
	return user, nil
}

// GetByID 按 ID 查询用户。
//
// 参数：
//   - ctx: 请求上下文。
//   - userID: 用户ID。
//
// 返回：
//   - *User: 用户实例；不存在返回 nil + ErrUserNotFound。
//   - error: DB 错误。
func (s *AuthService) GetByID(ctx context.Context, userID int64) (*User, error) {
	user, err := GetUserByID(ctx, s.db, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("query user by id: %w", err)
	}
	return user, nil
}

// CreateUser 创建用户（管理员工具）。密码内部哈希存储，状态默认 active。
//
// 参数：
//   - ctx: 请求上下文。
//   - username: 用户名（须唯一）。
//   - password: 明文密码。
//   - role: 角色，admin/user。
//
// 返回：
//   - *User: 已创建的用户实例。
//   - error: 哈希失败或 DB 错误。
func (s *AuthService) CreateUser(ctx context.Context, username, password, role string) (*User, error) {
	hash, err := common.HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, role, status) VALUES (?, ?, ?, 'active')`,
		username, hash, role)
	if err != nil {
		return nil, fmt.Errorf("insert user: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get last insert id: %w", err)
	}
	return s.GetByID(ctx, id)
}
