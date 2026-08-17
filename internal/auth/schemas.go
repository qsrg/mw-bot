// File schemas.go: 认证相关请求/响应 schema，对齐 Python app/auth/schemas.py。
package auth

// LoginRequest 登录请求体。
type LoginRequest struct {
	Username string `json:"username"` // 用户名
	Password string `json:"password"` // 明文密码
}

// LoginResponse 登录响应，包含 token 与用户基本信息。
type LoginResponse struct {
	AccessToken string `json:"access_token"` // JWT access token
	TokenType   string `json:"token_type"`   // token 类型，固定 "bearer"
	UserID      int64  `json:"user_id"`      // 用户ID
	Username    string `json:"username"`     // 用户名
	Role        string `json:"role"`         // 角色
}

// UserInfo 当前用户信息，用于 /auth/me 响应。
type UserInfo struct {
	UserID      int64    `json:"user_id"`      // 用户ID
	Username    string   `json:"username"`     // 用户名
	Role        string   `json:"role"`         // 角色
	Permissions []string `json:"permissions"`  // 权限列表（排序后）
}
