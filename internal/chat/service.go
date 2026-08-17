// File service.go: 聊天服务核心，对齐 Python app/chat/service.py。
//
// ChatService 负责会话管理、消息持久化、引用记录、短期记忆压缩与 agent loop 编排；
// MemoryService 负责长期记忆的 LLM 提取、敏感过滤、按类型 upsert 与启用切换。
//
// 关键设计：
//   - 同步问答 Ask 与流式问答 AskStream 共用 persistAssistant 落库逻辑
//   - 流式问答在 setup 阶段跑完 agent loop（依赖 db），流式阶段只做 LLM 生成
//   - 短期记忆压缩：超 HistoryTokenBudget 时折叠旧消息进 summary，推进 summarized_up_to 边界
//   - 长期记忆：LLM 提取 + 敏感词黑名单二次过滤；结构化偏好按 (user_id, memory_type) upsert
package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mw-bot/internal/audit"
	"mw-bot/internal/common"
	"mw-bot/internal/knowledge"
	"mw-bot/internal/mcp_gateway"
	"mw-bot/internal/rag"
)

// allowedMemoryTypes 长期记忆类型枚举（设计 spec 允许的记忆类型）。
// LLM 提取限定于此集合，与 Python _ALLOWED_MEMORY_TYPES 对齐。
var allowedMemoryTypes = []string{
	"default_environment",
	"common_cluster",
	"default_knowledge_base",
	"common_component",
	"frequent_domain",
	"answer_style",
	"preference",
}

// memoryTemplates 记忆内容模板：LLM 返回 value 后套用，保持与已存记忆格式一致。
// 与 Python _MEMORY_TEMPLATES 对齐。
var memoryTemplates = map[string]string{
	"default_environment":    "默认环境: {value}",
	"common_cluster":         "常用集群: {value}",
	"default_knowledge_base": "默认知识库: {value}",
	"common_component":       "常用组件: {value}",
	"frequent_domain":        "高频业务域: {value}",
	"answer_style":           "回答风格: {value}",
	"preference":             "{value}",
}

// sensitiveKeywords 敏感词黑名单：命中则丢弃候选记忆，禁止保存凭证/密钥/隐私等。
// 作为 LLM 判断之外的防御性二次过滤，避免模型漏判导致敏感信息入库。
var sensitiveKeywords = []string{
	"password", "passwd", "密码", "token", "secret", "私钥", "密钥",
	"凭证", "凭据", "api_key", "apikey", "access_key", "credentials",
	"cookie", "session_id", "sessionid",
}

// maxMemoryLength 单条记忆内容最大长度，超长视为大段输出予以丢弃。
const maxMemoryLength = 200

// memoryArrayRE 从 LLM 输出提取首个 JSON 数组（记忆候选列表）。
var memoryArrayRE = regexp.MustCompile(`(?s)\[.*\]`)

// ChatService 会话与问答服务。
type ChatService struct {
	db       *sql.DB
	rag      *rag.RagService
	audit    *audit.AuditService
	settings common.Settings
}

// NewChatService 创建聊天服务。
//
// 参数：
//   - db: 已就绪的 MySQL 连接池。
//   - ragSvc: RAG 服务，用于 agent loop 与答案生成。
//   - auditSvc: 审计服务，用于记录问答/记忆事件。
//   - settings: 应用配置（提供 HistoryTokenBudget 等记忆压缩参数）。
func NewChatService(
	db *sql.DB,
	ragSvc *rag.RagService,
	auditSvc *audit.AuditService,
	settings common.Settings,
) *ChatService {
	return &ChatService{db: db, rag: ragSvc, audit: auditSvc, settings: settings}
}

// CreateConversation 创建新会话。title 为空时使用默认"新会话"。
func (s *ChatService) CreateConversation(ctx context.Context, userID int64, title string) (*Conversation, error) {
	if title == "" {
		title = "新会话"
	}
	return InsertConversation(ctx, s.db, userID, title)
}

// ListConversations 列出用户未归档的会话，按 updated_at 降序。
func (s *ChatService) ListConversations(ctx context.Context, userID int64) ([]*Conversation, error) {
	return ListConversationsByUser(ctx, s.db, userID)
}

// ListMessagesWithRefs 列出指定会话的全部消息及其引用（按消息时间升序）。
// 仅可查看自己的会话，否则返回 NotFound。
//
// 返回 (消息, 引用列表) 元组列表。
func (s *ChatService) ListMessagesWithRefs(ctx context.Context, userID int64, conversationUUID string) ([]*Message, []*MessageReference, error) {
	conv, err := GetConversationByUUID(ctx, s.db, conversationUUID)
	if err != nil {
		return nil, nil, common.SystemError(err)
	}
	if conv == nil || conv.UserID != userID {
		return nil, nil, common.NotFound("会话不存在")
	}
	messages, err := ListMessagesByConversation(ctx, s.db, conv.ID, 0)
	if err != nil {
		return nil, nil, common.SystemError(err)
	}
	if len(messages) == 0 {
		return messages, nil, nil
	}
	messageIDs := make([]int64, 0, len(messages))
	for _, m := range messages {
		messageIDs = append(messageIDs, m.ID)
	}
	refs, err := ListMessageReferencesByMessageIDs(ctx, s.db, messageIDs)
	if err != nil {
		return nil, nil, common.SystemError(err)
	}
	return messages, refs, nil
}

// DeleteConversation 硬删除指定会话及其消息与引用。
// 表间无外键级联，手动级联：先删 message_references，置空 tool_calls.message_id
// 以保留工具调用审计，再删 messages，最后删 conversation。
func (s *ChatService) DeleteConversation(ctx context.Context, userID int64, conversationUUID, actorRole string) error {
	conv, err := GetConversationByUUID(ctx, s.db, conversationUUID)
	if err != nil {
		return common.SystemError(err)
	}
	if conv == nil || conv.UserID != userID {
		return common.NotFound("会话不存在")
	}
	messageIDs, err := ListMessageIDsByConversation(ctx, s.db, conv.ID)
	if err != nil {
		return common.SystemError(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return common.SystemError(fmt.Errorf("begin tx: %w", err))
	}
	defer func() { _ = tx.Rollback() }()
	if len(messageIDs) > 0 {
		if err := deleteMessageReferencesByMessageIDsTx(ctx, tx, messageIDs); err != nil {
			return common.SystemError(err)
		}
		if err := nullifyToolCallMessageIDTx(ctx, tx, messageIDs); err != nil {
			return common.SystemError(err)
		}
		if err := deleteMessagesByConversationTx(ctx, tx, conv.ID); err != nil {
			return common.SystemError(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM conversations WHERE id = ?`, conv.ID); err != nil {
		return common.SystemError(fmt.Errorf("delete conversation: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return common.SystemError(fmt.Errorf("commit tx: %w", err))
	}
	s.audit.RecordEvent(ctx, audit.AuditEvent{
		EventType:    "conversation_deleted",
		ActorUserID:  sql.NullInt64{Int64: userID, Valid: userID > 0},
		RequestID:    common.RequestIDFromContext(ctx),
		ActorRole:    sql.NullString{String: actorRole, Valid: actorRole != ""},
		ResourceType: sql.NullString{String: "conversation", Valid: true},
		ResourceID:   sql.NullString{String: conv.UUID, Valid: true},
		Action:       sql.NullString{String: "delete", Valid: true},
		Status:       sql.NullString{String: "success", Valid: true},
		Metadata:     marshalJSON(map[string]any{"message_count": len(messageIDs)}),
	})
	return nil
}

// loadHistory 加载会话上下文用于续接：摘要 + summarized_up_to 之后的近期消息。
// 摘要（若有）作为 system 消息置于历史最前，其后为尚未折叠的近期消息。
func (s *ChatService) loadHistory(ctx context.Context, conversationID int64) ([]common.Message, error) {
	conv, err := GetConversationByID(ctx, s.db, conversationID)
	if err != nil {
		return nil, common.SystemError(err)
	}
	if conv == nil {
		return nil, nil
	}
	summarizedUpTo := int64(0)
	if conv.SummarizedUpTo.Valid {
		summarizedUpTo = conv.SummarizedUpTo.Int64
	}
	messages, err := ListMessagesByConversation(ctx, s.db, conversationID, summarizedUpTo)
	if err != nil {
		return nil, common.SystemError(err)
	}
	history := make([]common.Message, 0, len(messages)+1)
	if conv.Summary != "" {
		history = append(history, common.Message{
			Role:    "system",
			Content: "之前的会话摘要:\n" + conv.Summary,
		})
	}
	for _, m := range messages {
		history = append(history, common.Message{Role: m.Role, Content: m.Content})
	}
	return history, nil
}

// buildToolContext 加载启用的 MCP 工具，构造工具定义与执行回调。
// 无启用工具时返回 ([], nil)，RagService 将走纯知识库流程。
func (s *ChatService) buildToolContext(ctx context.Context, userID int64, userRole string) ([]rag.ToolDef, rag.ToolExecutor) {
	tools, err := mcp_gateway.ListEnabledTools(ctx, s.db)
	if err != nil {
		slog.WarnContext(ctx, "加载启用工具失败，按无工具处理", "error", err)
		return nil, nil
	}
	if len(tools) == 0 {
		return nil, nil
	}
	toolDefs := make([]rag.ToolDef, 0, len(tools))
	byName := make(map[string]*mcp_gateway.McpTool, len(tools))
	serverCache := make(map[int64]*mcp_gateway.McpServer)
	for _, t := range tools {
		toolDefs = append(toolDefs, rag.ToolDef{
			Name:        t.ToolName,
			Description: t.Description,
			InputSchema: normalizeInputSchema(t.InputSchema),
		})
		byName[t.ToolName] = t
		// 预加载 server，避免每次工具调用都查库
		if _, ok := serverCache[t.ServerID]; !ok {
			sv, err := mcp_gateway.GetServerByID(ctx, s.db, t.ServerID)
			if err != nil {
				slog.WarnContext(ctx, "加载 MCP Server 失败", "server_id", t.ServerID, "error", err)
				continue
			}
			serverCache[t.ServerID] = sv
		}
	}
	gw := mcp_gateway.NewMcpGatewayService(s.db, s.audit)
	executor := func(ctx context.Context, toolName string, arguments map[string]any) (map[string]any, error) {
		tool, ok := byName[toolName]
		if !ok {
			return nil, fmt.Errorf("工具 %s 不可用", toolName)
		}
		server := serverCache[tool.ServerID]
		if server == nil {
			return nil, fmt.Errorf("工具 %s 所属 Server 不可用", toolName)
		}
		// confirmed=false：requires_approval 的工具在 agent 自动调用时按拒绝处理
		rec, err := gw.InvokeTool(ctx, tool, server, arguments, userID, userRole, false)
		if err != nil {
			return nil, err
		}
		// output 为 JSON RawMessage，反序列化为 map
		var output map[string]any
		if len(rec.Output) > 0 {
			if err := json.Unmarshal(rec.Output, &output); err != nil {
				return nil, fmt.Errorf("decode tool output: %w", err)
			}
		}
		if output == nil {
			output = map[string]any{}
		}
		return output, nil
	}
	return toolDefs, executor
}

// normalizeInputSchema 将 InputSchema 归一化为 map[string]any。
// InputSchema 为空或解析失败时返回空 map。
func normalizeInputSchema(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{}
	}
	if m == nil {
		return map[string]any{}
	}
	return m
}

// AskResult Ask 方法返回的问答结果：会话、助手消息、RAG 答案与记忆提取状态。
type AskResult struct {
	Conversation           *Conversation  // 会话实体
	Message                *Message       // 助手消息
	RagAnswer              *rag.RagAnswer // RAG 答案
	MemoryExtractionFailed bool           // 长期记忆提取是否失败（不影响问答结果，仅供 API 层提示）
}

// Ask 提交问题，持久化用户与助手消息、引用，返回答案。
//
// 流程：
//  1. 加载/新建会话；校验会话归属当前用户
//  2. 加载历史上下文（含摘要前置）与启用的长期记忆
//  3. 持久化 user 消息
//  4. 在 db 仍可用时跑 agent loop（工具决策-执行）
//  5. RAG 生成答案（检索/意图/反问/拼 prompt/LLM）
//  6. 持久化 assistant 消息、引用，写审计与长期记忆
//  7. 超 token 预算时压缩会话历史
func (s *ChatService) Ask(
	ctx context.Context,
	conversationUUID, question string,
	userID int64,
	userRole string,
) (*AskResult, error) {
	conv, err := s.resolveConversation(ctx, conversationUUID, userID, question)
	if err != nil {
		return nil, err
	}
	history, err := s.loadHistory(ctx, conv.ID)
	if err != nil {
		return nil, err
	}
	memories := s.loadEnabledMemories(ctx, userID)

	// 持久化 user 消息
	if _, _, err := InsertMessage(ctx, s.db, conv.ID, "user", question, false); err != nil {
		return nil, common.SystemError(err)
	}

	// agent loop
	toolDefs, toolExecutor := s.buildToolContext(ctx, userID, userRole)
	var toolResults []string
	var toolCalls []map[string]any
	if toolExecutor != nil {
		toolResults, toolCalls = s.rag.RunToolLoop(ctx, question, toolDefs, toolExecutor, history)
	}

	ragAnswer, err := s.rag.Answer(ctx, question, history, memories, toolResults, toolCalls)
	if err != nil {
		return nil, err
	}

	msg, memoryFailed, err := s.persistAssistant(ctx, conv.ID, conv.UUID, ragAnswer.Content, ragAnswer.Citations, ragAnswer.UsedModelInference, userID, userRole, question)
	if err != nil {
		return nil, err
	}
	return &AskResult{Conversation: conv, Message: msg, RagAnswer: ragAnswer, MemoryExtractionFailed: memoryFailed}, nil
}

// resolveConversation 加载或新建会话。
// conversationUUID 为空时新建会话（标题取问题前 30 字符）；非空时校验归属。
func (s *ChatService) resolveConversation(ctx context.Context, conversationUUID string, userID int64, question string) (*Conversation, error) {
	if conversationUUID == "" {
		title := question
		if len([]rune(title)) > 30 {
			title = string([]rune(title)[:30])
		}
		conv, err := s.CreateConversation(ctx, userID, title)
		if err != nil {
			return nil, common.SystemError(err)
		}
		return conv, nil
	}
	conv, err := GetConversationByUUID(ctx, s.db, conversationUUID)
	if err != nil {
		return nil, common.SystemError(err)
	}
	if conv == nil || conv.UserID != userID {
		return nil, common.NotFound("会话不存在")
	}
	return conv, nil
}

// persistAssistant 持久化助手消息、引用并写入审计与长期记忆，返回助手消息对象与记忆提取是否失败。
//
// 抽出供同步问答与流式问答落库复用；流式路径传入捕获的 request_id，
// 避免 context 在流式响应生命周期内被取消。
// 返回值 memoryFailed 仅表示长期记忆提取失败（兜底/反问回复会跳过提取，不算失败），
// 不影响消息落库结果，由调用方决定是否向用户提示。
func (s *ChatService) persistAssistant(
	ctx context.Context,
	conversationID int64,
	conversationUUID, content string,
	citations []rag.CitationItem,
	usedModelInference bool,
	userID int64,
	userRole, question string,
) (*Message, bool, error) {
	assistantID, assistantUUID, err := InsertMessage(ctx, s.db, conversationID, "assistant", content, usedModelInference)
	if err != nil {
		return nil, false, common.SystemError(err)
	}
	for _, c := range citations {
		if c.DocumentID == nil || *c.DocumentID == "" {
			continue
		}
		doc, err := knowledge.GetDocumentByUUID(ctx, s.db, *c.DocumentID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			slog.WarnContext(ctx, "查询引用文档失败", "document_uuid", *c.DocumentID, "error", err)
			continue
		}
		chunkID := ""
		if c.ChunkID != nil {
			chunkID = *c.ChunkID
		}
		fileName := ""
		if c.FileName != nil {
			fileName = *c.FileName
		}
		if _, err := InsertMessageReference(ctx, s.db, assistantID, doc.ID, chunkID, fileName, c.Snippet, c.Score); err != nil {
			slog.WarnContext(ctx, "插入消息引用失败", "error", err)
		}
	}
	// 写入问答审计
	s.audit.RecordEvent(ctx, audit.AuditEvent{
		EventType:    "chat_question",
		ActorUserID:  sql.NullInt64{Int64: userID, Valid: userID > 0},
		RequestID:    common.RequestIDFromContext(ctx),
		ActorRole:    sql.NullString{String: userRole, Valid: userRole != ""},
		ResourceType: sql.NullString{String: "conversation", Valid: true},
		ResourceID:   sql.NullString{String: conversationUUID, Valid: true},
		Action:       sql.NullString{String: "ask", Valid: true},
		Status:       sql.NullString{String: "success", Valid: true},
		Metadata:     marshalJSON(map[string]any{"citations": len(citations)}),
	})
	if usedModelInference {
		s.audit.RecordEvent(ctx, audit.AuditEvent{
			EventType:    "model_inference",
			ActorUserID:  sql.NullInt64{Int64: userID, Valid: userID > 0},
			RequestID:    common.RequestIDFromContext(ctx),
			ActorRole:    sql.NullString{String: userRole, Valid: userRole != ""},
			ResourceType: sql.NullString{String: "conversation", Valid: true},
			ResourceID:   sql.NullString{String: conversationUUID, Valid: true},
			Action:       sql.NullString{String: "model_inference", Valid: true},
			Status:       sql.NullString{String: "success", Valid: true},
		})
	}
	// 问答完成后提取长期记忆；兜底回复与反问模板跳过（见 shouldExtractMemory）。
	// 提取失败不影响已完成的问答结果，仅向上返回供 API 层提示用户。
	memoryFailed := false
	if shouldExtractMemory(content) {
		memSvc := NewMemoryService(s.db, s.rag.LLMProvider(), s.audit)
		if _, memErr := memSvc.ExtractAndSave(ctx, userID, question, content, userRole); memErr != nil {
			slog.WarnContext(ctx, "长期记忆提取失败", "user_id", userID, "error", memErr)
			memoryFailed = true
		}
	}
	// 超 token 预算时压缩会话历史；失败不影响已完成的问答
	if compErr := s.compressHistoryIfNeeded(ctx, conversationID); compErr != nil {
		slog.WarnContext(ctx, "会话历史压缩失败", "conversation_id", conversationID, "error", compErr)
	}
	return &Message{ID: assistantID, UUID: assistantUUID, ConversationID: conversationID, Role: "assistant", Content: content, UsedModelInference: usedModelInference}, memoryFailed, nil
}

// shouldExtractMemory 判断回复内容是否需要执行长期记忆提取。
//
// 跳过两类回复：
//   - 兜底回复：模型网关不可用时的固定文案，此时提取用的 LLM 调用同样会失败，
//     尝试提取只是白白产生一次失败日志（历史上大量静默失败即源于此）；
//   - 跨中间件歧义反问模板：固定文案，不含任何用户偏好信息。
//
// 正常模型回复（含 chat 意图的闲聊/身份回答）仍需提取：
// answer_style 类偏好（如"记住用中文回答"）正是通过 chat 意图轮次表达的。
func shouldExtractMemory(content string) bool {
	return content != rag.FallbackAnswer && !rag.IsClarification(content)
}

// compressHistoryIfNeeded 超 token 预算时压缩会话历史：折叠最旧消息进摘要、推进边界。
//
// 取 summarized_up_to 之后的全部消息，若 summary + 消息总量超 history_token_budget，
// 则从最旧开始淘汰至剩余 <= budget * history_recent_ratio，将淘汰消息折叠进 summary，
// 并推进 summarized_up_to 边界。普通会话不超预算则直接返回，无额外开销。
func (s *ChatService) compressHistoryIfNeeded(ctx context.Context, conversationID int64) error {
	conv, err := GetConversationByID(ctx, s.db, conversationID)
	if err != nil {
		return err
	}
	if conv == nil {
		return nil
	}
	summarizedUpTo := int64(0)
	if conv.SummarizedUpTo.Valid {
		summarizedUpTo = conv.SummarizedUpTo.Int64
	}
	messages, err := ListMessagesByConversation(ctx, s.db, conversationID, summarizedUpTo)
	if err != nil {
		return err
	}
	budget := s.settings.HistoryTokenBudget
	total := common.EstimateTokens(conv.Summary)
	for _, m := range messages {
		total += common.EstimateTokens(m.Content)
	}
	if total <= budget {
		return nil
	}
	// 从最旧开始淘汰，直到剩余 token <= budget * history_recent_ratio
	recentLimit := int(float64(budget) * s.settings.HistoryRecentRatio)
	evicted := make([]*Message, 0)
	remainingTokens := total
	idx := 0
	for idx < len(messages) && remainingTokens > recentLimit {
		msg := messages[idx]
		remainingTokens -= common.EstimateTokens(msg.Content)
		evicted = append(evicted, msg)
		idx++
	}
	if len(evicted) == 0 {
		return nil
	}
	// 折叠淘汰消息进摘要；摘要失败则保留旧摘要不推进边界
	newSummary, err := s.summarizeMessages(ctx, conv.Summary, evicted)
	if err != nil {
		slog.WarnContext(ctx, "会话摘要压缩失败", "error", err)
		return nil
	}
	if newSummary == "" {
		return nil
	}
	return UpdateConversationSummary(ctx, s.db, conversationID, newSummary, evicted[len(evicted)-1].ID)
}

// summarizeMessages 调用 LLM 将已有摘要与新淘汰消息折叠为更新后的简洁摘要。
//
// prompt 要求聚焦关键事实/用户意图/已确定约束与决策，控制在约 1500 token 以内，
// 丢弃寒暄与冗余。LLM 调用失败时返回空串，调用方据此跳过更新。
func (s *ChatService) summarizeMessages(ctx context.Context, existingSummary string, messages []*Message) (string, error) {
	parts := make([]string, 0, len(messages))
	for _, m := range messages {
		parts = append(parts, m.Role+": "+m.Content)
	}
	transcript := strings.Join(parts, "\n")
	prior := ""
	if existingSummary != "" {
		prior = "现有摘要：\n" + existingSummary + "\n\n"
	}
	messages2 := []common.Message{
		{
			Role: "system",
			Content: "你是会话摘要助手。将以下内容折叠为一份简洁的会话摘要，" +
				"聚焦关键事实、用户意图、已确定约束与决策，丢弃寒暄与冗余。" +
				"控制在约 1500 token 以内。只输出摘要正文，不要额外说明。",
		},
		{Role: "user", Content: prior + "需要折叠的对话：\n" + transcript},
	}
	raw, err := s.rag.LLMProvider().Chat(ctx, messages2)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(raw), nil
}

// StreamEvent 流式问答事件，与前端约定一致。
// Type 取值 meta/reasoning/delta/done。
type StreamEvent struct {
	Type                   string         `json:"type"`                               // 事件类型：meta/reasoning/delta/done
	ConversationID         string         `json:"conversation_id,omitempty"`          // meta：会话 uuid
	Citations              []CitationItem `json:"citations,omitempty"`                // meta：引用列表
	UsedModelInference     bool           `json:"used_model_inference,omitempty"`     // meta/done：是否模型推断
	Text                   string         `json:"text,omitempty"`                     // reasoning/delta：文本片段
	MessageID              string         `json:"message_id,omitempty"`               // done：助手消息 uuid
	MemoryExtractionFailed bool           `json:"memory_extraction_failed,omitempty"` // done：长期记忆提取失败
}

// AskStream 流式问答：同步完成会话与用户消息持久化，返回事件流回调。
//
// 流程：
//  1. 加载/新建会话；加载历史与长期记忆；持久化 user 消息
//  2. 在 setup 阶段（db 仍可用）跑 agent loop
//  3. 调用 RAG.AnswerStream 生成事件流，透传 meta/reasoning/delta/done
//  4. done 事件触发时，用捕获的 request_id 落库 assistant 消息、引用、审计与长期记忆
//
// 返回 (会话, 事件流回调)。事件流回调由调用方逐事件调用，无更多事件时返回 io.EOF。
func (s *ChatService) AskStream(
	ctx context.Context,
	conversationUUID, question string,
	userID int64,
	userRole string,
) (*Conversation, func() (*StreamEvent, error), error) {
	conv, err := s.resolveConversation(ctx, conversationUUID, userID, question)
	if err != nil {
		return nil, nil, err
	}
	history, err := s.loadHistory(ctx, conv.ID)
	if err != nil {
		return nil, nil, err
	}
	memories := s.loadEnabledMemories(ctx, userID)

	// 持久化 user 消息
	if _, _, err := InsertMessage(ctx, s.db, conv.ID, "user", question, false); err != nil {
		return nil, nil, common.SystemError(err)
	}

	// 物化标识供生成器使用，避免旧会话对象在生成器中脱管失效
	conversationUUID2 := conv.UUID
	conversationID := conv.ID
	// agent loop 在 setup 阶段（db 仍可用）跑完
	toolDefs, toolExecutor := s.buildToolContext(ctx, userID, userRole)
	var toolResults []string
	var toolCalls []map[string]any
	if toolExecutor != nil {
		toolResults, toolCalls = s.rag.RunToolLoop(ctx, question, toolDefs, toolExecutor, history)
	}

	// 状态机：透传 RAG 事件，done 时落库
	state := &streamState{
		chat:               s,
		userID:             userID,
		userRole:           userRole,
		question:           question,
		conversationID:     conversationID,
		conversationUUID:   conversationUUID2,
		citations:          nil,
		usedModelInference: false,
		done:               false,
	}

	// 启动 RAG 流式生成，将事件投递到 chan
	events := make(chan rag.StreamEvent, 16)
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.rag.AnswerStream(ctx, question, history, memories, toolResults, toolCalls, func(e rag.StreamEvent) error {
			// 客户端断开/ctx 取消时不再阻塞发送，避免 goroutine 与事件永久泄漏（H7）
			select {
			case events <- e:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		close(events)
	}()

	var next func() (*StreamEvent, error)
	next = func() (*StreamEvent, error) {
		if state.done {
			return nil, ioEOF
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case e, ok := <-events:
			if !ok {
				// 事件流关闭后检查 RAG 是否出错
				if err := <-errCh; err != nil {
					// 出错也走 done 流程，落库兜底文案
					return s.finalizeStream(ctx, state, rag.FallbackAnswer, state.usedModelInference)
				}
				// 未收到 done 但流已关闭：兜底
				return s.finalizeStream(ctx, state, rag.FallbackAnswer, state.usedModelInference)
			}
			switch e.Type {
			case "meta":
				state.citations = toChatCitations(e.Citations)
				state.usedModelInference = e.UsedModelInference
				return &StreamEvent{
					Type:               "meta",
					ConversationID:     state.conversationUUID,
					Citations:          state.citations,
					UsedModelInference: state.usedModelInference,
				}, nil
			case "reasoning":
				return &StreamEvent{Type: "reasoning", Text: e.Text}, nil
			case "delta":
				state.contentMu.Lock()
				state.content += e.Text
				state.contentMu.Unlock()
				return &StreamEvent{Type: "delta", Text: e.Text}, nil
			case "done":
				content := e.Content
				if content == "" {
					state.contentMu.Lock()
					content = state.content
					state.contentMu.Unlock()
				}
				return s.finalizeStream(ctx, state, content, e.UsedModelInference)
			}
			// 未知事件类型：跳过，取下一个
			return next()
		}
	}
	return conv, next, nil
}

// ioEOF 表示流正常结束的哨兵错误（避免引入 io 包到接口签名）。
var ioEOF = errors.New("EOF")

// streamState 流式问答内部状态：累积 delta 文本与 done 落库所需的上下文。
type streamState struct {
	chat               *ChatService
	userID             int64
	userRole           string
	question           string
	conversationID     int64
	conversationUUID   string
	citations          []CitationItem
	usedModelInference bool
	content            string
	contentMu          sync.Mutex
	done               bool
}

// finalizeStream 落库助手消息、引用、审计与长期记忆，返回 done 事件。
// 重复调用时直接返回 EOF。
func (s *ChatService) finalizeStream(ctx context.Context, state *streamState, content string, usedModelInference bool) (*StreamEvent, error) {
	if state.done {
		return nil, ioEOF
	}
	state.done = true
	// done 事件的 used_model_inference 覆盖 meta 值（对齐 Python event.get("used_model_inference", used_model_inference)，
	// M10）。错误路径调用方传入 state.usedModelInference（meta 值）作为兜底。
	finalInference := usedModelInference
	// 落库
	msg, memoryFailed, err := s.persistAssistant(ctx, state.conversationID, state.conversationUUID, content, toRagCitations(state.citations), finalInference, state.userID, state.userRole, state.question)
	if err != nil {
		slog.WarnContext(ctx, "流式问答落库失败", "error", err)
	}
	msgID := ""
	if msg != nil {
		msgID = msg.UUID
	}
	return &StreamEvent{Type: "done", MessageID: msgID, MemoryExtractionFailed: memoryFailed}, nil
}

// toChatCitations 将 rag.CitationItem 列表转为 chat.CitationItem 列表。
func toChatCitations(items []rag.CitationItem) []CitationItem {
	out := make([]CitationItem, 0, len(items))
	for _, c := range items {
		out = append(out, CitationItem{
			DocumentID: c.DocumentID,
			ChunkID:    c.ChunkID,
			FileName:   c.FileName,
			Score:      c.Score,
			Snippet:    c.Snippet,
		})
	}
	return out
}

// toRagCitations 将 chat.CitationItem 列表转回 rag.CitationItem 列表。
// 落库时用，因 persistAssistant 接受 rag.CitationItem。
func toRagCitations(items []CitationItem) []rag.CitationItem {
	out := make([]rag.CitationItem, 0, len(items))
	for _, c := range items {
		out = append(out, rag.CitationItem{
			DocumentID: c.DocumentID,
			ChunkID:    c.ChunkID,
			FileName:   c.FileName,
			Score:      c.Score,
			Snippet:    c.Snippet,
		})
	}
	return out
}

// loadEnabledMemories 加载用户启用的长期记忆内容列表，用于注入问答 prompt。
func (s *ChatService) loadEnabledMemories(ctx context.Context, userID int64) []string {
	memSvc := NewMemoryService(s.db, nil, s.audit)
	rows, err := memSvc.ListEnabled(ctx, userID)
	if err != nil {
		slog.WarnContext(ctx, "加载启用长期记忆失败", "user_id", userID, "error", err)
		return nil
	}
	out := make([]string, 0, len(rows))
	for _, m := range rows {
		out = append(out, m.Content)
	}
	return out
}

// MemoryService 用户长期记忆服务，由 LLM 决策提取、只自动保存低风险偏好。
type MemoryService struct {
	db    *sql.DB
	llm   common.LLMProvider
	audit *audit.AuditService
}

// NewMemoryService 创建记忆服务。
//
// 参数：
//   - db: 已就绪的 MySQL 连接池。
//   - llm: LLM 提供者，用于记忆提取；读操作（列表/启用/删除）可传 nil。
//   - auditSvc: 审计服务。
func NewMemoryService(db *sql.DB, llm common.LLMProvider, auditSvc *audit.AuditService) *MemoryService {
	return &MemoryService{db: db, llm: llm, audit: auditSvc}
}

// ListEnabled 加载用户启用的长期记忆，按 created_at 升序。
func (s *MemoryService) ListEnabled(ctx context.Context, userID int64) ([]*UserMemory, error) {
	return ListEnabledUserMemoriesByUser(ctx, s.db, userID)
}

// ListMemories 列出用户全部长期记忆，按 created_at 降序。
func (s *MemoryService) ListMemories(ctx context.Context, userID int64) ([]*UserMemory, error) {
	return ListUserMemoriesByUser(ctx, s.db, userID)
}

// DeleteMemory 删除指定记忆。仅可删除自己的记忆，否则返回 NotFound。
func (s *MemoryService) DeleteMemory(ctx context.Context, userID int64, memoryUUID string) error {
	m, err := GetUserMemoryByUUID(ctx, s.db, memoryUUID)
	if err != nil {
		return common.SystemError(err)
	}
	if m == nil || m.UserID != userID {
		return common.NotFound("记忆不存在")
	}
	if err := DeleteUserMemoryByID(ctx, s.db, m.ID); err != nil {
		return common.SystemError(err)
	}
	s.audit.RecordEvent(ctx, audit.AuditEvent{
		EventType:    "memory_deleted",
		ActorUserID:  sql.NullInt64{Int64: userID, Valid: userID > 0},
		RequestID:    common.RequestIDFromContext(ctx),
		ResourceType: sql.NullString{String: "memory", Valid: true},
		ResourceID:   sql.NullString{String: m.UUID, Valid: true},
		Action:       sql.NullString{String: "delete", Valid: true},
		Status:       sql.NullString{String: "success", Valid: true},
	})
	return nil
}

// SetMemoryEnabled 启用或关闭长期记忆。仅可操作自己的记忆，否则返回 NotFound。
func (s *MemoryService) SetMemoryEnabled(ctx context.Context, userID int64, memoryUUID string, enabled bool) (*UserMemory, error) {
	m, err := GetUserMemoryByUUID(ctx, s.db, memoryUUID)
	if err != nil {
		return nil, common.SystemError(err)
	}
	if m == nil || m.UserID != userID {
		return nil, common.NotFound("记忆不存在")
	}
	if err := UpdateUserMemoryEnabled(ctx, s.db, m.ID, enabled); err != nil {
		return nil, common.SystemError(err)
	}
	m.Enabled = enabled
	s.audit.RecordEvent(ctx, audit.AuditEvent{
		EventType:    "memory_updated",
		ActorUserID:  sql.NullInt64{Int64: userID, Valid: userID > 0},
		RequestID:    common.RequestIDFromContext(ctx),
		ResourceType: sql.NullString{String: "memory", Valid: true},
		ResourceID:   sql.NullString{String: m.UUID, Valid: true},
		Action:       sql.NullString{String: "update", Valid: true},
		Status:       sql.NullString{String: "success", Valid: true},
		Metadata:     marshalJSON(map[string]any{"enabled": enabled}),
	})
	return m, nil
}

// ExtractAndSave LLM 从问答中决策提取长期记忆，过滤敏感项与重复后自动保存。
//
// 由大模型判断用户问答中值得长期记住的偏好（环境/集群/组件/业务域/回答风格/其他偏好），
// 输出结构化 JSON；命中敏感词黑名单或超长则作为防御性二次过滤丢弃。
// LLM 调用失败时返回错误（不保存任何记忆），并写入 memory_extraction failed 审计，
// 调用方据此向用户提示；失败不影响问答结果本身。
//
// 去重/更新策略：
//   - 结构化偏好（非 preference）按 (user_id, memory_type) upsert：
//     同类型已有记忆则更新 content 并写 memory_updated 审计，避免同类冲突记忆累积；
//     不存在则新增。
//   - preference 兜底按 content 精确去重后新增。
//
// 返回本次新保存或更新的记忆条数。
func (s *MemoryService) ExtractAndSave(ctx context.Context, userID int64, question, answer, actorRole string) (int, error) {
	candidates, err := s.extractWithLLM(ctx, question, answer)
	if err != nil {
		// 提取失败写审计，避免失败仅留在运行日志里无从追查
		s.audit.RecordEvent(ctx, audit.AuditEvent{
			EventType:    "memory_extraction",
			ActorUserID:  sql.NullInt64{Int64: userID, Valid: userID > 0},
			RequestID:    common.RequestIDFromContext(ctx),
			ActorRole:    sql.NullString{String: actorRole, Valid: actorRole != ""},
			ResourceType: sql.NullString{String: "memory", Valid: true},
			Action:       sql.NullString{String: "extract", Valid: true},
			Status:       sql.NullString{String: "failed", Valid: true},
			Metadata:     marshalJSON(map[string]any{"error": err.Error()}),
		})
		return 0, err
	}
	saved := 0
	for _, c := range candidates {
		if isSensitive(c.Content) {
			continue
		}
		if c.MemoryType != "preference" {
			existing, err := GetUserMemoryByUserAndType(ctx, s.db, userID, c.MemoryType)
			if err != nil {
				return saved, common.SystemError(err)
			}
			if existing == nil {
				if _, err := s.createMemory(ctx, userID, c.MemoryType, c.Content, actorRole); err != nil {
					return saved, err
				}
				saved++
			} else if existing.Content != c.Content {
				if err := UpdateUserMemoryContent(ctx, s.db, existing.ID, c.Content); err != nil {
					return saved, common.SystemError(err)
				}
				s.audit.RecordEvent(ctx, audit.AuditEvent{
					EventType:    "memory_updated",
					ActorUserID:  sql.NullInt64{Int64: userID, Valid: userID > 0},
					RequestID:    common.RequestIDFromContext(ctx),
					ActorRole:    sql.NullString{String: actorRole, Valid: actorRole != ""},
					ResourceType: sql.NullString{String: "memory", Valid: true},
					ResourceID:   sql.NullString{String: existing.UUID, Valid: true},
					Action:       sql.NullString{String: "update", Valid: true},
					Status:       sql.NullString{String: "success", Valid: true},
					Metadata:     marshalJSON(map[string]any{"memory_type": c.MemoryType}),
				})
				saved++
			}
			// content 与已有完全相同则跳过
			continue
		}
		// preference 兜底：按 content 去重后新增
		exists, err := ExistsUserMemoryByUserAndContent(ctx, s.db, userID, c.Content)
		if err != nil {
			return saved, common.SystemError(err)
		}
		if exists {
			continue
		}
		if _, err := s.createMemory(ctx, userID, c.MemoryType, c.Content, actorRole); err != nil {
			return saved, err
		}
		saved++
	}
	return saved, nil
}

// createMemory 新增一条长期记忆并写 memory_created 审计。
func (s *MemoryService) createMemory(ctx context.Context, userID int64, memoryType, content, actorRole string) (*UserMemory, error) {
	id, memUUID, err := InsertUserMemory(ctx, s.db, userID, memoryType, content)
	if err != nil {
		return nil, common.SystemError(err)
	}
	s.audit.RecordEvent(ctx, audit.AuditEvent{
		EventType:    "memory_created",
		ActorUserID:  sql.NullInt64{Int64: userID, Valid: userID > 0},
		RequestID:    common.RequestIDFromContext(ctx),
		ActorRole:    sql.NullString{String: actorRole, Valid: actorRole != ""},
		ResourceType: sql.NullString{String: "memory", Valid: true},
		ResourceID:   sql.NullString{String: memUUID, Valid: true},
		Action:       sql.NullString{String: "create", Valid: true},
		Status:       sql.NullString{String: "success", Valid: true},
		Metadata:     marshalJSON(map[string]any{"memory_type": memoryType}),
	})
	return &UserMemory{ID: id, UUID: memUUID, UserID: userID, MemoryType: memoryType, Content: content, Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}, nil
}

// memoryCandidate LLM 提取的记忆候选。
type memoryCandidate struct {
	MemoryType string // 记忆类型枚举
	Content    string // 套用模板后的内容
}

// extractWithLLM 调用 LLM 从问答中决策提取值得长期记住的偏好候选。
//
// prompt 限定只提取用户主动表达的稳定偏好，禁止提取凭证/隐私等敏感信息；
// 输出 JSON 数组 [{memory_type, value}]。LLM 输出不可解析时返回空候选；
// LLM 调用失败时返回 error，由上层记录审计并向前端暴露失败标识。
func (s *MemoryService) extractWithLLM(ctx context.Context, question, answer string) ([]memoryCandidate, error) {
	if s.llm == nil {
		return nil, nil
	}
	systemPrompt := "你是用户偏好记忆提取助手。阅读以下用户问答，判断是否有值得长期记住的" +
		"用户偏好（环境/集群/知识库/组件/业务域/回答风格/其他偏好）。\n" +
		"- 只提取用户主动表达的稳定偏好，不提取一次性查询意图、疑问、事实询问。\n" +
		"- 禁止提取：密码、token、密钥、私钥、故障细节、生产变更参数、个人隐私、" +
		"大段工具输出、未脱敏内部敏感信息。\n" +
		"- 输出 JSON 数组，每项 {\"memory_type\": 枚举值, \"value\": 简洁值}；" +
		"无可记则输出 []。\n" +
		"- memory_type 限定枚举：" + strings.Join(allowedMemoryTypes, ", ") + "。\n" +
		"- 只输出 JSON，不要任何解释。"
	messages := []common.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: "用户问题：" + question + "\n助手回答：" + answer},
	}
	raw, err := s.llm.Chat(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("memory extraction LLM call: %w", err)
	}
	return parseMemoryCandidates(raw), nil
}

// parseMemoryCandidates 从 LLM 输出解析 (memory_type, content) 候选。
// 容忍模型在 JSON 外附加说明文本，提取首个 JSON 数组并校验类型枚举与值非空。
func parseMemoryCandidates(raw string) []memoryCandidate {
	match := memoryArrayRE.FindString(raw)
	if match == "" {
		return nil
	}
	var data []struct {
		MemoryType string `json:"memory_type"`
		Value      string `json:"value"`
	}
	if err := json.Unmarshal([]byte(match), &data); err != nil {
		return nil
	}
	if len(data) == 0 {
		return nil
	}
	// 校验枚举
	allowed := make(map[string]bool, len(allowedMemoryTypes))
	for _, t := range allowedMemoryTypes {
		allowed[t] = true
	}
	out := make([]memoryCandidate, 0, len(data))
	for _, item := range data {
		if !allowed[item.MemoryType] {
			continue
		}
		value := strings.TrimSpace(item.Value)
		if value == "" {
			continue
		}
		tmpl, ok := memoryTemplates[item.MemoryType]
		if !ok {
			continue
		}
		content := strings.ReplaceAll(tmpl, "{value}", value)
		out = append(out, memoryCandidate{MemoryType: item.MemoryType, Content: content})
	}
	return out
}

// isSensitive 判断候选记忆是否敏感（命中黑名单关键词或超长）。
func isSensitive(content string) bool {
	// 按 Unicode 字符计长，对齐 Python len(content)（M12，避免中文记忆被按字节过度过滤）
	if utf8.RuneCountInString(content) > maxMemoryLength {
		return true
	}
	lower := strings.ToLower(content)
	for _, kw := range sensitiveKeywords {
		if strings.Contains(lower, kw) {
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
