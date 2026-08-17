// File schemas.go: MCP Gateway 请求/响应 schema，对齐 Python app/mcp_gateway/schemas.py。
package mcp_gateway

import (
	"encoding/json"
	"time"
)

// ServerRegisterRequest 注册 MCP Server 请求体。
type ServerRegisterRequest struct {
	Name    string `json:"name"`     // 名称
	BaseURL string `json:"base_url"` // 地址
	Enabled *bool  `json:"enabled"`  // 是否启用，nil 时默认 true
}

// ServerUpdateRequest 更新 MCP Server 请求体，字段 nil 表示不改。
type ServerUpdateRequest struct {
	Name    *string `json:"name"`     // 名称
	BaseURL *string `json:"base_url"` // 地址
	Enabled *bool   `json:"enabled"`  // 是否启用
}

// ServerResponse MCP Server 响应。
type ServerResponse struct {
	ID        string    `json:"id"`         // server.uuid
	Name      string    `json:"name"`       // 名称
	BaseURL   string    `json:"base_url"`   // 地址
	Enabled   bool      `json:"enabled"`    // 是否启用
	CreatedAt time.Time `json:"created_at"` // 创建时间
}

// ServerListItem MCP Server 列表项（含工具数）。
type ServerListItem struct {
	ID         string    `json:"id"`          // server.uuid
	Name       string    `json:"name"`        // 名称
	BaseURL    string    `json:"base_url"`    // 地址
	Enabled    bool      `json:"enabled"`     // 是否启用
	CreatedAt  time.Time `json:"created_at"`  // 创建时间
	ToolCount  int       `json:"tool_count"`  // 工具数
}

// ToolResponse MCP 工具响应。
type ToolResponse struct {
	ID                string   `json:"id"`                  // tool.uuid
	ServerID          string   `json:"server_id"`           // 所属 server.uuid
	ServerName        string   `json:"server_name"`         // 所属 server.name
	ToolName          string   `json:"tool_name"`           // 工具名
	Description       string   `json:"description"`         // 描述
	RequiresApproval  bool     `json:"requires_approval"`   // 是否需要二次确认
	Enabled           bool     `json:"enabled"`             // 是否启用
	AllowedRoles      []string `json:"allowed_roles"`       // 允许角色
	TimeoutSeconds    int      `json:"timeout_seconds"`     // 超时(秒)
}

// ToolPolicyUpdate 工具策略更新请求，字段 nil 表示不改。
type ToolPolicyUpdate struct {
	Enabled          *bool    `json:"enabled"`           // 是否启用
	RequiresApproval *bool    `json:"requires_approval"` // 是否需要二次确认
	AllowedRoles     []string `json:"allowed_roles"`     // 允许角色（nil 不改，空切片清空）
	TimeoutSeconds   *int     `json:"timeout_seconds"`   // 超时(秒)
}

// ToolInvokeRequest 工具调用请求。
type ToolInvokeRequest struct {
	Arguments map[string]any `json:"arguments"` // 调用参数
	Confirmed bool           `json:"confirmed"` // 是否已二次确认
}

// ToolInvokeResponse 工具调用响应，结果为摘要，不泄露凭证/连接串。
type ToolInvokeResponse struct {
	Status  string         `json:"status"`         // 状态：success/failed/timeout
	Result  map[string]any `json:"result,omitempty"` // 输出结果（成功时）
	Message string         `json:"message,omitempty"` // 错误信息（失败时）
}

// toServerResponse 将 McpServer 转换为响应。
func toServerResponse(s *McpServer) ServerResponse {
	return ServerResponse{
		ID:        s.UUID,
		Name:      s.Name,
		BaseURL:   s.BaseURL,
		Enabled:   s.Enabled,
		CreatedAt: s.CreatedAt,
	}
}

// toToolResponse 将 McpTool 转换为响应，需要所属 Server 的对外 uuid 与 name。
func toToolResponse(t *McpTool, serverUUID, serverName string) ToolResponse {
	roles := t.AllowedRoles
	if roles == nil {
		roles = []string{}
	}
	return ToolResponse{
		ID:                t.UUID,
		ServerID:          serverUUID,
		ServerName:        serverName,
		ToolName:          t.ToolName,
		Description:       t.Description,
		RequiresApproval:  t.RequiresApproval,
		Enabled:           t.Enabled,
		AllowedRoles:      roles,
		TimeoutSeconds:    t.TimeoutSeconds,
	}
}

// marshalArgs 将 arguments map 序列化为 json.RawMessage。
// 空字典写 "{}" 而非 NULL，对齐 Python arguments 默认 {}（M23）。
func marshalArgs(args map[string]any) json.RawMessage {
	if len(args) == 0 {
		return json.RawMessage("{}")
	}
	data, _ := json.Marshal(args)
	return data
}

// resultAsMap 将 json.RawMessage 反序列化为 map[string]any。
// 用于 tool_calls.output 读取后构造响应。
func resultAsMap(b json.RawMessage) map[string]any {
	if len(b) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	return m
}
