// Package mcp_gateway 实现 MCP Server 注册、工具 schema 刷新、
// 授权策略与调用审计，对齐 Python app/mcp_gateway 模块。
//
// - models.go: McpServer / McpTool 实体与查询
// - schemas.go: 请求/响应 schema
// - service.go: 注册/刷新/限流/调用核心流程
// - router.go: HTTP 路由
package mcp_gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// McpServer MCP Server 注册记录，对应 mcp_servers 表。
type McpServer struct {
	ID        int64     `json:"id"`         // 主键ID（INT AUTO_INCREMENT）
	UUID      string    `json:"uuid"`       // 对外标识（CHAR(36)）
	Name      string    `json:"name"`       // 名称（VARCHAR(128) UNIQUE）
	BaseURL   string    `json:"base_url"`   // 地址（VARCHAR(1024)）
	Enabled   bool      `json:"enabled"`    // 是否启用
	CreatedAt time.Time `json:"created_at"` // 创建时间
}

// McpTool MCP 工具策略记录，对应 mcp_tools 表。
type McpTool struct {
	ID                int64           `json:"id"`                  // 主键ID
	UUID              string          `json:"uuid"`                // 对外标识
	ServerID          int64           `json:"server_id"`           // 所属 Server(mcp_servers.id)
	ToolName          string          `json:"tool_name"`           // 工具名
	Description       string          `json:"description"`         // 描述
	InputSchema       json.RawMessage `json:"input_schema"`        // 输入 schema（JSON）
	ReadOnly          bool            `json:"read_only"`           // 是否只读
	Destructive       bool            `json:"destructive"`         // 是否破坏性
	RequiresApproval  bool            `json:"requires_approval"`   // 是否需要二次确认
	Enabled           bool            `json:"enabled"`             // 是否启用
	AllowedRoles      []string        `json:"allowed_roles"`       // 允许角色（JSON 数组）
	TimeoutSeconds    int             `json:"timeout_seconds"`     // 超时(秒)
	RateLimit         string          `json:"rate_limit"`          // 限流配置（如 "60/minute"）
	ResultSizeLimit   int             `json:"result_size_limit"`   // 结果大小限制(字节)
	CreatedAt         time.Time       `json:"created_at"`          // 创建时间
	UpdatedAt         time.Time       `json:"updated_at"`          // 更新时间
}

// GetServerByUUID 按 uuid 查询 Server，不存在返回 (nil, nil)。
func GetServerByUUID(ctx context.Context, db *sql.DB, uuid string) (*McpServer, error) {
	const q = `SELECT id, uuid, name, base_url, enabled, created_at
		FROM mcp_servers WHERE uuid = ?`
	var s McpServer
	var enabled int8
	err := db.QueryRowContext(ctx, q, uuid).Scan(
		&s.ID, &s.UUID, &s.Name, &s.BaseURL, &enabled, &s.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query mcp_server by uuid: %w", err)
	}
	s.Enabled = enabled != 0
	return &s, nil
}

// GetServerByID 按 id 查询 Server，不存在返回 (nil, nil)。
func GetServerByID(ctx context.Context, db *sql.DB, id int64) (*McpServer, error) {
	const q = `SELECT id, uuid, name, base_url, enabled, created_at
		FROM mcp_servers WHERE id = ?`
	var s McpServer
	var enabled int8
	err := db.QueryRowContext(ctx, q, id).Scan(
		&s.ID, &s.UUID, &s.Name, &s.BaseURL, &enabled, &s.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query mcp_server by id: %w", err)
	}
	s.Enabled = enabled != 0
	return &s, nil
}

// GetServerByBaseURL 按 base_url 查询 Server，不存在返回 (nil, nil)。
// 用于注册时按地址去重（upsert 语义）。
func GetServerByBaseURL(ctx context.Context, db *sql.DB, baseURL string) (*McpServer, error) {
	const q = `SELECT id, uuid, name, base_url, enabled, created_at
		FROM mcp_servers WHERE base_url = ?`
	var s McpServer
	var enabled int8
	err := db.QueryRowContext(ctx, q, baseURL).Scan(
		&s.ID, &s.UUID, &s.Name, &s.BaseURL, &enabled, &s.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query mcp_server by base_url: %w", err)
	}
	s.Enabled = enabled != 0
	return &s, nil
}

// GetServerByName 按 name 查询 Server（排除指定 ID），不存在返回 (nil, nil)。
// 用于名称冲突校验：excludeID > 0 时排除该 ID 自身。
func GetServerByName(ctx context.Context, db *sql.DB, name string, excludeID int64) (*McpServer, error) {
	q := `SELECT id, uuid, name, base_url, enabled, created_at FROM mcp_servers WHERE name = ?`
	args := []any{name}
	if excludeID > 0 {
		q += ` AND id <> ?`
		args = append(args, excludeID)
	}
	var s McpServer
	var enabled int8
	err := db.QueryRowContext(ctx, q, args...).Scan(
		&s.ID, &s.UUID, &s.Name, &s.BaseURL, &enabled, &s.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query mcp_server by name: %w", err)
	}
	s.Enabled = enabled != 0
	return &s, nil
}

// ListServers 列出全部 Server，按 id 升序。
func ListServers(ctx context.Context, db *sql.DB) ([]*McpServer, error) {
	const q = `SELECT id, uuid, name, base_url, enabled, created_at
		FROM mcp_servers ORDER BY id ASC`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list mcp_servers: %w", err)
	}
	defer rows.Close()
	var out []*McpServer
	for rows.Next() {
		var s McpServer
		var enabled int8
		if err := rows.Scan(&s.ID, &s.UUID, &s.Name, &s.BaseURL, &enabled, &s.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan mcp_server: %w", err)
		}
		s.Enabled = enabled != 0
		out = append(out, &s)
	}
	return out, rows.Err()
}

// CountToolsByServer 按 server_id 分组统计工具数，返回 map[serverID]count。
// 用于 list_servers 响应中带 tool_count 字段。
func CountToolsByServer(ctx context.Context, db *sql.DB) (map[int64]int, error) {
	const q = `SELECT server_id, COUNT(*) FROM mcp_tools GROUP BY server_id`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("count tools by server: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]int)
	for rows.Next() {
		var sid int64
		var cnt int
		if err := rows.Scan(&sid, &cnt); err != nil {
			return nil, fmt.Errorf("scan tool count: %w", err)
		}
		out[sid] = cnt
	}
	return out, rows.Err()
}

// InsertServer 插入新 Server 记录，返回新主键 ID。
func InsertServer(ctx context.Context, db *sql.DB, name, baseURL string, enabled bool) (int64, error) {
	const q = `INSERT INTO mcp_servers (name, base_url, enabled) VALUES (?, ?, ?)`
	res, err := db.ExecContext(ctx, q, name, baseURL, enabled)
	if err != nil {
		return 0, fmt.Errorf("insert mcp_server: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get last insert id: %w", err)
	}
	return id, nil
}

// UpdateServerFields 更新 Server 名称/地址/启用状态。
// 仅当对应字段非空（字符串）或 needUpdateEnabled=true 时更新对应列。
func UpdateServerFields(
	ctx context.Context,
	db *sql.DB,
	id int64,
	newName, newBaseURL string,
	updateName, updateBaseURL bool,
	enabled bool,
	updateEnabled bool,
) error {
	if !updateName && !updateBaseURL && !updateEnabled {
		return nil
	}
	q := `UPDATE mcp_servers SET `
	args := []any{}
	sets := []string{}
	if updateName {
		sets = append(sets, "name = ?")
		args = append(args, newName)
	}
	if updateBaseURL {
		sets = append(sets, "base_url = ?")
		args = append(args, newBaseURL)
	}
	if updateEnabled {
		sets = append(sets, "enabled = ?")
		args = append(args, enabled)
	}
	q = q + joinSets(sets) + " WHERE id = ?"
	args = append(args, id)
	if _, err := db.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("update mcp_server: %w", err)
	}
	return nil
}

// joinSets 用逗号拼接 SET 子句片段。
func joinSets(sets []string) string {
	out := ""
	for i, s := range sets {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

// DeleteServerByID 删除 Server，同时清理其名下工具（无外键，手动级联）。
func DeleteServerByID(ctx context.Context, db *sql.DB, id int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM mcp_tools WHERE server_id = ?`, id); err != nil {
		return fmt.Errorf("delete mcp_tools by server: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM mcp_servers WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete mcp_server: %w", err)
	}
	return tx.Commit()
}

// GetToolByUUID 按 uuid 查询 Tool，不存在返回 (nil, nil)。
func GetToolByUUID(ctx context.Context, db *sql.DB, uuid string) (*McpTool, error) {
	const q = `SELECT id, uuid, server_id, tool_name, description, input_schema,
		read_only, destructive, requires_approval, enabled, allowed_roles,
		timeout_seconds, rate_limit, result_size_limit, created_at, updated_at
		FROM mcp_tools WHERE uuid = ?`
	t, err := scanTool(db.QueryRowContext(ctx, q, uuid))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query mcp_tool by uuid: %w", err)
	}
	return t, nil
}

// GetToolByServerAndName 按 (server_id, tool_name) 查询 Tool，不存在返回 (nil, nil)。
// 用于 refresh_tools 时的 upsert 命中判断。
func GetToolByServerAndName(ctx context.Context, db *sql.DB, serverID int64, toolName string) (*McpTool, error) {
	const q = `SELECT id, uuid, server_id, tool_name, description, input_schema,
		read_only, destructive, requires_approval, enabled, allowed_roles,
		timeout_seconds, rate_limit, result_size_limit, created_at, updated_at
		FROM mcp_tools WHERE server_id = ? AND tool_name = ?`
	t, err := scanTool(db.QueryRowContext(ctx, q, serverID, toolName))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query mcp_tool by server+name: %w", err)
	}
	return t, nil
}

// ListTools 列出全部工具，按 id 升序。
func ListTools(ctx context.Context, db *sql.DB) ([]*McpTool, error) {
	const q = `SELECT id, uuid, server_id, tool_name, description, input_schema,
		read_only, destructive, requires_approval, enabled, allowed_roles,
		timeout_seconds, rate_limit, result_size_limit, created_at, updated_at
		FROM mcp_tools ORDER BY id ASC`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list mcp_tools: %w", err)
	}
	defer rows.Close()
	var out []*McpTool
	for rows.Next() {
		t, err := scanTool(rows)
		if err != nil {
			return nil, fmt.Errorf("scan mcp_tool: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListToolsByServer 列出指定 Server 下的全部工具，用于 refresh_tools 清理。
func ListToolsByServer(ctx context.Context, db *sql.DB, serverID int64) ([]*McpTool, error) {
	const q = `SELECT id, uuid, server_id, tool_name, description, input_schema,
		read_only, destructive, requires_approval, enabled, allowed_roles,
		timeout_seconds, rate_limit, result_size_limit, created_at, updated_at
		FROM mcp_tools WHERE server_id = ? ORDER BY id ASC`
	rows, err := db.QueryContext(ctx, q, serverID)
	if err != nil {
		return nil, fmt.Errorf("list mcp_tools by server: %w", err)
	}
	defer rows.Close()
	var out []*McpTool
	for rows.Next() {
		t, err := scanTool(rows)
		if err != nil {
			return nil, fmt.Errorf("scan mcp_tool: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListEnabledTools 列出全部启用的工具，按 id 升序。
// 用于 ChatService 构建 agent loop 的可用工具列表。
func ListEnabledTools(ctx context.Context, db *sql.DB) ([]*McpTool, error) {
	const q = `SELECT id, uuid, server_id, tool_name, description, input_schema,
		read_only, destructive, requires_approval, enabled, allowed_roles,
		timeout_seconds, rate_limit, result_size_limit, created_at, updated_at
		FROM mcp_tools WHERE enabled = 1 ORDER BY id ASC`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list enabled mcp_tools: %w", err)
	}
	defer rows.Close()
	var out []*McpTool
	for rows.Next() {
		t, err := scanTool(rows)
		if err != nil {
			return nil, fmt.Errorf("scan mcp_tool: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// scanner 适配 *sql.Row 与 *sql.Rows 的 Scan 接口。
type scanner interface {
	Scan(dest ...any) error
}

// scanTool 从 scanner 扫描一条 Tool 记录。
// allowed_roles 为 JSON 数组，需手动反序列化为 []string。
func scanTool(s scanner) (*McpTool, error) {
	var t McpTool
	var readOnly, destructive, requiresApproval, enabled int8
	var allowedRolesJSON []byte
	if err := s.Scan(
		&t.ID, &t.UUID, &t.ServerID, &t.ToolName, &t.Description, &t.InputSchema,
		&readOnly, &destructive, &requiresApproval, &enabled, &allowedRolesJSON,
		&t.TimeoutSeconds, &t.RateLimit, &t.ResultSizeLimit, &t.CreatedAt, &t.UpdatedAt,
	); err != nil {
		return nil, err
	}
	t.ReadOnly = readOnly != 0
	t.Destructive = destructive != 0
	t.RequiresApproval = requiresApproval != 0
	t.Enabled = enabled != 0
	if len(allowedRolesJSON) > 0 {
		_ = json.Unmarshal(allowedRolesJSON, &t.AllowedRoles)
	}
	if t.AllowedRoles == nil {
		t.AllowedRoles = []string{}
	}
	return &t, nil
}

// InsertTool 插入新 Tool 记录，返回新主键 ID。
// allowedRoles 序列化为 JSON 写入 allowed_roles 列。
func InsertTool(
	ctx context.Context,
	db *sql.DB,
	serverID int64,
	toolName, description string,
	inputSchema json.RawMessage,
	readOnly bool,
	allowedRoles []string,
) (int64, error) {
	rolesJSON, _ := json.Marshal(allowedRoles)
	const q = `INSERT INTO mcp_tools
		(server_id, tool_name, description, input_schema, read_only, allowed_roles)
		VALUES (?, ?, ?, ?, ?, ?)`
	res, err := db.ExecContext(ctx, q, serverID, toolName, description, inputSchema, readOnly, rolesJSON)
	if err != nil {
		return 0, fmt.Errorf("insert mcp_tool: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get last insert id: %w", err)
	}
	return id, nil
}

// UpdateToolDescription 更新工具描述（refresh_tools 命中已有工具时使用）。
func UpdateToolDescription(ctx context.Context, db *sql.DB, id int64, description string) error {
	const q = `UPDATE mcp_tools SET description = ? WHERE id = ?`
	if _, err := db.ExecContext(ctx, q, description, id); err != nil {
		return fmt.Errorf("update mcp_tool description: %w", err)
	}
	return nil
}

// DeleteToolByID 按 id 删除工具（refresh_tools 清理已下架工具时使用）。
func DeleteToolByID(ctx context.Context, db *sql.DB, id int64) error {
	const q = `DELETE FROM mcp_tools WHERE id = ?`
	if _, err := db.ExecContext(ctx, q, id); err != nil {
		return fmt.Errorf("delete mcp_tool: %w", err)
	}
	return nil
}

// UpdateToolPolicy 更新工具策略字段。
// 仅当对应 want 标志为 true 时更新对应列；字符串字段空值也允许更新（如 allowed_roles 清空）。
func UpdateToolPolicy(
	ctx context.Context,
	db *sql.DB,
	id int64,
	enabled *bool,
	requiresApproval *bool,
	allowedRoles []string,
	timeoutSeconds *int,
) error {
	sets := []string{}
	args := []any{}
	if enabled != nil {
		sets = append(sets, "enabled = ?")
		args = append(args, *enabled)
	}
	if requiresApproval != nil {
		sets = append(sets, "requires_approval = ?")
		args = append(args, *requiresApproval)
	}
	if allowedRoles != nil {
		rolesJSON, _ := json.Marshal(allowedRoles)
		sets = append(sets, "allowed_roles = ?")
		args = append(args, rolesJSON)
	}
	if timeoutSeconds != nil {
		sets = append(sets, "timeout_seconds = ?")
		args = append(args, *timeoutSeconds)
	}
	if len(sets) == 0 {
		return nil
	}
	q := `UPDATE mcp_tools SET ` + joinSets(sets) + ` WHERE id = ?`
	args = append(args, id)
	if _, err := db.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("update mcp_tool policy: %w", err)
	}
	return nil
}

// ToolCallRecord 工具调用记录，对应 tool_calls 表。
// 由 mcp_gateway 写入，chat/rag 模块会回填 message_id 关联。
type ToolCallRecord struct {
	ID         int64          `json:"id"`          // 主键ID
	UUID       string         `json:"uuid"`        // 对外标识
	MessageID  sql.NullInt64  `json:"message_id"`  // 关联消息ID（可空）
	UserID     int64          `json:"user_id"`     // 调用用户(users.id)
	ToolName   string         `json:"tool_name"`   // 工具名
	ServerID   int64          `json:"server_id"`   // MCP Server ID
	Input      json.RawMessage `json:"input"`      // 输入参数（JSON，可空）
	Output     json.RawMessage `json:"output"`     // 输出结果摘要（JSON，可空）
	Status     string         `json:"status"`      // 状态：running/success/failed/timeout
	Error      string         `json:"error"`       // 错误信息（可空）
	DurationMs sql.NullInt64  `json:"duration_ms"` // 耗时(毫秒，可空)
	CreatedAt  time.Time      `json:"created_at"`  // 创建时间
}

// InsertToolCall 插入工具调用记录，返回新主键 ID 与生成的 uuid。
func InsertToolCall(ctx context.Context, db *sql.DB, rec *ToolCallRecord) (int64, string, error) {
	callUUID := uuid.New().String()
	const q = `INSERT INTO tool_calls
		(uuid, message_id, user_id, tool_name, server_id, input, output, status, error, duration_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	res, err := db.ExecContext(ctx, q,
		callUUID, rec.MessageID, rec.UserID, rec.ToolName, rec.ServerID,
		nullJSON(rec.Input), nullJSON(rec.Output), rec.Status, nullStr(rec.Error), rec.DurationMs)
	if err != nil {
		return 0, "", fmt.Errorf("insert tool_call: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, "", fmt.Errorf("get last insert id: %w", err)
	}
	return id, callUUID, nil
}

// UpdateToolCallResult 更新工具调用结果（成功时 status=success, output 写入）。
func UpdateToolCallResult(ctx context.Context, db *sql.DB, id int64, status string, output json.RawMessage, errMsg string, durationMs int) error {
	const q = `UPDATE tool_calls SET status = ?, output = ?, error = ?, duration_ms = ? WHERE id = ?`
	if _, err := db.ExecContext(ctx, q, status, nullJSON(output), nullStr(errMsg), durationMs, id); err != nil {
		return fmt.Errorf("update tool_call result: %w", err)
	}
	return nil
}

// nullStr 空串转为 sql.NullString 的 NULL，便于 INSERT/UPDATE 写可空列。
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullJSON 空 json.RawMessage 转为 NULL，便于 INSERT/UPDATE 写 JSON 列。
func nullJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
