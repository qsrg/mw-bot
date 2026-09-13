// File router.go: MCP Gateway HTTP 处理器，对齐 Python app/mcp_gateway/router.py。
//
// 路由（所有路由前置 AuthMiddleware）：
//   - POST   /api/mcp/servers                 注册 Server（mcp.server.register）
//   - GET    /api/mcp/servers                 列出 Server（登录）
//   - PATCH  /api/mcp/servers/{id}            更新 Server（mcp.server.register）
//   - DELETE /api/mcp/servers/{id}            删除 Server（mcp.server.register）
//   - POST   /api/mcp/servers/{id}/refresh    刷新工具 schema（mcp.tool.manage）
//   - GET    /api/mcp/tools                   列出工具（登录）
//   - PATCH  /api/mcp/tools/{id}              更新工具策略（mcp.tool.manage）
//   - POST   /api/mcp/tools/{id}/invoke       调用工具（mcp.tool.call）
package mcp_gateway

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"fmt"
	"mw-bot/internal/audit"
	"mw-bot/internal/auth"
	"mw-bot/internal/common"
)

// Handler MCP Gateway HTTP 处理器，封装 db/audit/auth 依赖。
type Handler struct {
	db    *sql.DB
	audit *audit.AuditService
	authH *auth.Handler
}

// NewHandler 创建 MCP Gateway 处理器。
//
// 参数：
//   - db: 已就绪的 MySQL 连接池。
//   - auditSvc: 审计服务。
//   - authHandler: 认证处理器（复用 AuthMiddleware）。
func NewHandler(db *sql.DB, auditSvc *audit.AuditService, authHandler *auth.Handler) *Handler {
	return &Handler{db: db, audit: auditSvc, authH: authHandler}
}

// RegisterRoutes 注册 MCP Gateway 路由到 mux。
// 所有路由前置 AuthMiddleware，要求调用方登录。
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("/api/mcp/servers", h.authH.AuthMiddleware(http.HandlerFunc(h.serversRoot)))
	mux.Handle("/api/mcp/servers/", h.authH.AuthMiddleware(http.HandlerFunc(h.serverByID)))
	mux.Handle("/api/mcp/tools", h.authH.AuthMiddleware(http.HandlerFunc(h.toolsRoot)))
	mux.Handle("/api/mcp/tools/", h.authH.AuthMiddleware(http.HandlerFunc(h.toolByID)))
	mux.Handle("/api/mcp/middleware-options", h.authH.AuthMiddleware(http.HandlerFunc(h.middlewareOptions)))
	mux.Handle("/api/mcp/clusters", h.authH.AuthMiddleware(http.HandlerFunc(h.clustersRoot)))
	mux.Handle("/api/mcp/clusters/", h.authH.AuthMiddleware(http.HandlerFunc(h.clusterByID)))
}

// middlewareOptions 列出可登记的中间件选项（来自 MIDDLEWARES 配置）。
func (h *Handler) middlewareOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.MethodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, common.MiddlewareList())
}

// clustersRoot 处理 /api/mcp/clusters 的 POST（新增登记）与 GET（列表）。
func (h *Handler) clustersRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.createCluster(w, r)
	case http.MethodGet:
		h.listClusters(w, r)
	default:
		common.MethodNotAllowed(w)
	}
}

// clusterRequest 集群登记请求体。
type clusterRequest struct {
	Middleware  *string `json:"middleware"`
	ClusterName *string `json:"cluster_name"`
	DisplayName *string `json:"display_name"`
	Datacenter  *string `json:"datacenter"`
	Namesrv     *string `json:"namesrv"`
	Description *string `json:"description"`
	Enabled     *bool   `json:"enabled"`
}

// toClusterResponse 将集群登记转为响应结构。
func toClusterResponse(c *MiddlewareCluster) map[string]any {
	return map[string]any{
		"id":           c.UUID,
		"middleware":   c.Middleware,
		"cluster_name": c.ClusterName,
		"display_name": c.DisplayName,
		"datacenter":   c.Datacenter,
		"namesrv":      c.Namesrv,
		"description":  c.Description,
		"enabled":      c.Enabled,
		"created_at":   c.CreatedAt,
	}
}

// listClusters 列出全部集群登记（含停用）。
func (h *Handler) listClusters(w http.ResponseWriter, r *http.Request) {
	clusters, err := ListMiddlewareClusters(r.Context(), h.db)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	items := make([]map[string]any, 0, len(clusters))
	for _, c := range clusters {
		items = append(items, toClusterResponse(c))
	}
	writeJSON(w, http.StatusOK, items)
}

// createCluster 新增集群登记（同中间件下真实集群名唯一）。
func (h *Handler) createCluster(w http.ResponseWriter, r *http.Request) {
	identity := auth.IdentityFromContext(r.Context())
	if identity == nil || identity.Role != "admin" {
		common.WriteError(w, common.Forbidden("需要管理员权限"))
		return
	}
	var req clusterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		common.WriteError(w, common.BusinessError("invalid JSON body: "+err.Error()))
		return
	}
	if req.Middleware == nil || req.ClusterName == nil || req.Datacenter == nil ||
		strings.TrimSpace(*req.Middleware) == "" || strings.TrimSpace(*req.ClusterName) == "" ||
		strings.TrimSpace(*req.Datacenter) == "" {
		common.WriteError(w, common.BusinessError("中间件、真实集群名与机房不能为空"))
		return
	}
	// 同中间件下集群名唯一校验
	clusters, err := ListMiddlewareClusters(r.Context(), h.db)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	mw := strings.ToLower(strings.TrimSpace(*req.Middleware))
	name := strings.TrimSpace(*req.ClusterName)
	for _, c := range clusters {
		if c.Middleware == mw && c.ClusterName == name {
			common.WriteError(w, common.BusinessError(fmt.Sprintf("集群 %s 已登记（%s）", name, mw)))
			return
		}
	}
	created, err := CreateMiddlewareCluster(r.Context(), h.db, &MiddlewareCluster{
		Middleware:  mw,
		ClusterName: name,
		DisplayName: strings.TrimSpace(deref(req.DisplayName)),
		Datacenter:  strings.TrimSpace(*req.Datacenter),
		Namesrv:     strings.TrimSpace(deref(req.Namesrv)),
		Description: strings.TrimSpace(deref(req.Description)),
		Enabled:     true,
	})
	if err != nil {
		common.WriteError(w, common.BusinessError(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, toClusterResponse(created))
}

// clusterByID 处理 /api/mcp/clusters/{id} 的 PATCH（更新）与 DELETE（删除）。
func (h *Handler) clusterByID(w http.ResponseWriter, r *http.Request) {
	identity := auth.IdentityFromContext(r.Context())
	if identity == nil || identity.Role != "admin" {
		common.WriteError(w, common.Forbidden("需要管理员权限"))
		return
	}
	clusterUUID := strings.TrimPrefix(r.URL.Path, "/api/mcp/clusters/")
	if clusterUUID == "" || strings.Contains(clusterUUID, "/") {
		common.WriteError(w, common.NotFound("集群登记不存在"))
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var req clusterRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			common.WriteError(w, common.BusinessError("invalid JSON body: "+err.Error()))
			return
		}
		updated, err := UpdateMiddlewareCluster(r.Context(), h.db, clusterUUID,
			req.Middleware, req.ClusterName, req.DisplayName, req.Datacenter,
			req.Namesrv, req.Description, req.Enabled)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if updated == nil {
			common.WriteError(w, common.NotFound("集群登记不存在"))
			return
		}
		writeJSON(w, http.StatusOK, toClusterResponse(updated))
	case http.MethodDelete:
		deleted, err := DeleteMiddlewareCluster(r.Context(), h.db, clusterUUID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if !deleted {
			common.WriteError(w, common.NotFound("集群登记不存在"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	default:
		common.MethodNotAllowed(w)
	}
}

// deref 取字符串指针的值，nil 返回空串。
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// serversRoot 处理 /api/mcp/servers 的 POST（注册）与 GET（列表）。
func (h *Handler) serversRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.registerServer(w, r)
	case http.MethodGet:
		h.listServers(w, r)
	default:
		common.MethodNotAllowed(w)
	}
}

// serverByID 处理 /api/mcp/servers/{id} 与 /api/mcp/servers/{id}/refresh。
// 路径解析：截取 /api/mcp/servers/ 后的部分，按 "/" 切分为 serverID 与可选子路径。
func (h *Handler) serverByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/mcp/servers/")
	parts := strings.SplitN(path, "/", 2)
	serverID := parts[0]
	if serverID == "" {
		common.WriteError(w, common.NotFound("MCP Server 不存在"))
		return
	}
	// 子路径 /refresh 刷新工具
	if len(parts) == 2 && parts[1] == "refresh" {
		if r.Method != http.MethodPost {
			common.MethodNotAllowed(w)
			return
		}
		h.refreshTools(w, r, serverID)
		return
	}
	// 无子路径：PATCH 更新 / DELETE 删除
	switch r.Method {
	case http.MethodPatch:
		h.updateServer(w, r, serverID)
	case http.MethodDelete:
		h.deleteServer(w, r, serverID)
	default:
		common.MethodNotAllowed(w)
	}
}

// toolsRoot 处理 /api/mcp/tools 的 GET（列表）。
func (h *Handler) toolsRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.MethodNotAllowed(w)
		return
	}
	h.listTools(w, r)
}

// toolByID 处理 /api/mcp/tools/{id} 与 /api/mcp/tools/{id}/invoke。
func (h *Handler) toolByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/mcp/tools/")
	parts := strings.SplitN(path, "/", 2)
	toolID := parts[0]
	if toolID == "" {
		common.WriteError(w, common.NotFound("工具不存在"))
		return
	}
	if len(parts) == 2 && parts[1] == "invoke" {
		if r.Method != http.MethodPost {
			common.MethodNotAllowed(w)
			return
		}
		h.invokeTool(w, r, toolID)
		return
	}
	// 无子路径：PATCH 更新策略
	if r.Method != http.MethodPatch {
		common.MethodNotAllowed(w)
		return
	}
	h.updateToolPolicy(w, r, toolID)
}

// registerServer 处理 POST /api/mcp/servers：注册 Server，需 mcp.server.register 权限。
func (h *Handler) registerServer(w http.ResponseWriter, r *http.Request) {
	identity := auth.IdentityFromContext(r.Context())
	if appErr := auth.RequirePermission(identity, "mcp.server.register"); appErr != nil {
		common.WriteError(w, appErr)
		return
	}
	var req ServerRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		common.WriteError(w, common.BusinessError("invalid JSON body: "+err.Error()))
		return
	}
	if req.Name == "" || req.BaseURL == "" {
		common.WriteError(w, common.BusinessError("name 和 base_url 不能为空"))
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	svc := h.newService()
	server, err := svc.RegisterServer(r.Context(), req.Name, req.BaseURL, enabled)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toServerResponse(server))
}

// listServers 处理 GET /api/mcp/servers：列出全部 Server（含工具数）。
func (h *Handler) listServers(w http.ResponseWriter, r *http.Request) {
	svc := h.newService()
	items, err := svc.ListServers(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// updateServer 处理 PATCH /api/mcp/servers/{id}：更新 Server，需 mcp.server.register 权限。
func (h *Handler) updateServer(w http.ResponseWriter, r *http.Request, serverID string) {
	identity := auth.IdentityFromContext(r.Context())
	if appErr := auth.RequirePermission(identity, "mcp.server.register"); appErr != nil {
		common.WriteError(w, appErr)
		return
	}
	server, err := GetServerByUUID(r.Context(), h.db, serverID)
	if err != nil {
		writeServiceError(w, common.SystemError(err))
		return
	}
	if server == nil {
		common.WriteError(w, common.NotFound("MCP Server 不存在"))
		return
	}
	var req ServerUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		common.WriteError(w, common.BusinessError("invalid JSON body: "+err.Error()))
		return
	}
	svc := h.newService()
	updated, err := svc.UpdateServer(r.Context(), server, req.Name, req.BaseURL, req.Enabled)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toServerResponse(updated))
}

// deleteServer 处理 DELETE /api/mcp/servers/{id}：删除 Server，需 mcp.server.register 权限。
func (h *Handler) deleteServer(w http.ResponseWriter, r *http.Request, serverID string) {
	identity := auth.IdentityFromContext(r.Context())
	if appErr := auth.RequirePermission(identity, "mcp.server.register"); appErr != nil {
		common.WriteError(w, appErr)
		return
	}
	server, err := GetServerByUUID(r.Context(), h.db, serverID)
	if err != nil {
		writeServiceError(w, common.SystemError(err))
		return
	}
	if server == nil {
		common.WriteError(w, common.NotFound("MCP Server 不存在"))
		return
	}
	svc := h.newService()
	if err := svc.DeleteServer(r.Context(), server); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// refreshTools 处理 POST /api/mcp/servers/{id}/refresh：刷新工具 schema，需 mcp.tool.manage 权限。
func (h *Handler) refreshTools(w http.ResponseWriter, r *http.Request, serverID string) {
	identity := auth.IdentityFromContext(r.Context())
	if appErr := auth.RequirePermission(identity, "mcp.tool.manage"); appErr != nil {
		common.WriteError(w, appErr)
		return
	}
	server, err := GetServerByUUID(r.Context(), h.db, serverID)
	if err != nil {
		writeServiceError(w, common.SystemError(err))
		return
	}
	if server == nil {
		common.WriteError(w, common.NotFound("MCP Server 不存在"))
		return
	}
	svc := h.newService()
	tools, err := svc.RefreshTools(r.Context(), server)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	resp := make([]ToolResponse, 0, len(tools))
	for _, t := range tools {
		resp = append(resp, toToolResponse(t, server.UUID, server.Name))
	}
	writeJSON(w, http.StatusOK, resp)
}

// listTools 处理 GET /api/mcp/tools：列出全部工具。
func (h *Handler) listTools(w http.ResponseWriter, r *http.Request) {
	tools, err := ListTools(r.Context(), h.db)
	if err != nil {
		writeServiceError(w, common.SystemError(err))
		return
	}
	// 预加载全部 Server，避免逐工具查询
	servers, err := ListServers(r.Context(), h.db)
	if err != nil {
		writeServiceError(w, common.SystemError(err))
		return
	}
	serverByID := make(map[int64]*McpServer, len(servers))
	for _, s := range servers {
		serverByID[s.ID] = s
	}
	resp := make([]ToolResponse, 0, len(tools))
	for _, t := range tools {
		sv := serverByID[t.ServerID]
		uuid, name := "", ""
		if sv != nil {
			uuid, name = sv.UUID, sv.Name
		}
		resp = append(resp, toToolResponse(t, uuid, name))
	}
	writeJSON(w, http.StatusOK, resp)
}

// updateToolPolicy 处理 PATCH /api/mcp/tools/{id}：更新工具策略，需 mcp.tool.manage 权限。
func (h *Handler) updateToolPolicy(w http.ResponseWriter, r *http.Request, toolID string) {
	identity := auth.IdentityFromContext(r.Context())
	if appErr := auth.RequirePermission(identity, "mcp.tool.manage"); appErr != nil {
		common.WriteError(w, appErr)
		return
	}
	tool, err := GetToolByUUID(r.Context(), h.db, toolID)
	if err != nil {
		writeServiceError(w, common.SystemError(err))
		return
	}
	if tool == nil {
		common.WriteError(w, common.NotFound("工具不存在"))
		return
	}
	var req ToolPolicyUpdate
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		common.WriteError(w, common.BusinessError("invalid JSON body: "+err.Error()))
		return
	}
	if err := UpdateToolPolicy(r.Context(), h.db, tool.ID, req.Enabled, req.RequiresApproval, req.AllowedRoles, req.TimeoutSeconds); err != nil {
		writeServiceError(w, common.SystemError(err))
		return
	}
	updated, err := GetToolByUUID(r.Context(), h.db, toolID)
	if err != nil {
		writeServiceError(w, common.SystemError(err))
		return
	}
	server, _ := GetServerByID(r.Context(), h.db, updated.ServerID)
	uuid, name := "", ""
	if server != nil {
		uuid, name = server.UUID, server.Name
	}
	writeJSON(w, http.StatusOK, toToolResponse(updated, uuid, name))
}

// invokeTool 处理 POST /api/mcp/tools/{id}/invoke：调用工具，需 mcp.tool.call 权限。
func (h *Handler) invokeTool(w http.ResponseWriter, r *http.Request, toolID string) {
	identity := auth.IdentityFromContext(r.Context())
	if appErr := auth.RequirePermission(identity, "mcp.tool.call"); appErr != nil {
		common.WriteError(w, appErr)
		return
	}
	tool, err := GetToolByUUID(r.Context(), h.db, toolID)
	if err != nil {
		writeServiceError(w, common.SystemError(err))
		return
	}
	if tool == nil {
		common.WriteError(w, common.NotFound("工具不存在"))
		return
	}
	if !tool.Enabled {
		common.WriteError(w, common.BusinessError("工具未启用"))
		return
	}
	server, err := GetServerByID(r.Context(), h.db, tool.ServerID)
	if err != nil {
		writeServiceError(w, common.SystemError(err))
		return
	}
	if server == nil {
		common.WriteError(w, common.NotFound("MCP Server 不存在"))
		return
	}
	var req ToolInvokeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		common.WriteError(w, common.BusinessError("invalid JSON body: "+err.Error()))
		return
	}
	if req.Arguments == nil {
		req.Arguments = map[string]any{}
	}
	svc := h.newService()
	rec, err := svc.InvokeTool(r.Context(), tool, server, req.Arguments, identity.UserID, identity.Role, req.Confirmed)
	if err != nil {
		// 超时/失败/拒绝/限流均按对应 AppError 状态码响应（超时/失败为 500，
		// 对齐 Python system_error）。tool_calls 记录已在 service 内写入。
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ToolInvokeResponse{
		Status: rec.Status,
		Result: resultAsMap(rec.Output),
	})
}

// newService 创建 McpGatewayService 实例，注入 handler 持有的依赖（限流器为包级单例）。
func (h *Handler) newService() *McpGatewayService {
	return NewMcpGatewayService(h.db, h.audit)
}

// writeServiceError 将 service 返回的 error 转换为 HTTP 响应。
// AppError 按其 HTTPStatus 输出；其他错误视为系统内部错误。
func writeServiceError(w http.ResponseWriter, err error) {
	var appErr *common.AppError
	if errors.As(err, &appErr) {
		common.WriteError(w, appErr)
		return
	}
	common.WriteError(w, common.SystemError(err))
}

// writeJSON 写入 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
