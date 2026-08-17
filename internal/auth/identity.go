// File identity.go: 标准身份上下文 IdentityContext 与 JWT 解析，对齐 Python app/auth/dependencies.py。
//
// IdentityContext 是业务模块消费身份的唯一入口，不感知具体认证提供者。
// AuthMiddleware 解析 Authorization 头中的 JWT，构造 IdentityContext 并注入请求 ctx。
package auth

import (
	"context"
	"strconv"
	"strings"

	"mw-bot/internal/common"
)

// IdentityContext 标准身份上下文，供业务模块消费。
// 字段与 Python IdentityContext dataclass 对齐。
type IdentityContext struct {
	UserID      int64    // 用户ID
	Username    string   // 用户名
	Role        string   // 角色
	Permissions []string // 权限列表
	RequestID   string   // 请求追踪ID
}

// identityCtxKey 是 IdentityContext 在 context.Context 中的键类型，避免键冲突。
type identityCtxKey struct{}

// WithIdentity 将 IdentityContext 注入 context，返回派生 context。
func WithIdentity(ctx context.Context, identity *IdentityContext) context.Context {
	return context.WithValue(ctx, identityCtxKey{}, identity)
}

// IdentityFromContext 从 context 取出 IdentityContext，不存在返回 nil。
func IdentityFromContext(ctx context.Context) *IdentityContext {
	if v, ok := ctx.Value(identityCtxKey{}).(*IdentityContext); ok {
		return v
	}
	return nil
}

// ExtractTokenFromHeader 从 Authorization 头提取 Bearer token。
// 头缺失或格式不对返回 Unauthorized 错误。
//
// 参数：
//   - authHeader: Authorization 头原始值。
//
// 返回：
//   - string: token 字符串。
//   - *common.AppError: 头格式不合法（401）。
func ExtractTokenFromHeader(authHeader string) (string, *common.AppError) {
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return "", common.Unauthorized("未提供有效的登录凭证")
	}
	return strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer ")), nil
}

// ParseIdentityFromToken 从 JWT 解析标准身份上下文。
// token 无效、过期或 claims 缺失返回 Unauthorized 错误。
//
// 参数：
//   - token: JWT 字符串。
//   - settings: 应用配置（提供 JWTSecret 与 JWTAlgorithm）。
//   - requestID: 当前请求追踪ID。
//
// 返回：
//   - *IdentityContext: 解析出的身份上下文。
//   - *common.AppError: token 无效或 claims 不完整（401）。
func ParseIdentityFromToken(token string, settings common.Settings, requestID string) (*IdentityContext, *common.AppError) {
	claims, err := common.ParseToken(token, settings)
	if err != nil {
		return nil, common.Unauthorized("登录已过期或无效")
	}
	if claims.Subject == "" || claims.Username == "" || claims.Role == "" {
		return nil, common.Unauthorized("登录信息不完整")
	}
	userID, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil {
		return nil, common.Unauthorized("登录信息不完整")
	}
	return &IdentityContext{
		UserID:      userID,
		Username:    claims.Username,
		Role:        claims.Role,
		Permissions: common.PermissionsForRole(claims.Role),
		RequestID:   requestID,
	}, nil
}

// RequirePermission 检查 identity 是否拥有指定权限，没有返回 Forbidden 错误。
//
// 参数：
//   - identity: 当前用户身份上下文。
//   - permission: 需要的权限标识，如 "document.upload"。
//
// 返回：
//   - *common.AppError: 权限不足（403），通过返回 nil。
func RequirePermission(identity *IdentityContext, permission string) *common.AppError {
	// 防御：未挂载 AuthMiddleware 的路由调用时 identity 可能为 nil（L5）
	if identity == nil {
		return common.Unauthorized("未认证")
	}
	for _, p := range identity.Permissions {
		if p == permission {
			return nil
		}
	}
	return common.Forbidden("缺少权限：" + permission)
}
