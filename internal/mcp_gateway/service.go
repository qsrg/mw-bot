// File service.go: MCP Gateway 核心服务，对齐 Python app/mcp_gateway/service.py。
//
// 职责：
//   - Server 注册/更新/删除（upsert by base_url、名称冲突校验、级联清理工具）
//   - 工具 schema 刷新（拉取 → upsert → 清理已下架）
//   - 工具调用授权（启用 / 角色 / 二次确认）
//   - 简单固定窗口限流
//   - 调用 MCP Server /tools/{name}，结果大小限制，写 tool_calls 表与审计事件
package mcp_gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mw-bot/internal/audit"
	"mw-bot/internal/common"
)

// refreshTimeout 拉取工具列表的固定超时。
const refreshTimeout = 10 * time.Second

// errToolTimeout 工具调用超时哨兵错误，供 InvokeTool 区分超时与一般失败（M22）。
var errToolTimeout = errors.New("tool invoke timeout")

// rateBucket 限流桶：固定窗口（起始时间 + 累计计数）。
type rateBucket struct {
	startTime time.Time // 窗口起始时间
	count     int       // 当前窗口累计请求数
}

// rateLimiter 限流器，跨请求共享（tool_id -> 桶）。
// 必须为长生命周期单例：McpGatewayService 每请求新建，但限流状态需跨请求累积，
// 否则限流形同虚设（C1）。对齐 Python 的类级 _rate_buckets（进程全局），
// 这里用包级单例，HTTP invoke 与 chat 工具调用两条路径共享同一份限流状态。
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[int64]*rateBucket
}

// defaultRateLimiter 包级限流单例，所有 McpGatewayService 实例共享。
var defaultRateLimiter = &rateLimiter{buckets: make(map[int64]*rateBucket)}

// McpGatewayService MCP 工具网关，集中管理策略、授权与调用审计。
type McpGatewayService struct {
	db     *sql.DB
	audit  *audit.AuditService
	client *http.Client
	rl     *rateLimiter // 跨请求共享的限流器（默认包级单例）
}

// NewMcpGatewayService 创建网关服务。
//
// 参数：
//   - db: 已就绪的 MySQL 连接池。
//   - auditSvc: 审计服务。
func NewMcpGatewayService(db *sql.DB, auditSvc *audit.AuditService) *McpGatewayService {
	return &McpGatewayService{
		db:     db,
		audit:  auditSvc,
		client: &http.Client{Timeout: refreshTimeout},
		rl:     defaultRateLimiter,
	}
}

// CanCallTool 校验工具是否可被指定角色调用。
// 条件：启用 + 角色在 allowed_roles 中 + (requires_approval → confirmed)。
func (s *McpGatewayService) CanCallTool(tool *McpTool, userRole string, confirmed bool) bool {
	if !tool.Enabled {
		return false
	}
	if !contains(tool.AllowedRoles, userRole) {
		return false
	}
	if tool.RequiresApproval && !confirmed {
		return false
	}
	return true
}

// RegisterServer 注册 MCP Server。
// 同一 base_url 复用已有记录（upsert）：命中已有地址则更新名称与启用状态，否则新建。
// 名称被其他 Server 占用时返回业务错误。
func (s *McpGatewayService) RegisterServer(ctx context.Context, name, baseURL string, enabled bool) (*McpServer, error) {
	normalized := NormalizeBaseURL(baseURL)
	existing, err := GetServerByBaseURL(ctx, s.db, normalized)
	if err != nil {
		return nil, common.SystemError(err)
	}
	if existing != nil {
		// base_url 命中：校验 name 是否被其他 Server 占用
		owner, err := GetServerByName(ctx, s.db, name, existing.ID)
		if err != nil {
			return nil, common.SystemError(err)
		}
		if owner != nil {
			return nil, common.BusinessError("名称「" + name + "」已被其他 Server 占用，请更换名称")
		}
		if err := UpdateServerFields(ctx, s.db, existing.ID,
			name, normalized, true, true, enabled, true); err != nil {
			return nil, common.SystemError(err)
		}
		// 重新查询返回最新数据
		return GetServerByID(ctx, s.db, existing.ID)
	}
	// 新地址：校验 name 唯一
	owner, err := GetServerByName(ctx, s.db, name, 0)
	if err != nil {
		return nil, common.SystemError(err)
	}
	if owner != nil {
		return nil, common.BusinessError("名称「" + name + "」已被占用，请更换名称")
	}
	id, err := InsertServer(ctx, s.db, name, normalized, enabled)
	if err != nil {
		return nil, common.SystemError(err)
	}
	return GetServerByID(ctx, s.db, id)
}

// ListServers 列出全部 Server（含工具数），按 id 升序。
func (s *McpGatewayService) ListServers(ctx context.Context) ([]ServerListItem, error) {
	servers, err := ListServers(ctx, s.db)
	if err != nil {
		return nil, common.SystemError(err)
	}
	counts, err := CountToolsByServer(ctx, s.db)
	if err != nil {
		return nil, common.SystemError(err)
	}
	out := make([]ServerListItem, 0, len(servers))
	for _, sv := range servers {
		out = append(out, ServerListItem{
			ID:        sv.UUID,
			Name:      sv.Name,
			BaseURL:   sv.BaseURL,
			Enabled:   sv.Enabled,
			CreatedAt: sv.CreatedAt,
			ToolCount: counts[sv.ID],
		})
	}
	return out, nil
}

// UpdateServer 更新 Server 名称/地址/启用状态。
// 字段 nil 表示不改；名称或地址与其他 Server 冲突时返回业务错误。
func (s *McpGatewayService) UpdateServer(
	ctx context.Context,
	server *McpServer,
	name, baseURL *string,
	enabled *bool,
) (*McpServer, error) {
	updateName := false
	updateBaseURL := false
	newName := server.Name
	newBaseURL := server.BaseURL
	if name != nil && *name != server.Name {
		// 名称变更：校验冲突
		owner, err := GetServerByName(ctx, s.db, *name, server.ID)
		if err != nil {
			return nil, common.SystemError(err)
		}
		if owner != nil {
			return nil, common.BusinessError("名称「" + *name + "」已被其他 Server 占用，请更换名称")
		}
		newName = *name
		updateName = true
	}
	if baseURL != nil {
		normalized := NormalizeBaseURL(*baseURL)
		if normalized != server.BaseURL {
			owner, err := GetServerByBaseURL(ctx, s.db, normalized)
			if err != nil {
				return nil, common.SystemError(err)
			}
			if owner != nil && owner.ID != server.ID {
				return nil, common.BusinessError("地址「" + normalized + "」已被其他 Server 占用")
			}
			newBaseURL = normalized
			updateBaseURL = true
		}
	}
	updateEnabled := enabled != nil
	newEnabled := server.Enabled
	if updateEnabled {
		newEnabled = *enabled
	}
	if err := UpdateServerFields(ctx, s.db, server.ID,
		newName, newBaseURL, updateName, updateBaseURL,
		newEnabled, updateEnabled); err != nil {
		return nil, common.SystemError(err)
	}
	return GetServerByID(ctx, s.db, server.ID)
}

// DeleteServer 删除 Server 并级联清理其名下工具。
func (s *McpGatewayService) DeleteServer(ctx context.Context, server *McpServer) error {
	if err := DeleteServerByID(ctx, s.db, server.ID); err != nil {
		return common.SystemError(err)
	}
	return nil
}

// RefreshTools 从 Server 拉取工具列表并刷新本地工具记录，清理已下架工具。
// 返回刷新后的工具列表。
func (s *McpGatewayService) RefreshTools(ctx context.Context, server *McpServer) ([]*McpTool, error) {
	toolsData, err := s.fetchToolsList(ctx, server.BaseURL)
	if err != nil {
		return nil, common.SystemError(err)
	}
	currentNames := make(map[string]struct{}, len(toolsData))
	for _, item := range toolsData {
		currentNames[item.Name] = struct{}{}
	}
	upserted := make([]*McpTool, 0, len(toolsData))
	for _, item := range toolsData {
		t, err := s.upsertTool(ctx, server.ID, item)
		if err != nil {
			return nil, err
		}
		upserted = append(upserted, t)
	}
	// 自动清理已下架工具：currentNames 为空时跳过，避免瞬时返回空列表误删全部
	if len(currentNames) > 0 {
		existing, err := ListToolsByServer(ctx, s.db, server.ID)
		if err != nil {
			return nil, common.SystemError(err)
		}
		for _, t := range existing {
			if _, ok := currentNames[t.ToolName]; !ok {
				slog.InfoContext(ctx, "清理已下架工具",
					"server", server.Name, "tool", t.ToolName)
				if err := DeleteToolByID(ctx, s.db, t.ID); err != nil {
					return nil, common.SystemError(err)
				}
			}
		}
	}
	return upserted, nil
}

// fetchToolItem MCP Server /tools 返回的单个工具 schema。
type fetchToolItem struct {
	Name        string          `json:"name"`         // 工具名
	Description string          `json:"description"`  // 描述
	InputSchema json.RawMessage `json:"input_schema"` // 输入 schema
	ReadOnly    *bool           `json:"read_only"`    // 是否只读（缺省按 true，对齐 Python）
}

// fetchToolsList 调用 MCP Server GET /tools 拉取工具列表。
func (s *McpGatewayService) fetchToolsList(ctx context.Context, baseURL string) ([]fetchToolItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/tools", nil)
	if err != nil {
		return nil, fmt.Errorf("create /tools request: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call /tools: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("/tools status %d: %s", resp.StatusCode, string(body))
	}
	var parsed struct {
		Tools []fetchToolItem `json:"tools"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode /tools response: %w", err)
	}
	return parsed.Tools, nil
}

// upsertTool 工具记录 upsert：(server_id, tool_name) 命中则更新 description，否则新建。
// 新建时默认 enabled=false, allowed_roles=["admin"]，与 Python 行为一致。
func (s *McpGatewayService) upsertTool(ctx context.Context, serverID int64, item fetchToolItem) (*McpTool, error) {
	existing, err := GetToolByServerAndName(ctx, s.db, serverID, item.Name)
	if err != nil {
		return nil, common.SystemError(err)
	}
	if existing != nil {
		// 命中：仅更新 description（保留策略字段不变）
		desc := item.Description
		if desc == "" {
			desc = existing.Description
		}
		if err := UpdateToolDescription(ctx, s.db, existing.ID, desc); err != nil {
			return nil, common.SystemError(err)
		}
		return GetToolByServerAndName(ctx, s.db, serverID, item.Name)
	}
	// 新建
	inputSchema := item.InputSchema
	if len(inputSchema) == 0 {
		inputSchema = json.RawMessage(`{}`)
	}
	desc := item.Description
	if desc == "" {
		desc = ""
	}
	// read_only 缺省按 true（对齐 Python item.get("read_only", True)）
	readOnly := true
	if item.ReadOnly != nil {
		readOnly = *item.ReadOnly
	}
	if _, err := InsertTool(ctx, s.db, serverID, item.Name, desc, inputSchema, readOnly, []string{"admin"}); err != nil {
		return nil, common.SystemError(err)
	}
	return GetToolByServerAndName(ctx, s.db, serverID, item.Name)
}

// InvokeTool 授权并调用 MCP 工具，返回调用记录与调用结果。
//
// 流程：
//  1. CanCallTool 校验（拒绝写 mcp_tool_denied 审计）
//  2. 限流校验（触发限流返回业务错误）
//  3. 插入 tool_calls 记录（status=running）
//  4. POST {base_url}/tools/{tool_name}，超时按 tool.timeout_seconds
//  5. 结果大小限制：JSON 编码后超过 result_size_limit 则替换为截断占位
//  6. 更新 tool_calls 为 success/timeout/failed，写 mcp_tool_called 审计（成功时）
//
// 参数：
//   - tool: 工具实例
//   - server: 所属 Server
//   - arguments: 调用参数
//   - userID: 调用用户ID
//   - userRole: 调用用户角色
//   - confirmed: 是否已二次确认
//
// 返回：
//   - *ToolCallRecord: 调用记录（含最终 status/output/error/duration）
//   - error: 调用失败（已写审计/更新 tool_calls），AppError 类型供 router 直接响应
func (s *McpGatewayService) InvokeTool(
	ctx context.Context,
	tool *McpTool,
	server *McpServer,
	arguments map[string]any,
	userID int64,
	userRole string,
	confirmed bool,
) (*ToolCallRecord, error) {
	requestID := common.RequestIDFromContext(ctx)

	// 授权校验
	if !s.CanCallTool(tool, userRole, confirmed) {
		s.audit.RecordEvent(ctx, audit.AuditEvent{
			EventType:    "mcp_tool_denied",
			ActorUserID:  sql.NullInt64{Int64: userID, Valid: userID > 0},
			ActorRole:    sql.NullString{String: userRole, Valid: userRole != ""},
			RequestID:    requestID,
			ResourceType: sql.NullString{String: "mcp_tool", Valid: true},
			ResourceID:   sql.NullString{String: tool.UUID, Valid: true},
			Action:       sql.NullString{String: "invoke", Valid: true},
			Status:       sql.NullString{String: "denied", Valid: true},
			Metadata:     marshalJSON(map[string]any{"tool_name": tool.ToolName}),
		})
		return nil, common.Forbidden("无权限调用该工具或未完成二次确认")
	}

	// 限流校验
	if !s.checkRateLimit(tool) {
		return nil, common.BusinessError("工具调用触发限流，请稍后重试")
	}

	// 插入 running 记录
	start := time.Now()
	rec := &ToolCallRecord{
		UserID:   userID,
		ToolName: tool.ToolName,
		ServerID: server.ID,
		Input:    marshalArgs(arguments),
		Status:   "running",
	}
	callID, _, err := InsertToolCall(ctx, s.db, rec)
	if err != nil {
		return nil, common.SystemError(err)
	}
	rec.ID = callID

	// 实际调用
	result, callErr := s.callRemoteTool(ctx, server.BaseURL, tool, arguments)
	durationMs := int(time.Since(start).Milliseconds())

	if callErr != nil {
		// 区分超时与其他失败：callRemoteTool 超时时返回 errToolTimeout 哨兵错误（M22）
		status := "failed"
		errMsg := "工具调用失败"
		if errors.Is(callErr, errToolTimeout) {
			status = "timeout"
			errMsg = "工具调用超时"
		}
		_ = UpdateToolCallResult(ctx, s.db, callID, status, nil, errMsg, durationMs)
		rec.Status = status
		rec.Error = errMsg
		rec.DurationMs = sql.NullInt64{Int64: int64(durationMs), Valid: true}
		if status == "timeout" {
			return rec, common.SystemErrorWithMessage("工具调用超时")
		}
		return rec, common.SystemErrorWithMessage("工具调用失败")
	}

	// 结果大小限制：按 Unicode 字符数计（对齐 Python len(json.dumps)），
	// 避免按字节计对中文结果过度截断。
	resultJSON, _ := json.Marshal(result)
	if utf8.RuneCount(resultJSON) > tool.ResultSizeLimit {
		result = map[string]any{
			"summary":   "结果超出大小限制，已截断",
			"truncated": true,
		}
		resultJSON, _ = json.Marshal(result)
	}

	// 更新 tool_calls 为 success
	if err := UpdateToolCallResult(ctx, s.db, callID, "success", resultJSON, "", durationMs); err != nil {
		return nil, common.SystemError(err)
	}
	rec.Status = "success"
	rec.Output = resultJSON
	rec.DurationMs = sql.NullInt64{Int64: int64(durationMs), Valid: true}

	// 写 mcp_tool_called 审计
	s.audit.RecordEvent(ctx, audit.AuditEvent{
		EventType:    "mcp_tool_called",
		ActorUserID:  sql.NullInt64{Int64: userID, Valid: userID > 0},
		ActorRole:    sql.NullString{String: userRole, Valid: userRole != ""},
		RequestID:    requestID,
		ResourceType: sql.NullString{String: "mcp_tool", Valid: true},
		ResourceID:   sql.NullString{String: tool.UUID, Valid: true},
		Action:       sql.NullString{String: "invoke", Valid: true},
		Status:       sql.NullString{String: "success", Valid: true},
		Metadata:     marshalJSON(map[string]any{"tool_name": tool.ToolName, "duration_ms": durationMs}),
	})
	return rec, nil
}

// callRemoteTool 调用 MCP Server POST /tools/{tool_name}。
// 超时按 tool.TimeoutSeconds 设置独立 http.Client（不与 refresh 共享 client）。
// 返回 result 与 error；超时返回带"超时"关键词的 SystemError 以便上层区分。
func (s *McpGatewayService) callRemoteTool(ctx context.Context, baseURL string, tool *McpTool, arguments map[string]any) (map[string]any, error) {
	timeout := time.Duration(tool.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	client := &http.Client{Timeout: timeout}

	body, err := json.Marshal(map[string]any{"arguments": arguments})
	if err != nil {
		return nil, fmt.Errorf("marshal arguments: %w", err)
	}
	url := baseURL + "/tools/" + tool.ToolName
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create invoke request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		// 超时识别：net/http 在 client.Timeout 触发时返回实现了 net.Error 接口的错误
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return nil, errToolTimeout
		}
		return nil, fmt.Errorf("call invoke: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("invoke status %d: %s", resp.StatusCode, string(respBody))
	}
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode invoke response: %w", err)
	}
	return result, nil
}

// checkRateLimit 简单固定窗口限流校验。
// 桶 key 为 tool.ID；窗口内计数超 limit 时返回 false。状态跨请求共享（s.rl）。
func (s *McpGatewayService) checkRateLimit(tool *McpTool) bool {
	limit, window := ParseRateLimit(tool.RateLimit)
	now := time.Now()
	s.rl.mu.Lock()
	defer s.rl.mu.Unlock()
	bucket, ok := s.rl.buckets[tool.ID]
	if !ok || now.Sub(bucket.startTime) >= window {
		s.rl.buckets[tool.ID] = &rateBucket{startTime: now, count: 1}
		return true
	}
	if bucket.count >= limit {
		return false
	}
	bucket.count++
	return true
}

// NormalizeBaseURL 归一化 Server 地址：去首尾空白与尾部斜杠，便于按地址去重。
func NormalizeBaseURL(baseURL string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/")
}

// ParseRateLimit 解析 '60/minute' 格式限流配置。
// 返回 (次数上限, 窗口秒数)；解析失败回退到 (60, 60s)。
func ParseRateLimit(rateLimit string) (int, time.Duration) {
	parts := strings.Split(rateLimit, "/")
	if len(parts) != 2 {
		return 60, 60 * time.Second
	}
	count, err := strconv.Atoi(parts[0])
	if err != nil {
		return 60, 60 * time.Second
	}
	var windowSec int
	switch parts[1] {
	case "second":
		windowSec = 1
	case "minute":
		windowSec = 60
	case "hour":
		windowSec = 3600
	default:
		windowSec = 60
	}
	return count, time.Duration(windowSec) * time.Second
}

// contains 判断字符串切片是否包含指定值。
func contains(s []string, v string) bool {
	for _, item := range s {
		if item == v {
			return true
		}
	}
	return false
}

// marshalJSON 序列化为 json.RawMessage，失败返回 nil（写入 NULL）。
func marshalJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}
