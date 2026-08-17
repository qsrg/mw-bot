// File schemas.go: 聊天相关请求/响应 schema，对齐 Python app/chat/schemas.py。
//
// 仅描述 JSON 序列化结构，不含业务逻辑。字段名、可空性、默认值与 Python
// Pydantic 模型对齐，确保前后端契约一致。
package chat

import "time"

// CitationItem 引用来源项，与 Python CitationItem 对齐。
// document_id/chunk_id/file_name 可为空（与 RAG CitationItem 的指针字段对应）。
type CitationItem struct {
	DocumentID *string `json:"document_id"` // 文档对外标识 uuid（可空）
	ChunkID    *string `json:"chunk_id"`    // 分块标识（可空）
	FileName   *string `json:"file_name"`   // 文件名（可空）
	Score      float64 `json:"score"`       // 相关性分数
	Snippet    string  `json:"snippet"`     // 摘要片段（默认空串）
}

// ChatRequest 问答请求。
type ChatRequest struct {
	Question string `json:"question"` // 用户问题
}

// ChatResponse 问答响应。
type ChatResponse struct {
	MessageID              string         `json:"message_id"`                         // 助手消息对外标识 uuid
	ConversationID         string         `json:"conversation_id"`                    // 会话对外标识 uuid
	Content                string         `json:"content"`                            // 助手回答文本
	Citations              []CitationItem `json:"citations"`                          // 引用列表
	UsedModelInference     bool           `json:"used_model_inference"`               // 是否模型推断
	MemoryExtractionFailed bool           `json:"memory_extraction_failed,omitempty"` // 长期记忆提取失败（不影响回答本身）
}

// ConversationSummary 会话摘要项，用于列表展示。
type ConversationSummary struct {
	ID        string    `json:"id"`         // 会话对外标识 uuid
	Title     string    `json:"title"`      // 会话标题
	UpdatedAt time.Time `json:"updated_at"` // 最后更新时间
}

// MessageItem 历史消息项。
type MessageItem struct {
	ID                 string         `json:"id"`                   // 消息对外标识 uuid
	Role               string         `json:"role"`                 // 角色：user/assistant
	Content            string         `json:"content"`              // 消息内容
	Citations          []CitationItem `json:"citations"`            // 引用列表（仅 assistant 有）
	UsedModelInference bool           `json:"used_model_inference"` // 是否模型推断
	CreatedAt          time.Time      `json:"created_at"`           // 创建时间
}

// MemoryItem 长期记忆项。
type MemoryItem struct {
	ID         string `json:"id"`          // 记忆对外标识 uuid
	MemoryType string `json:"memory_type"` // 记忆类型
	Content    string `json:"content"`     // 记忆内容
	Enabled    bool   `json:"enabled"`     // 是否启用
}

// ToggleMemoryRequest 启用/禁用记忆请求体。
type ToggleMemoryRequest struct {
	Enabled bool `json:"enabled"` // 是否启用
}

// DeleteStatusResponse 删除操作统一响应。
type DeleteStatusResponse struct {
	Status string `json:"status"` // 状态：deleted
}
