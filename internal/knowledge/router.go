// File router.go: 知识库 HTTP 处理器。
//
// 路由（所有路由前置 AuthMiddleware）：
//   - POST   /api/knowledge/documents                上传文档（multipart: file + knowledge_base_id，仅 Markdown）
//                                                  或在线新建（JSON: file_name + content + knowledge_base_id）
//   - GET    /api/knowledge/documents                列出文档（?knowledge_base_id=ID）
//   - PUT    /api/knowledge/documents/{document_id}  在线更新 Markdown 内容并重建索引
//   - DELETE /api/knowledge/documents/{document_id}  删除文档
//   - GET    /api/knowledge/documents/{document_id}/content  下载/预览内容流
//
// 权限：上传/新建/更新/列出/下载需 document.upload 权限，删除需 document.delete 权限。
package knowledge

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"mw-bot/internal/audit"
	"mw-bot/internal/auth"
	"mw-bot/internal/common"
)

// maxUploadBytes 上传文件最大字节数（100MB），与 FastAPI 默认上限量级一致。
const maxUploadBytes = 100 << 20

// maxJSONBodyBytes JSON 请求体（在线新建/更新）最大字节数，需覆盖 maxMarkdownBytes 内容上限。
const maxJSONBodyBytes = maxMarkdownBytes + 64<<10

// Handler 知识库 HTTP 处理器，封装 db/storage/vector_store/audit/auth 依赖。
type Handler struct {
	db             *sql.DB
	storage        common.FileStorage
	vectorStore    common.VectorStore
	audit          *audit.AuditService
	authHandler    *auth.Handler
	taskDispatcher TaskDispatcher
}

// NewHandler 创建知识库处理器。
//
// 参数：
//   - db: 已就绪的 MySQL 连接池。
//   - storage: 文件存储实现（仅用于清理历史文件型文档的旧文件）。
//   - vectorStore: 向量库实例。
//   - auditSvc: 审计服务。
//   - authHandler: 认证处理器（复用 AuthMiddleware）。
//   - taskDispatcher: 索引任务投递回调（可为 nil，list/delete 场景不用）。
func NewHandler(
	db *sql.DB,
	storage common.FileStorage,
	vectorStore common.VectorStore,
	auditSvc *audit.AuditService,
	authHandler *auth.Handler,
	taskDispatcher TaskDispatcher,
) *Handler {
	return &Handler{
		db:             db,
		storage:        storage,
		vectorStore:    vectorStore,
		audit:          auditSvc,
		authHandler:    authHandler,
		taskDispatcher: taskDispatcher,
	}
}

// RegisterRoutes 注册知识库路由到 mux。
// 所有路由前置 AuthMiddleware，要求调用方登录。
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("/api/knowledge/documents", h.authHandler.AuthMiddleware(http.HandlerFunc(h.documentsRoot)))
	mux.Handle("/api/knowledge/documents/", h.authHandler.AuthMiddleware(http.HandlerFunc(h.documentByID)))
}

// documentsRoot 处理 /api/knowledge/documents 的 POST（上传/在线新建）与 GET（列表）。
// POST 按 Content-Type 分流：application/json 走在线新建，multipart 走文件上传。
func (h *Handler) documentsRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		if isJSONRequest(r) {
			h.createDocument(w, r)
		} else {
			h.uploadDocument(w, r)
		}
	case http.MethodGet:
		h.listDocuments(w, r)
	default:
		common.MethodNotAllowed(w)
	}
}

// isJSONRequest 判断请求是否为 JSON 体（按 Content-Type 头）。
func isJSONRequest(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return strings.HasPrefix(strings.TrimSpace(r.Header.Get("Content-Type")), "application/json")
	}
	return mediaType == "application/json"
}

// documentByID 处理 /api/knowledge/documents/{id} 与 /api/knowledge/documents/{id}/content。
// 路径解析：截取 /api/knowledge/documents/ 后的部分，按 "/" 切分为 docID 与可选子路径。
func (h *Handler) documentByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/knowledge/documents/")
	parts := strings.SplitN(path, "/", 2)
	docID := parts[0]
	if docID == "" {
		common.WriteError(w, common.NotFound("文档不存在"))
		return
	}
	// 子路径 /content 流式下载
	if len(parts) == 2 && parts[1] == "content" {
		if r.Method != http.MethodGet {
			common.MethodNotAllowed(w)
			return
		}
		h.getDocumentContent(w, r, docID)
		return
	}
	// 无子路径，支持 PUT（在线更新）与 DELETE
	switch r.Method {
	case http.MethodPut:
		h.updateDocument(w, r, docID)
	case http.MethodDelete:
		h.deleteDocument(w, r, docID)
	default:
		common.MethodNotAllowed(w)
	}
}

// uploadDocument 处理 POST /api/knowledge/documents：multipart 上传文件，需 document.upload 权限。
func (h *Handler) uploadDocument(w http.ResponseWriter, r *http.Request) {
	identity := auth.IdentityFromContext(r.Context())
	if appErr := auth.RequirePermission(identity, "document.upload"); appErr != nil {
		common.WriteError(w, appErr)
		return
	}
	// 限制上传体积，避免内存爆炸
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		common.WriteError(w, common.BusinessError("解析 multipart 表单失败："+err.Error()))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		common.WriteError(w, common.BusinessError("缺少 file 字段"))
		return
	}
	defer file.Close()

	// knowledge_base_id 默认 1（与 Python Form(1) 一致）
	kbIDStr := r.FormValue("knowledge_base_id")
	if kbIDStr == "" {
		kbIDStr = "1"
	}
	kbID, err := strconv.ParseInt(kbIDStr, 10, 64)
	if err != nil {
		common.WriteError(w, common.BusinessError("knowledge_base_id 格式非法"))
		return
	}

	fileName := header.Filename
	if fileName == "" {
		fileName = "unnamed"
	}
	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	svc := h.newService()
	doc, err := svc.CreateDocumentUpload(r.Context(), kbID, fileName, contentType, file, identity.UserID, identity.Role)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toDocumentResponse(doc))
}

// createDocument 处理 JSON POST /api/knowledge/documents：在线新建 Markdown 文档，需 document.upload 权限。
func (h *Handler) createDocument(w http.ResponseWriter, r *http.Request) {
	identity := auth.IdentityFromContext(r.Context())
	if appErr := auth.RequirePermission(identity, "document.upload"); appErr != nil {
		common.WriteError(w, appErr)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	var req CreateDocumentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		common.WriteError(w, common.BusinessError("解析 JSON 请求体失败："+err.Error()))
		return
	}
	if strings.TrimSpace(req.FileName) == "" {
		common.WriteError(w, common.BusinessError("file_name 不能为空"))
		return
	}
	if req.KnowledgeBaseID <= 0 {
		req.KnowledgeBaseID = 1
	}
	svc := h.newService()
	doc, err := svc.CreateMarkdownDocument(r.Context(), req.KnowledgeBaseID,
		req.FileName, req.Content, identity.UserID, identity.Role, "document_created")
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toDocumentResponse(doc))
}

// updateDocument 处理 PUT /api/knowledge/documents/glm-5.3_common：在线更新 Markdown 内容并重建索引，
// 需 document.upload 权限。
func (h *Handler) updateDocument(w http.ResponseWriter, r *http.Request, docID string) {
	identity := auth.IdentityFromContext(r.Context())
	if appErr := auth.RequirePermission(identity, "document.upload"); appErr != nil {
		common.WriteError(w, appErr)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	var req UpdateDocumentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		common.WriteError(w, common.BusinessError("解析 JSON 请求体失败："+err.Error()))
		return
	}
	svc := h.newService()
	doc, err := svc.UpdateDocumentContent(r.Context(), docID, req.FileName, req.Content,
		identity.UserID, identity.Role)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toDocumentResponse(doc))
}

// listDocuments 处理 GET /api/knowledge/documents：列出文档，需 document.upload 权限。
func (h *Handler) listDocuments(w http.ResponseWriter, r *http.Request) {
	identity := auth.IdentityFromContext(r.Context())
	if appErr := auth.RequirePermission(identity, "document.upload"); appErr != nil {
		common.WriteError(w, appErr)
		return
	}
	var kbID int64
	if v := r.URL.Query().Get("knowledge_base_id"); v != "" {
		var err error
		kbID, err = strconv.ParseInt(v, 10, 64)
		if err != nil {
			common.WriteError(w, common.BusinessError("knowledge_base_id 格式非法"))
			return
		}
	}
	svc := h.newService()
	docs, err := svc.ListDocuments(r.Context(), kbID)
	if err != nil {
		common.WriteError(w, common.SystemError(err))
		return
	}
	resp := make([]DocumentResponse, 0, len(docs))
	for _, d := range docs {
		resp = append(resp, toDocumentResponse(d))
	}
	writeJSON(w, http.StatusOK, resp)
}

// deleteDocument 处理 DELETE /api/knowledge/documents/{id}：删除文档，需 document.delete 权限。
func (h *Handler) deleteDocument(w http.ResponseWriter, r *http.Request, docID string) {
	identity := auth.IdentityFromContext(r.Context())
	if appErr := auth.RequirePermission(identity, "document.delete"); appErr != nil {
		common.WriteError(w, appErr)
		return
	}
	svc := h.newService()
	if err := svc.DeleteDocument(r.Context(), docID, identity.UserID, identity.Role); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// getDocumentContent 处理 GET /api/knowledge/documents/{id}/content：流式返回原始文件，需 document.upload 权限。
// Content-Disposition 设为 inline，便于 PDF 等类型在浏览器内直接渲染。
func (h *Handler) getDocumentContent(w http.ResponseWriter, r *http.Request, docID string) {
	identity := auth.IdentityFromContext(r.Context())
	if appErr := auth.RequirePermission(identity, "document.upload"); appErr != nil {
		common.WriteError(w, appErr)
		return
	}
	svc := h.newService()
	doc, stream, err := svc.GetDocumentStream(r.Context(), docID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	defer stream.Close()

	contentType := doc.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", contentDisposition(doc.FileName, "inline"))
	if _, err := io.Copy(w, stream); err != nil {
		// 流式响应中 header 已写，无法再切换错误响应，仅记录日志
		slog.WarnContext(r.Context(), "stream document content failed",
			"document_id", docID, "error", err.Error())
		return
	}
}

// newService 创建 KnowledgeService 实例，注入 handler 持有的依赖。
func (h *Handler) newService() *KnowledgeService {
	return NewKnowledgeService(h.db, h.storage, h.taskDispatcher, h.vectorStore, h.audit)
}

// toDocumentResponse 将 Document 转换为响应。
// id 为文档 uuid（对外标识），与 Python DocumentResponse.id 一致。
func toDocumentResponse(d *Document) DocumentResponse {
	return DocumentResponse{
		ID:             d.UUID,
		FileName:       d.FileName,
		ContentType:    d.ContentType,
		FileSize:       d.FileSize,
		StorageBackend: d.StorageBackend,
		IndexStatus:    d.IndexStatus,
		CreatedAt:      d.CreatedAt,
	}
}

// contentDisposition 生成 ASCII 安全的 Content-Disposition 头。
// HTTP 头只允许 Latin-1，文件名含中文等非 ASCII 字符时直接拼接会触发 500。
// 用 RFC 5987 的 filename* 携带原始 UTF-8 文件名，并提供去掉非 ASCII 字符的 fallback filename。
func contentDisposition(filename, disposition string) string {
	// 去掉非 ASCII 与双引号，作为 fallback
	ascii := strings.Map(func(r rune) rune {
		if r > 127 || r == '"' {
			return -1
		}
		return r
	}, filename)
	if ascii == "" {
		ascii = "document"
	}
	// filename* 用 URL 编码（RFC 5987），保留 UTF-8 原文件名
	encoded := url.PathEscape(filename)
	return fmt.Sprintf("%s; filename=\"%s\"; filename*=UTF-8''%s", disposition, ascii, encoded)
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
