// File router.go: 认证 HTTP 处理器与 AuthMiddleware，对齐 Python app/auth/router.py。
//
// 路由：
//   - POST /api/auth/login   账号密码登录，签发 JWT
//   - GET  /api/auth/me      返回当前登录用户信息
//   - POST /api/auth/refresh 用有效 token 换发新 token
//
// 审计：login 成功/失败、token 刷新均记录审计事件，字段映射与 Python router.py 一致。
package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"

	"mw-bot/internal/audit"
	"mw-bot/internal/common"
)

// Handler 认证 HTTP 处理器，封装 db、settings、audit 依赖。
type Handler struct {
	db       *sql.DB
	settings common.Settings
	audit    *audit.AuditService
}

// NewHandler 创建认证处理器。
//
// 参数：
//   - db: 已就绪的 MySQL 连接池。
//   - settings: 应用配置（提供 JWTSecret、AccessTokenMinutes 等）。
//   - auditSvc: 审计服务，用于记录登录/刷新事件。
func NewHandler(db *sql.DB, settings common.Settings, auditSvc *audit.AuditService) *Handler {
	return &Handler{db: db, settings: settings, audit: auditSvc}
}

// RegisterRoutes 注册认证路由到 mux。
// /api/auth/me 与 /api/auth/refresh 前置 AuthMiddleware，/api/auth/login 无需登录。
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/auth/login", h.Login)
	mux.Handle("/api/auth/me", h.AuthMiddleware(http.HandlerFunc(h.Me)))
	mux.Handle("/api/auth/refresh", h.AuthMiddleware(http.HandlerFunc(h.Refresh)))
}

// Login 处理 POST /api/auth/login：校验账号密码，签发 JWT，记录审计。
// 失败时写 login_failed 审计事件（resource_id 记录尝试的用户名），
// 成功时写 login_success 审计事件（resource_id 记录 user.id 字符串）。
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.MethodNotAllowed(w)
		return
	}
	ctx := r.Context()
	requestID := common.RequestIDFromContext(ctx)

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		common.WriteError(w, common.BusinessError("invalid JSON body: "+err.Error()))
		return
	}
	if req.Username == "" || req.Password == "" {
		common.WriteError(w, common.BusinessError("用户名和密码不能为空"))
		return
	}

	svc := NewAuthService(h.db)
	user, err := svc.Authenticate(ctx, req.Username, req.Password)
	if err != nil {
		// 登录失败：resource_id 记录尝试的用户名（与 Python router.py 一致）
		h.recordLoginAudit(ctx, "login_failed", 0, "", requestID, req.Username, req.Username, "failed")
		common.WriteError(w, common.Unauthorized("用户名或密码错误"))
		return
	}

	token, err := common.IssueToken(user.ID, user.Username, user.Role, h.settings)
	if err != nil {
		common.WriteError(w, common.SystemError(err))
		return
	}

	// 登录成功：resource_id 记录 user.id 字符串，metadata.username 记录用户名
	h.recordLoginAudit(ctx, "login_success", user.ID, user.Role, requestID,
		strconv.FormatInt(user.ID, 10), user.Username, "success")

	writeJSON(w, http.StatusOK, LoginResponse{
		AccessToken: token,
		TokenType:   "bearer",
		UserID:      user.ID,
		Username:    user.Username,
		Role:        user.Role,
	})
}

// Me 处理 GET /api/auth/me：返回当前登录用户信息与权限。
// 依赖 AuthMiddleware 注入 IdentityContext 到 ctx。
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.MethodNotAllowed(w)
		return
	}
	identity := IdentityFromContext(r.Context())
	if identity == nil {
		common.WriteError(w, common.Unauthorized("未登录"))
		return
	}
	// 排序 permissions，与 Python sorted() 一致
	perms := append([]string(nil), identity.Permissions...)
	sort.Strings(perms)
	writeJSON(w, http.StatusOK, UserInfo{
		UserID:      identity.UserID,
		Username:    identity.Username,
		Role:        identity.Role,
		Permissions: perms,
	})
}

// Refresh 处理 POST /api/auth/refresh：用有效 token 换发新 token，延长会话。
// 依赖 AuthMiddleware 校验当前 token 有效且用户启用；过期或无效 token 直接 401。
// 复用 IssueToken 重签并刷新 exp，不引入独立 refresh token。
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.MethodNotAllowed(w)
		return
	}
	ctx := r.Context()
	identity := IdentityFromContext(ctx)
	if identity == nil {
		common.WriteError(w, common.Unauthorized("未登录"))
		return
	}

	token, err := common.IssueToken(identity.UserID, identity.Username, identity.Role, h.settings)
	if err != nil {
		common.WriteError(w, common.SystemError(err))
		return
	}

	// 写 token_refreshed 审计事件，resource_id 记录 user.id 字符串
	h.audit.RecordEvent(ctx, audit.AuditEvent{
		EventType:    "token_refreshed",
		ActorUserID:  sql.NullInt64{Int64: identity.UserID, Valid: true},
		ActorRole:    sql.NullString{String: identity.Role, Valid: true},
		RequestID:    identity.RequestID,
		ResourceType: sql.NullString{String: "user", Valid: true},
		ResourceID:   sql.NullString{String: strconv.FormatInt(identity.UserID, 10), Valid: true},
		Action:       sql.NullString{String: "refresh", Valid: true},
		Status:       sql.NullString{String: "success", Valid: true},
	})

	writeJSON(w, http.StatusOK, LoginResponse{
		AccessToken: token,
		TokenType:   "bearer",
		UserID:      identity.UserID,
		Username:    identity.Username,
		Role:        identity.Role,
	})
}

// AuthMiddleware 解析 Authorization 头，校验 JWT，将 IdentityContext 注入 ctx。
// 用于需要登录的路由前置中间件。token 无效、用户不存在或已禁用均返回 401。
func (h *Handler) AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		requestID := common.RequestIDFromContext(ctx)

		token, appErr := ExtractTokenFromHeader(r.Header.Get("Authorization"))
		if appErr != nil {
			common.WriteError(w, appErr)
			return
		}
		identity, appErr := ParseIdentityFromToken(token, h.settings, requestID)
		if appErr != nil {
			common.WriteError(w, appErr)
			return
		}
		// 二次校验用户存在且启用，与 Python dependencies.py 一致
		svc := NewAuthService(h.db)
		user, err := svc.GetByID(ctx, identity.UserID)
		if err != nil {
			if errors.Is(err, ErrUserNotFound) {
				common.WriteError(w, common.Unauthorized("用户不存在或已禁用"))
				return
			}
			// DB 等基础设施错误返回 500，不应误判为 401（H12）
			common.WriteError(w, common.SystemError(err))
			return
		}
		if user == nil || user.Status != "active" {
			common.WriteError(w, common.Unauthorized("用户不存在或已禁用"))
			return
		}
		ctx = WithIdentity(ctx, identity)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// recordLoginAudit 记录登录审计事件，统一字段映射。
// resource_id 在成功时为 user.id 字符串，失败时为尝试的用户名（与 Python router.py 一致）。
// username 写入 metadata 便于按用户名聚合查询。
func (h *Handler) recordLoginAudit(ctx context.Context, eventType string, userID int64, role, requestID, resourceID, username, status string) {
	h.audit.RecordEvent(ctx, audit.AuditEvent{
		EventType:    eventType,
		ActorUserID:  sql.NullInt64{Int64: userID, Valid: userID > 0},
		ActorRole:    sql.NullString{String: role, Valid: role != ""},
		RequestID:    requestID,
		ResourceType: sql.NullString{String: "user", Valid: true},
		ResourceID:   sql.NullString{String: resourceID, Valid: resourceID != ""},
		Action:       sql.NullString{String: "login", Valid: true},
		Status:       sql.NullString{String: status, Valid: true},
		Metadata:     marshalJSON(map[string]any{"username": username}),
	})
}

// writeJSON 写入 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// marshalJSON 序列化为 json.RawMessage，失败返回 nil（写入 NULL）。
func marshalJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}
