// Package audit 实现审计事件写入服务，覆盖登录、上传、索引、问答、检索、
// 模型推断、MCP 工具调用与记忆变更等关键动作。所有写入失败仅记录日志，
// 不阻塞业务流程，与 Python app/audit 模块语义对齐。
package audit

import (
	"database/sql"
	"encoding/json"
	"time"
)

// AuditEvent 对应数据库 audit_events 表的一行记录。
// 字段类型与 db/migrations/001_init_schema.sql 中 audit_events 列定义对齐：
// id、uuid、created_at 三列由数据库默认值填充（AUTO_INCREMENT / DEFAULT (UUID()) /
// DEFAULT CURRENT_TIMESTAMP），INSERT 时不显式指定。
type AuditEvent struct {
	ID           int64            `json:"id"`            // 事件ID（主键，BIGINT AUTO_INCREMENT）
	EventType    string           `json:"event_type"`    // 事件类型，如 login_success、document_uploaded
	ActorUserID  sql.NullInt64    `json:"actor_user_id"` // 操作者ID(users.id)，登录失败时为 NULL
	ActorRole    sql.NullString   `json:"actor_role"`    // 操作者角色
	RequestID    string           `json:"request_id"`    // 请求追踪ID，由 ctx 透传
	ResourceType sql.NullString   `json:"resource_type"` // 资源类型
	ResourceID   sql.NullString   `json:"resource_id"`   // 资源标识（混合用途，可为 id 或用户名）
	Action       sql.NullString   `json:"action"`        // 动作描述
	Status       sql.NullString   `json:"status"`        // 状态：success/failed
	Metadata     json.RawMessage  `json:"metadata"`      // 附加信息（不含敏感凭证），JSON 列
	CreatedAt    time.Time        `json:"created_at"`    // 创建时间（数据库默认 CURRENT_TIMESTAMP）
}

// TableName 返回 audit_events 表对应的数据库表名，供 ORM 风格调用使用。
func (AuditEvent) TableName() string {
	return "audit_events"
}
