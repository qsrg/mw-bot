// File service.go: 审计事件写入服务，对齐 Python app/audit/service.py 语义。
// 所有写入失败仅记录日志，不向上抛错，避免审计故障阻塞业务主流程。
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"

	"mw-bot/internal/common"
)

// AuditService 审计事件写入服务。所有写入操作集中于此，
// 保证字段完整与 request_id 透传一致。
type AuditService struct {
	db *sql.DB
}

// NewAuditService 创建审计服务实例。
// db 为已就绪的 MySQL 连接池（由 common.NewDB 创建）。
func NewAuditService(db *sql.DB) *AuditService {
	return &AuditService{db: db}
}

// RecordEvent 记录一条审计事件。
// 若入参 RequestID 为空，则从 ctx 透传 request_id（common.RequestIDFromContext）。
// INSERT 到 audit_events 表，失败仅记录日志并返回 nil，确保审计故障不阻塞业务。
func (s *AuditService) RecordEvent(ctx context.Context, event AuditEvent) error {
	// 从 ctx 透传 request_id：入参未指定时取上下文中的值
	if event.RequestID == "" {
		event.RequestID = common.RequestIDFromContext(ctx)
	}
	// metadata 为空时写 "{}" 而非 NULL，对齐 Python metadata or {}（M24）
	metadata := event.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage("{}")
	}
	// action/status 为 NOT NULL 列：确保 Valid，避免空串被当作 NULL 插入失败（L21）
	if !event.Action.Valid {
		event.Action = sql.NullString{String: "", Valid: true}
	}
	if !event.Status.Valid {
		event.Status = sql.NullString{String: "", Valid: true}
	}

	// INSERT 语句：id/uuid/created_at 三列由数据库默认值填充，不显式指定。
	const query = `INSERT INTO audit_events
		(event_type, actor_user_id, actor_role, request_id, resource_type, resource_id, action, status, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := s.db.ExecContext(ctx, query,
		event.EventType,
		event.ActorUserID,
		event.ActorRole,
		event.RequestID,
		event.ResourceType,
		event.ResourceID,
		event.Action,
		event.Status,
		metadata,
	)
	if err != nil {
		// 审计失败仅记录日志，不向上抛错
		slog.ErrorContext(ctx, "audit record failed",
			"event_type", event.EventType,
			"actor_user_id", event.ActorUserID,
			"error", err.Error())
		return nil
	}
	return nil
}
