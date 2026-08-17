// Package chat 实现问答会话、消息持久化、引用记录、长期记忆与短期记忆压缩，
// 对齐 Python app/chat 模块。
//
// 模块组成：
//   - models.go: Conversation/Message/MessageReference/UserMemory 实体与 DB 查询
//   - schemas.go: 请求/响应 schema（ChatRequest/ChatResponse/CitationItem 等）
//   - service.go: ChatService（问答/会话管理/短期记忆压缩）+ MemoryService（长期记忆提取与启用）
//   - router.go: HTTP 路由（同步/流式问答、会话列表、消息列表、记忆管理）
package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Conversation 问答会话实体，对应 conversations 表。
type Conversation struct {
	ID             int64         `json:"id"`               // 主键ID（INT AUTO_INCREMENT）
	UUID           string        `json:"uuid"`             // 对外标识（CHAR(36)）
	UserID         int64         `json:"user_id"`          // 用户ID(users.id)
	Title          string        `json:"title"`            // 标题
	Archived       bool          `json:"archived"`         // 是否归档
	Summary        string        `json:"summary"`          // 会话摘要（可空）
	SummarizedUpTo sql.NullInt64 `json:"summarized_up_to"` // 摘要已覆盖到的消息ID(含)
	CreatedAt      time.Time     `json:"created_at"`       // 创建时间
	UpdatedAt      time.Time     `json:"updated_at"`       // 更新时间
}

// Message 会话消息实体，对应 messages 表。
type Message struct {
	ID                 int64           `json:"id"`                   // 主键ID
	UUID               string          `json:"uuid"`                 // 对外标识
	ConversationID     int64           `json:"conversation_id"`      // 会话ID(conversations.id)
	Role               string          `json:"role"`                 // 角色：user/assistant
	Content            string          `json:"content"`              // 内容
	UsedModelInference bool            `json:"used_model_inference"` // 是否模型推断
	TokenUsage         json.RawMessage `json:"token_usage"`          // token 用量（JSON，可空）
	CreatedAt          time.Time       `json:"created_at"`           // 创建时间
}

// MessageReference 消息引用来源实体，对应 message_references 表。
type MessageReference struct {
	ID            int64     `json:"id"`             // 主键ID
	UUID          string    `json:"uuid"`           // 对外标识
	MessageID     int64     `json:"message_id"`     // 消息ID(messages.id)
	DocumentID    int64     `json:"document_id"`    // 文档ID(documents.id)
	ChunkID       string    `json:"chunk_id"`       // 分块标识(非表关联)
	FileName      string    `json:"file_name"`      // 文件名
	LocationLabel string    `json:"location_label"` // 位置标签(可空)
	Score         float64   `json:"score"`          // 相关性分数
	Snippet       string    `json:"snippet"`        // 摘要片段
	CreatedAt     time.Time `json:"created_at"`     // 创建时间
}

// UserMemory 用户长期记忆实体，对应 user_memories 表。
type UserMemory struct {
	ID         int64     `json:"id"`          // 主键ID
	UUID       string    `json:"uuid"`        // 对外标识
	UserID     int64     `json:"user_id"`     // 用户ID(users.id)
	MemoryType string    `json:"memory_type"` // 记忆类型
	Content    string    `json:"content"`     // 内容
	Enabled    bool      `json:"enabled"`     // 是否启用
	CreatedAt  time.Time `json:"created_at"`  // 创建时间
	UpdatedAt  time.Time `json:"updated_at"`  // 更新时间
}

// InsertConversation 插入新会话，返回新主键 ID 与生成的 uuid。
// title 为空时使用默认"新会话"。
func InsertConversation(ctx context.Context, db *sql.DB, userID int64, title string) (*Conversation, error) {
	if title == "" {
		title = "新会话"
	}
	convUUID := uuid.New().String()
	const q = `INSERT INTO conversations (uuid, user_id, title) VALUES (?, ?, ?)`
	res, err := db.ExecContext(ctx, q, convUUID, userID, title)
	if err != nil {
		return nil, fmt.Errorf("insert conversation: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get last insert id: %w", err)
	}
	return GetConversationByID(ctx, db, id)
}

// GetConversationByUUID 按 uuid 查询会话，不存在返回 (nil, nil)。
func GetConversationByUUID(ctx context.Context, db *sql.DB, uuid string) (*Conversation, error) {
	const q = `SELECT id, uuid, user_id, title, archived,
		COALESCE(summary, ''), summarized_up_to, created_at, updated_at
		FROM conversations WHERE uuid = ?`
	c, err := scanConversation(db.QueryRowContext(ctx, q, uuid))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query conversation by uuid: %w", err)
	}
	return c, nil
}

// GetConversationByID 按主键 ID 查询会话，不存在返回 (nil, nil)。
func GetConversationByID(ctx context.Context, db *sql.DB, id int64) (*Conversation, error) {
	const q = `SELECT id, uuid, user_id, title, archived,
		COALESCE(summary, ''), summarized_up_to, created_at, updated_at
		FROM conversations WHERE id = ?`
	c, err := scanConversation(db.QueryRowContext(ctx, q, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query conversation by id: %w", err)
	}
	return c, nil
}

// ListConversationsByUser 列出用户未归档的会话，按 updated_at 降序。
func ListConversationsByUser(ctx context.Context, db *sql.DB, userID int64) ([]*Conversation, error) {
	const q = `SELECT id, uuid, user_id, title, archived,
		COALESCE(summary, ''), summarized_up_to, created_at, updated_at
		FROM conversations WHERE user_id = ? AND archived = 0
		ORDER BY updated_at DESC`
	rows, err := db.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()
	out := make([]*Conversation, 0)
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan conversation: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// scanner 抽象 *sql.Row 与 *sql.Rows 的 Scan 接口。
type scanner interface {
	Scan(dest ...any) error
}

// scanConversation 扫描一行到 *Conversation。
func scanConversation(s scanner) (*Conversation, error) {
	var c Conversation
	var archived int8
	if err := s.Scan(&c.ID, &c.UUID, &c.UserID, &c.Title, &archived,
		&c.Summary, &c.SummarizedUpTo, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.Archived = archived != 0
	return &c, nil
}

// UpdateConversationSummary 更新会话摘要与摘要覆盖边界。
func UpdateConversationSummary(ctx context.Context, db *sql.DB, id int64, summary string, summarizedUpTo int64) error {
	const q = `UPDATE conversations SET summary = ?, summarized_up_to = ? WHERE id = ?`
	if _, err := db.ExecContext(ctx, q, summary, summarizedUpTo, id); err != nil {
		return fmt.Errorf("update conversation summary: %w", err)
	}
	return nil
}

// InsertMessage 插入消息，返回新主键 ID 与生成的 uuid。
func InsertMessage(ctx context.Context, db *sql.DB, conversationID int64, role, content string, usedModelInference bool) (int64, string, error) {
	msgUUID := uuid.New().String()
	const q = `INSERT INTO messages (uuid, conversation_id, role, content, used_model_inference)
		VALUES (?, ?, ?, ?, ?)`
	res, err := db.ExecContext(ctx, q, msgUUID, conversationID, role, content, usedModelInference)
	if err != nil {
		return 0, "", fmt.Errorf("insert message: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, "", fmt.Errorf("get last insert id: %w", err)
	}
	return id, msgUUID, nil
}

// ListMessagesByConversation 列出会话消息（id > sinceID），按 created_at 升序。
// sinceID <= 0 时查全部消息。
func ListMessagesByConversation(ctx context.Context, db *sql.DB, conversationID, sinceID int64) ([]*Message, error) {
	var (
		q    string
		args []any
	)
	if sinceID > 0 {
		q = `SELECT id, uuid, conversation_id, role, content, used_model_inference, token_usage, created_at
			FROM messages WHERE conversation_id = ? AND id > ? ORDER BY created_at ASC`
		args = []any{conversationID, sinceID}
	} else {
		q = `SELECT id, uuid, conversation_id, role, content, used_model_inference, token_usage, created_at
			FROM messages WHERE conversation_id = ? ORDER BY created_at ASC`
		args = []any{conversationID}
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	defer rows.Close()
	out := make([]*Message, 0)
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetMessageByID 按主键 ID 查询消息，不存在返回 (nil, nil)。
func GetMessageByID(ctx context.Context, db *sql.DB, id int64) (*Message, error) {
	const q = `SELECT id, uuid, conversation_id, role, content, used_model_inference, token_usage, created_at
		FROM messages WHERE id = ?`
	m, err := scanMessage(db.QueryRowContext(ctx, q, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query message by id: %w", err)
	}
	return m, nil
}

// scanMessage 扫描一行到 *Message。token_usage 可为 NULL。
func scanMessage(s scanner) (*Message, error) {
	var m Message
	var usedModelInference int8
	var tokenUsage []byte
	if err := s.Scan(&m.ID, &m.UUID, &m.ConversationID, &m.Role, &m.Content,
		&usedModelInference, &tokenUsage, &m.CreatedAt); err != nil {
		return nil, err
	}
	m.UsedModelInference = usedModelInference != 0
	m.TokenUsage = json.RawMessage(tokenUsage)
	return &m, nil
}

// InsertMessageReference 插入消息引用，返回新主键 ID。
func InsertMessageReference(
	ctx context.Context,
	db *sql.DB,
	messageID, documentID int64,
	chunkID, fileName, snippet string,
	score float64,
) (int64, error) {
	refUUID := uuid.New().String()
	const q = `INSERT INTO message_references
		(uuid, message_id, document_id, chunk_id, file_name, score, snippet)
		VALUES (?, ?, ?, ?, ?, ?, ?)`
	res, err := db.ExecContext(ctx, q, refUUID, messageID, documentID, chunkID, fileName, score, snippet)
	if err != nil {
		return 0, fmt.Errorf("insert message_reference: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get last insert id: %w", err)
	}
	return id, nil
}

// ListMessageReferencesByMessageIDs 按 message_id 列表查询引用，按 id 升序。
// message_ids 为空时返回空列表。
func ListMessageReferencesByMessageIDs(ctx context.Context, db *sql.DB, messageIDs []int64) ([]*MessageReference, error) {
	if len(messageIDs) == 0 {
		return nil, nil
	}
	// 构造 IN (?, ?, ...) 占位符
	placeholders := ""
	args := make([]any, 0, len(messageIDs))
	for i, id := range messageIDs {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, id)
	}
	q := `SELECT id, uuid, message_id, document_id, chunk_id, file_name,
		COALESCE(location_label, ''), score, snippet, created_at
		FROM message_references WHERE message_id IN (` + placeholders + `) ORDER BY id ASC`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list message_references: %w", err)
	}
	defer rows.Close()
	out := make([]*MessageReference, 0)
	for rows.Next() {
		var r MessageReference
		if err := rows.Scan(&r.ID, &r.UUID, &r.MessageID, &r.DocumentID, &r.ChunkID,
			&r.FileName, &r.LocationLabel, &r.Score, &r.Snippet, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan message_reference: %w", err)
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// NullifyToolCallMessageID 将指定 message_id 列表的 tool_calls.message_id 置空。
// 用于删除会话时保留工具调用审计（不级联删除 tool_calls，仅断关联）。
func NullifyToolCallMessageID(ctx context.Context, db *sql.DB, messageIDs []int64) error {
	if len(messageIDs) == 0 {
		return nil
	}
	placeholders := ""
	args := make([]any, 0, len(messageIDs))
	for i, id := range messageIDs {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, id)
	}
	q := `UPDATE tool_calls SET message_id = NULL WHERE message_id IN (` + placeholders + `)`
	if _, err := db.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("nullify tool_calls message_id: %w", err)
	}
	return nil
}

// DeleteMessageReferencesByMessageIDs 按 message_id 列表删除引用。
func DeleteMessageReferencesByMessageIDs(ctx context.Context, db *sql.DB, messageIDs []int64) error {
	if len(messageIDs) == 0 {
		return nil
	}
	placeholders := ""
	args := make([]any, 0, len(messageIDs))
	for i, id := range messageIDs {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, id)
	}
	q := `DELETE FROM message_references WHERE message_id IN (` + placeholders + `)`
	if _, err := db.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("delete message_references: %w", err)
	}
	return nil
}

// execer 抽象 *sql.DB 与 *sql.Tx 的 ExecContext 接口，便于 tx 版本辅助函数复用。
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// deleteMessageReferencesByMessageIDsTx tx 版本：按 message_id 列表删除引用。
func deleteMessageReferencesByMessageIDsTx(ctx context.Context, tx execer, messageIDs []int64) error {
	if len(messageIDs) == 0 {
		return nil
	}
	placeholders := ""
	args := make([]any, 0, len(messageIDs))
	for i, id := range messageIDs {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, id)
	}
	q := `DELETE FROM message_references WHERE message_id IN (` + placeholders + `)`
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("delete message_references: %w", err)
	}
	return nil
}

// DeleteMessagesByConversation 删除会话下的全部消息。
func DeleteMessagesByConversation(ctx context.Context, db *sql.DB, conversationID int64) error {
	const q = `DELETE FROM messages WHERE conversation_id = ?`
	if _, err := db.ExecContext(ctx, q, conversationID); err != nil {
		return fmt.Errorf("delete messages: %w", err)
	}
	return nil
}

// deleteMessagesByConversationTx tx 版本：删除会话下的全部消息。
func deleteMessagesByConversationTx(ctx context.Context, tx execer, conversationID int64) error {
	const q = `DELETE FROM messages WHERE conversation_id = ?`
	if _, err := tx.ExecContext(ctx, q, conversationID); err != nil {
		return fmt.Errorf("delete messages: %w", err)
	}
	return nil
}

// nullifyToolCallMessageIDTx tx 版本：将指定 message_id 列表的 tool_calls.message_id 置空。
func nullifyToolCallMessageIDTx(ctx context.Context, tx execer, messageIDs []int64) error {
	if len(messageIDs) == 0 {
		return nil
	}
	placeholders := ""
	args := make([]any, 0, len(messageIDs))
	for i, id := range messageIDs {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, id)
	}
	q := `UPDATE tool_calls SET message_id = NULL WHERE message_id IN (` + placeholders + `)`
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("nullify tool_calls message_id: %w", err)
	}
	return nil
}

// DeleteConversationByID 删除会话（按主键）。
func DeleteConversationByID(ctx context.Context, db *sql.DB, id int64) error {
	const q = `DELETE FROM conversations WHERE id = ?`
	if _, err := db.ExecContext(ctx, q, id); err != nil {
		return fmt.Errorf("delete conversation: %w", err)
	}
	return nil
}

// ListMessageIDsByConversation 列出会话下的全部消息 ID。
func ListMessageIDsByConversation(ctx context.Context, db *sql.DB, conversationID int64) ([]int64, error) {
	const q = `SELECT id FROM messages WHERE conversation_id = ?`
	rows, err := db.QueryContext(ctx, q, conversationID)
	if err != nil {
		return nil, fmt.Errorf("list message ids: %w", err)
	}
	defer rows.Close()
	out := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan message id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// InsertUserMemory 插入长期记忆，返回新主键 ID 与生成的 uuid。
// enabled 默认 true。
func InsertUserMemory(ctx context.Context, db *sql.DB, userID int64, memoryType, content string) (int64, string, error) {
	memUUID := uuid.New().String()
	const q = `INSERT INTO user_memories (uuid, user_id, memory_type, content, enabled)
		VALUES (?, ?, ?, ?, 1)`
	res, err := db.ExecContext(ctx, q, memUUID, userID, memoryType, content)
	if err != nil {
		return 0, "", fmt.Errorf("insert user_memory: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, "", fmt.Errorf("get last insert id: %w", err)
	}
	return id, memUUID, nil
}

// GetUserMemoryByUUID 按 uuid 查询长期记忆，不存在返回 (nil, nil)。
func GetUserMemoryByUUID(ctx context.Context, db *sql.DB, uuid string) (*UserMemory, error) {
	const q = `SELECT id, uuid, user_id, memory_type, content, enabled, created_at, updated_at
		FROM user_memories WHERE uuid = ?`
	m, err := scanUserMemory(db.QueryRowContext(ctx, q, uuid))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query user_memory by uuid: %w", err)
	}
	return m, nil
}

// GetUserMemoryByUserAndType 按 (user_id, memory_type) 查询记忆，不存在返回 (nil, nil)。
// 用于记忆 upsert：结构化偏好按类型唯一。
func GetUserMemoryByUserAndType(ctx context.Context, db *sql.DB, userID int64, memoryType string) (*UserMemory, error) {
	const q = `SELECT id, uuid, user_id, memory_type, content, enabled, created_at, updated_at
		FROM user_memories WHERE user_id = ? AND memory_type = ? LIMIT 1`
	m, err := scanUserMemory(db.QueryRowContext(ctx, q, userID, memoryType))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query user_memory by user+type: %w", err)
	}
	return m, nil
}

// ExistsUserMemoryByUserAndContent 判断用户是否已有相同内容的记忆，用于 preference 去重。
func ExistsUserMemoryByUserAndContent(ctx context.Context, db *sql.DB, userID int64, content string) (bool, error) {
	const q = `SELECT 1 FROM user_memories WHERE user_id = ? AND content = ? LIMIT 1`
	var one int
	err := db.QueryRowContext(ctx, q, userID, content).Scan(&one)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("check user_memory exists: %w", err)
	}
	return true, nil
}

// ListUserMemoriesByUser 列出用户的全部长期记忆，按 created_at 降序。
func ListUserMemoriesByUser(ctx context.Context, db *sql.DB, userID int64) ([]*UserMemory, error) {
	const q = `SELECT id, uuid, user_id, memory_type, content, enabled, created_at, updated_at
		FROM user_memories WHERE user_id = ? ORDER BY created_at DESC`
	rows, err := db.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("list user_memories: %w", err)
	}
	defer rows.Close()
	out := make([]*UserMemory, 0)
	for rows.Next() {
		m, err := scanUserMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("scan user_memory: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListEnabledUserMemoriesByUser 列出用户启用的长期记忆，按 created_at 升序。
// 用于注入问答 prompt。
func ListEnabledUserMemoriesByUser(ctx context.Context, db *sql.DB, userID int64) ([]*UserMemory, error) {
	const q = `SELECT id, uuid, user_id, memory_type, content, enabled, created_at, updated_at
		FROM user_memories WHERE user_id = ? AND enabled = 1 ORDER BY created_at ASC`
	rows, err := db.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("list enabled user_memories: %w", err)
	}
	defer rows.Close()
	out := make([]*UserMemory, 0)
	for rows.Next() {
		m, err := scanUserMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("scan user_memory: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// scanUserMemory 扫描一行到 *UserMemory。
func scanUserMemory(s scanner) (*UserMemory, error) {
	var m UserMemory
	var enabled int8
	if err := s.Scan(&m.ID, &m.UUID, &m.UserID, &m.MemoryType, &m.Content,
		&enabled, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	m.Enabled = enabled != 0
	return &m, nil
}

// UpdateUserMemoryContent 更新记忆内容。
func UpdateUserMemoryContent(ctx context.Context, db *sql.DB, id int64, content string) error {
	const q = `UPDATE user_memories SET content = ? WHERE id = ?`
	if _, err := db.ExecContext(ctx, q, content, id); err != nil {
		return fmt.Errorf("update user_memory content: %w", err)
	}
	return nil
}

// UpdateUserMemoryEnabled 更新记忆启用状态。
func UpdateUserMemoryEnabled(ctx context.Context, db *sql.DB, id int64, enabled bool) error {
	const q = `UPDATE user_memories SET enabled = ? WHERE id = ?`
	if _, err := db.ExecContext(ctx, q, enabled, id); err != nil {
		return fmt.Errorf("update user_memory enabled: %w", err)
	}
	return nil
}

// DeleteUserMemoryByID 按主键删除记忆。
func DeleteUserMemoryByID(ctx context.Context, db *sql.DB, id int64) error {
	const q = `DELETE FROM user_memories WHERE id = ?`
	if _, err := db.ExecContext(ctx, q, id); err != nil {
		return fmt.Errorf("delete user_memory: %w", err)
	}
	return nil
}
