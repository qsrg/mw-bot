// File router.go: 聊天 HTTP 处理器，对齐 Python app/chat/router.py。
//
// 路由（所有路由前置 AuthMiddleware）：
//   - POST /api/chat/messages                       同步问答（?conversation_id=ID 续接会话）
//   - POST /api/chat/messages/stream                流式问答（SSE，事件序列 meta→reasoning→delta→done）
//   - GET  /api/chat/conversations                  列出当前用户的历史会话
//   - DELETE /api/chat/conversations/{id}           删除指定会话（硬删除，级联清理消息与引用）
//   - GET  /api/chat/conversations/{id}/messages    列出指定会话的消息与引用
//   - GET  /api/chat/memories                       列出当前用户的长期记忆
//   - DELETE /api/chat/memories/{id}                删除指定长期记忆
//   - PATCH /api/chat/memories/{id}?enabled=true    启用/禁用长期记忆
package chat

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"mw-bot/internal/audit"
	"mw-bot/internal/auth"
	"mw-bot/internal/common"
	"mw-bot/internal/rag"
)

// Handler 聊天 HTTP 处理器，封装 db/rag/audit/settings/auth 依赖。
type Handler struct {
	db          *sql.DB
	chatService *ChatService
	authHandler *auth.Handler
}

// NewHandler 创建聊天处理器。
//
// 参数：
//   - db: 已就绪的 MySQL 连接池。
//   - ragSvc: RAG 服务，注入到 ChatService。
//   - auditSvc: 审计服务。
//   - settings: 应用配置（提供 HistoryTokenBudget 等记忆压缩参数）。
//   - authHandler: 认证处理器（复用 AuthMiddleware）。
func NewHandler(
	db *sql.DB,
	ragSvc *rag.RagService,
	auditSvc *audit.AuditService,
	settings common.Settings,
	authHandler *auth.Handler,
) *Handler {
	return &Handler{
		db:          db,
		chatService: NewChatService(db, ragSvc, auditSvc, settings),
		authHandler: authHandler,
	}
}

// RegisterRoutes 注册聊天路由到 mux。
// 所有路由前置 AuthMiddleware，要求调用方登录。
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("/api/chat/messages", h.authHandler.AuthMiddleware(http.HandlerFunc(h.messagesRoot)))
	mux.Handle("/api/chat/messages/stream", h.authHandler.AuthMiddleware(http.HandlerFunc(h.askStream)))
	mux.Handle("/api/chat/conversations", h.authHandler.AuthMiddleware(http.HandlerFunc(h.conversationsRoot)))
	mux.Handle("/api/chat/conversations/", h.authHandler.AuthMiddleware(http.HandlerFunc(h.conversationByID)))
	mux.Handle("/api/chat/memories", h.authHandler.AuthMiddleware(http.HandlerFunc(h.memoriesRoot)))
	mux.Handle("/api/chat/memories/", h.authHandler.AuthMiddleware(http.HandlerFunc(h.memoryByID)))
}

// messagesRoot 处理 /api/chat/messages 的 POST（同步问答）。
func (h *Handler) messagesRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.MethodNotAllowed(w)
		return
	}
	identity := auth.IdentityFromContext(r.Context())
	if identity == nil {
		common.WriteError(w, common.Unauthorized("未登录"))
		return
	}

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		common.WriteError(w, common.BusinessError("invalid JSON body: "+err.Error()))
		return
	}
	if req.Question == "" {
		common.WriteError(w, common.BusinessError("问题不能为空"))
		return
	}
	conversationID := r.URL.Query().Get("conversation_id")

	result, err := h.chatService.Ask(r.Context(), conversationID, req.Question, identity.UserID, identity.Role)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ChatResponse{
		MessageID:              result.Message.UUID,
		ConversationID:         result.Conversation.UUID,
		Content:                result.RagAnswer.Content,
		Citations:              toChatCitations(result.RagAnswer.Citations),
		UsedModelInference:     result.RagAnswer.UsedModelInference,
		MemoryExtractionFailed: result.MemoryExtractionFailed,
	})
}

// askStream 处理 POST /api/chat/messages/stream：流式问答（SSE）。
// 事件序列：meta（会话/引用/推断标识）→ reasoning（推理思考链，多次，可选）
// → delta（正式回答文本片段，多次）→ done（助手消息ID）。
func (h *Handler) askStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.MethodNotAllowed(w)
		return
	}
	identity := auth.IdentityFromContext(r.Context())
	if identity == nil {
		common.WriteError(w, common.Unauthorized("未登录"))
		return
	}

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		common.WriteError(w, common.BusinessError("invalid JSON body: "+err.Error()))
		return
	}
	if req.Question == "" {
		common.WriteError(w, common.BusinessError("问题不能为空"))
		return
	}
	conversationID := r.URL.Query().Get("conversation_id")

	_, next, err := h.chatService.AskStream(r.Context(), conversationID, req.Question, identity.UserID, identity.Role)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	// SSE 响应头
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // 关闭 Nginx 缓冲，确保实时透传
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	ctx := r.Context()
	for {
		event, err := next()
		if err != nil {
			// ioEOF 或 ctx 取消：终止事件流
			if errors.Is(err, ioEOF) || errors.Is(err, ctx.Err()) {
				break
			}
			slog.WarnContext(ctx, "流式问答事件流异常", "error", err)
			break
		}
		data, mErr := json.Marshal(event)
		if mErr != nil {
			slog.WarnContext(ctx, "序列化流式事件失败", "error", mErr)
			continue
		}
		if _, wErr := fmt.Fprintf(w, "data: %s\n\n", string(data)); wErr != nil {
			// 客户端断开连接
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	// 流结束标记（与 Python 一致）
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// conversationsRoot 处理 /api/chat/conversations 的 GET（列表）。
func (h *Handler) conversationsRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.MethodNotAllowed(w)
		return
	}
	identity := auth.IdentityFromContext(r.Context())
	if identity == nil {
		common.WriteError(w, common.Unauthorized("未登录"))
		return
	}
	convs, err := h.chatService.ListConversations(r.Context(), identity.UserID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	resp := make([]ConversationSummary, 0, len(convs))
	for _, c := range convs {
		resp = append(resp, ConversationSummary{
			ID:        c.UUID,
			Title:     c.Title,
			UpdatedAt: c.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// conversationByID 处理 /api/chat/conversations/{id} 与 /api/chat/conversations/{id}/messages。
// 路径解析：截取 /api/chat/conversations/ 后的部分，按 "/" 切分为 convID 与可选子路径。
func (h *Handler) conversationByID(w http.ResponseWriter, r *http.Request) {
	identity := auth.IdentityFromContext(r.Context())
	if identity == nil {
		common.WriteError(w, common.Unauthorized("未登录"))
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/chat/conversations/")
	parts := strings.SplitN(path, "/", 2)
	convID := parts[0]
	if convID == "" {
		common.WriteError(w, common.NotFound("会话不存在"))
		return
	}
	// 子路径 /messages 列出消息
	if len(parts) == 2 && parts[1] == "messages" {
		if r.Method != http.MethodGet {
			common.MethodNotAllowed(w)
			return
		}
		h.listMessages(w, r, convID, identity.UserID)
		return
	}
	// 无子路径，仅支持 DELETE
	if r.Method != http.MethodDelete {
		common.MethodNotAllowed(w)
		return
	}
	h.deleteConversation(w, r, convID, identity.UserID, identity.Role)
}

// deleteConversation 处理 DELETE /api/chat/conversations/{id}：硬删除会话。
func (h *Handler) deleteConversation(w http.ResponseWriter, r *http.Request, convID string, userID int64, role string) {
	if err := h.chatService.DeleteConversation(r.Context(), userID, convID, role); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, DeleteStatusResponse{Status: "deleted"})
}

// listMessages 处理 GET /api/chat/conversations/{id}/messages：列出消息与引用。
func (h *Handler) listMessages(w http.ResponseWriter, r *http.Request, convID string, userID int64) {
	messages, refs, err := h.chatService.ListMessagesWithRefs(r.Context(), userID, convID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	// refs 按 message_id 聚合
	refsByMsg := make(map[int64][]*MessageReference)
	for _, ref := range refs {
		refsByMsg[ref.MessageID] = append(refsByMsg[ref.MessageID], ref)
	}
	resp := make([]MessageItem, 0, len(messages))
	for _, m := range messages {
		msgRefs := refsByMsg[m.ID]
		citations := make([]CitationItem, 0, len(msgRefs))
		for _, ref := range msgRefs {
			// 与 Python router.py 一致：仅返回 file_name/score/snippet
			fileName := ref.FileName
			citations = append(citations, CitationItem{
				FileName: &fileName,
				Score:    ref.Score,
				Snippet:  ref.Snippet,
			})
		}
		resp = append(resp, MessageItem{
			ID:                 m.UUID,
			Role:               m.Role,
			Content:            m.Content,
			Citations:          citations,
			UsedModelInference: m.UsedModelInference,
			CreatedAt:          m.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// memoriesRoot 处理 /api/chat/memories 的 GET（列表）。
func (h *Handler) memoriesRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.MethodNotAllowed(w)
		return
	}
	identity := auth.IdentityFromContext(r.Context())
	if identity == nil {
		common.WriteError(w, common.Unauthorized("未登录"))
		return
	}
	memSvc := NewMemoryService(h.db, nil, nil)
	memories, err := memSvc.ListMemories(r.Context(), identity.UserID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	resp := make([]MemoryItem, 0, len(memories))
	for _, m := range memories {
		resp = append(resp, MemoryItem{
			ID:         m.UUID,
			MemoryType: m.MemoryType,
			Content:    m.Content,
			Enabled:    m.Enabled,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// memoryByID 处理 /api/chat/memories/{id}：DELETE（删除）与 PATCH（启用/禁用）。
// PATCH 通过 ?enabled=true|false 查询参数切换启用状态，与 Python router.py 一致。
func (h *Handler) memoryByID(w http.ResponseWriter, r *http.Request) {
	identity := auth.IdentityFromContext(r.Context())
	if identity == nil {
		common.WriteError(w, common.Unauthorized("未登录"))
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/chat/memories/")
	parts := strings.SplitN(path, "/", 2)
	memID := parts[0]
	if memID == "" {
		common.WriteError(w, common.NotFound("记忆不存在"))
		return
	}
	if len(parts) > 1 {
		common.WriteError(w, common.NotFound("记忆不存在"))
		return
	}
	memSvc := NewMemoryService(h.db, nil, nil)
	switch r.Method {
	case http.MethodDelete:
		if err := memSvc.DeleteMemory(r.Context(), identity.UserID, memID); err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, DeleteStatusResponse{Status: "deleted"})
	case http.MethodPatch:
		enabledStr := r.URL.Query().Get("enabled")
		enabled, err := strconv.ParseBool(enabledStr)
		if err != nil {
			common.WriteError(w, common.BusinessError("enabled 参数非法，需 true/false"))
			return
		}
		m, err := memSvc.SetMemoryEnabled(r.Context(), identity.UserID, memID, enabled)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, MemoryItem{
			ID:         m.UUID,
			MemoryType: m.MemoryType,
			Content:    m.Content,
			Enabled:    m.Enabled,
		})
	default:
		common.MethodNotAllowed(w)
	}
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
