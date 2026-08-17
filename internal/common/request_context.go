// File request_context.go: 通过 context.Context 透传 request_id、用户 ID 与角色。
package common

import "context"

// contextKey 是 context.Value 的键类型，避免字符串键冲突。
type contextKey string

const (
	requestIDKey contextKey = "request_id" // request_id 键
	userIDKey    contextKey = "user_id"    // 用户 ID 键
	userRoleKey  contextKey = "user_role"  // 用户角色键
)

// WithRequestID 将 request_id 注入 context，返回派生 context。
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFromContext 从 context 取出 request_id，不存在返回空串。
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

// WithUserID 将用户 ID 注入 context，返回派生 context。
func WithUserID(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, userIDKey, id)
}

// UserIDFromContext 从 context 取出用户 ID，不存在返回 0。
func UserIDFromContext(ctx context.Context) int64 {
	if v, ok := ctx.Value(userIDKey).(int64); ok {
		return v
	}
	return 0
}

// WithUserRole 将用户角色注入 context，返回派生 context。
func WithUserRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, userRoleKey, role)
}

// UserRoleFromContext 从 context 取出用户角色，不存在返回空串。
func UserRoleFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(userRoleKey).(string); ok {
		return v
	}
	return ""
}
