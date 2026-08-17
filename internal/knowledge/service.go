// File service.go: 知识库服务，封装文档上传、查询、删除与索引任务投递，
// 对齐 Python app/knowledge/service.py。
package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"mw-bot/internal/audit"
	"mw-bot/internal/common"
)

// TaskDispatcher 索引任务投递回调，接收文档自增主键 ID。
// 实际实现由 ingestion 模块提供（Task 12），测试场景可传 nil。
type TaskDispatcher func(documentID int64)

// maxMarkdownBytes Markdown 文档内容上限（8MB），需低于 content 列 MEDIUMTEXT 的 16MB 上限。
const maxMarkdownBytes = 8 << 20

// markdownContentType Markdown 文档统一记录的内容类型。
const markdownContentType = "text/markdown"

// isMarkdown 判断上传/新建的文件是否为 Markdown（按扩展名或内容类型）。
// 当前仅支持 Markdown 入库，其他格式直接拒绝。
func isMarkdown(fileName, contentType string) bool {
	lower := strings.ToLower(fileName)
	if strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".markdown") {
		return true
	}
	ct := strings.ToLower(strings.TrimSpace(contentType))
	return ct == "text/markdown" || ct == "text/plain"
}

// normalizeMarkdownFileName 保证文件名带 .md 扩展名，供 ingestion 按扩展名选择解析器。
func normalizeMarkdownFileName(fileName string) string {
	lower := strings.ToLower(fileName)
	if strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".markdown") {
		return fileName
	}
	return fileName + ".md"
}

// KnowledgeService 文档上传与索引投递服务。
type KnowledgeService struct {
	db             *sql.DB            // 数据库连接池
	storage        common.FileStorage // 文件存储实现（仅用于清理历史文件型文档的旧文件）
	taskDispatcher TaskDispatcher     // 索引任务投递回调
	vectorStore    common.VectorStore // 向量库，删除文档时清理 chunk
	audit          *audit.AuditService // 审计服务
}

// NewKnowledgeService 创建知识库服务实例。
//
// 参数：
//   - db: 已就绪的 MySQL 连接池。
//   - storage: 文件存储实现（仅用于清理历史 local/minio 文档的旧文件）。
//   - taskDispatcher: 索引任务投递回调，可为 nil（list/delete 场景）。
//   - vectorStore: 向量库实例。
//   - auditSvc: 审计服务。
func NewKnowledgeService(
	db *sql.DB,
	storage common.FileStorage,
	taskDispatcher TaskDispatcher,
	vectorStore common.VectorStore,
	auditSvc *audit.AuditService,
) *KnowledgeService {
	return &KnowledgeService{
		db:             db,
		storage:        storage,
		taskDispatcher: taskDispatcher,
		vectorStore:    vectorStore,
		audit:          auditSvc,
	}
}

// CreateDocumentUpload 校验并接收上传的 Markdown 文件，内容直接入库（不再保存文件），
// 创建 pending 文档记录并投递索引任务，立即返回。
//
// 流程：
//  1. 仅支持 Markdown（按扩展名/内容类型判断），其他格式返回业务错误。
//  2. 委托 CreateMarkdownDocument 走入库 + 投递 + 审计流程。
func (s *KnowledgeService) CreateDocumentUpload(
	ctx context.Context,
	knowledgeBaseID int64,
	fileName, contentType string,
	fileStream io.Reader,
	uploadedBy int64,
	actorRole string,
) (*Document, error) {
	if !isMarkdown(fileName, contentType) {
		return nil, common.BusinessError("仅支持 Markdown（.md）文档，请先转换为 Markdown 后上传")
	}
	data, err := io.ReadAll(fileStream)
	if err != nil {
		return nil, fmt.Errorf("read file content: %w", err)
	}
	return s.CreateMarkdownDocument(ctx, knowledgeBaseID, fileName, string(data),
		uploadedBy, actorRole, "document_uploaded")
}

// CreateMarkdownDocument 在线新建（或上传）Markdown 文档：内容存入 documents.content，
// 存储后端标记为 db（不落文件存储），状态置 pending 并投递索引任务。
//
// 流程：
//  1. 规范文件名（补 .md 扩展名）、校验内容大小。
//  2. 去重：同知识库下相同 file_hash 已存在则直接返回已有文档。
//  3. 写 DB（content、storage_backend=db、index_status=pending），重载完整记录。
//  4. 投递索引任务（使用自增主键 id）。
//  5. 写 auditEventType 审计事件。
//
// 参数：
//   - ctx: 请求上下文。
//   - knowledgeBaseID: 知识库ID。
//   - fileName: 文件名。
//   - content: Markdown 全文。
//   - uploadedBy: 创建用户ID。
//   - actorRole: 创建用户角色（审计用）。
//   - auditEventType: 审计事件类型（上传为 document_uploaded，在线新建为 document_created）。
//
// 返回：
//   - *Document: 已创建（或已存在的）文档记录。
//   - error: 校验、写库或投递失败。
func (s *KnowledgeService) CreateMarkdownDocument(
	ctx context.Context,
	knowledgeBaseID int64,
	fileName, content string,
	uploadedBy int64,
	actorRole string,
	auditEventType string,
) (*Document, error) {
	fileName = normalizeMarkdownFileName(fileName)
	if strings.TrimSpace(fileName) == ".md" {
		return nil, common.BusinessError("文件名不能为空")
	}
	if int64(len(content)) > maxMarkdownBytes {
		return nil, common.BusinessError(fmt.Sprintf("文档内容超过上限（%d MB）", maxMarkdownBytes>>20))
	}

	fileHash := SHA256Bytes([]byte(content))
	fileSize := int64(len(content))

	// 去重：同知识库下相同 hash 已存在则直接返回
	existing, err := GetDocumentByHashAndKB(ctx, s.db, knowledgeBaseID, fileHash)
	if err != nil {
		return nil, fmt.Errorf("query existing document: %w", err)
	}
	if existing != nil {
		return existing, nil
	}

	// 内容入库：storage_backend=db，不写文件存储
	docUUID := uuid.New().String()
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO documents (uuid, knowledge_base_id, file_name, content_type, file_size, file_hash, storage_backend, bucket, object_key, content, index_status, uploaded_by)
		 VALUES (?, ?, ?, ?, ?, ?, 'db', NULL, '', ?, 'pending', ?)`,
		docUUID, knowledgeBaseID, fileName, markdownContentType, fileSize, fileHash, content, uploadedBy)
	if err != nil {
		return nil, fmt.Errorf("insert document: %w", err)
	}
	docID, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get last insert id: %w", err)
	}

	doc, err := GetDocumentByID(ctx, s.db, docID)
	if err != nil {
		return nil, fmt.Errorf("reload document: %w", err)
	}

	// 投递异步索引任务
	if s.taskDispatcher != nil {
		s.taskDispatcher(docID)
	}

	// 写审计事件，resource_id 记录 user.id 字符串（与 Python 一致）
	s.audit.RecordEvent(ctx, audit.AuditEvent{
		EventType:    auditEventType,
		ActorUserID:  sql.NullInt64{Int64: uploadedBy, Valid: uploadedBy > 0},
		ActorRole:    sql.NullString{String: actorRole, Valid: actorRole != ""},
		RequestID:    common.RequestIDFromContext(ctx),
		ResourceType: sql.NullString{String: "document", Valid: true},
		ResourceID:   sql.NullString{String: strconv.FormatInt(docID, 10), Valid: true},
		Action:       sql.NullString{String: "create", Valid: true},
		Status:       sql.NullString{String: "success", Valid: true},
		Metadata:     marshalJSON(map[string]any{"file_name": fileName, "size": fileSize}),
	})

	return doc, nil
}

// UpdateDocumentContent 在线更新 Markdown 文档内容：重算 hash/size 写回 DB，
// 状态重置为 pending 并重投索引任务；ingestion 幂等（写前先删旧向量），
// 向量与 BM25 索引随重索引自动刷新。
//
// 若文档原本是文件存储（历史 local/minio Markdown），更新后转为内容入库（db），
// 并 best-effort 清理旧存储文件。
//
// 参数：
//   - ctx: 请求上下文。
//   - documentID: 文档对外标识 uuid。
//   - fileName: 新文件名，空串表示保持原文件名。
//   - content: 新的 Markdown 全文。
//   - actorUserID: 操作者用户ID（审计用）。
//   - actorRole: 操作者角色（审计用）。
//
// 返回：
//   - *Document: 更新后的文档记录。
//   - error: 文档不存在、非 Markdown、校验或写库失败。
func (s *KnowledgeService) UpdateDocumentContent(
	ctx context.Context,
	documentID, fileName, content string,
	actorUserID int64,
	actorRole string,
) (*Document, error) {
	doc, err := GetDocumentByUUID(ctx, s.db, documentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, common.NotFound("文档不存在")
		}
		return nil, fmt.Errorf("query document: %w", err)
	}
	if !doc.IsMarkdownDocument() {
		return nil, common.BusinessError("仅支持 Markdown 文档在线编辑")
	}
	if fileName == "" {
		fileName = doc.FileName
	}
	fileName = normalizeMarkdownFileName(fileName)
	if int64(len(content)) > maxMarkdownBytes {
		return nil, common.BusinessError(fmt.Sprintf("文档内容超过上限（%d MB）", maxMarkdownBytes>>20))
	}

	fileHash := SHA256Bytes([]byte(content))
	fileSize := int64(len(content))

	// 历史文件存储的 Markdown 转为内容入库后，best-effort 清理旧文件
	oldBackend, oldObjectKey := doc.StorageBackend, doc.ObjectKey

	if _, err := s.db.ExecContext(ctx,
		`UPDATE documents SET file_name = ?, content = ?, file_size = ?, file_hash = ?,
		 storage_backend = 'db', bucket = NULL, object_key = '',
		 index_status = 'pending', index_error = NULL
		 WHERE id = ?`,
		fileName, content, fileSize, fileHash, doc.ID); err != nil {
		return nil, fmt.Errorf("update document: %w", err)
	}
	if oldBackend != StorageBackendDB && oldObjectKey != "" {
		if err := s.storage.Delete(ctx, oldObjectKey); err != nil {
			// 清理失败不影响更新结果，仅记录日志
			slog.WarnContext(ctx, "清理旧存储文件失败",
				"document_id", documentID, "object_key", oldObjectKey, "error", err.Error())
		}
	}

	updated, err := GetDocumentByID(ctx, s.db, doc.ID)
	if err != nil {
		return nil, fmt.Errorf("reload document: %w", err)
	}

	// 重投索引任务：ingestion 先删旧向量再写新向量，向量与 BM25 均随之刷新
	if s.taskDispatcher != nil {
		s.taskDispatcher(doc.ID)
	}

	s.audit.RecordEvent(ctx, audit.AuditEvent{
		EventType:    "document_updated",
		ActorUserID:  sql.NullInt64{Int64: actorUserID, Valid: actorUserID > 0},
		ActorRole:    sql.NullString{String: actorRole, Valid: actorRole != ""},
		RequestID:    common.RequestIDFromContext(ctx),
		ResourceType: sql.NullString{String: "document", Valid: true},
		ResourceID:   sql.NullString{String: strconv.FormatInt(doc.ID, 10), Valid: true},
		Action:       sql.NullString{String: "update", Valid: true},
		Status:       sql.NullString{String: "success", Valid: true},
		Metadata:     marshalJSON(map[string]any{"file_name": fileName, "size": fileSize}),
	})
	return updated, nil
}

// ListDocuments 列出文档，可按知识库过滤。knowledgeBaseID <= 0 表示不过滤。
//
// 参数：
//   - ctx: 请求上下文。
//   - knowledgeBaseID: 知识库ID，<=0 表示列出全部。
//
// 返回：
//   - []*Document: 文档列表（按 created_at 降序）。
//   - error: 查询失败。
func (s *KnowledgeService) ListDocuments(ctx context.Context, knowledgeBaseID int64) ([]*Document, error) {
	return ListDocumentsByKB(ctx, s.db, knowledgeBaseID)
}

// GetDocumentStream 按 uuid 查文档并打开其内容流，供在线预览、下载或编辑加载。
// 内容入库（storage_backend=db）的文档直接返回 DB 中的 Markdown 内容；
// 历史文件型文档仍从文件存储打开。
//
// 参数：
//   - ctx: 请求上下文。
//   - documentID: 文档对外标识 uuid。
//
// 返回：
//   - *Document: 文档记录。
//   - io.ReadCloser: 内容流（调用方负责关闭）。
//   - error: 文档不存在或打开文件失败。
func (s *KnowledgeService) GetDocumentStream(ctx context.Context, documentID string) (*Document, io.ReadCloser, error) {
	doc, err := GetDocumentByUUID(ctx, s.db, documentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, common.NotFound("文档不存在")
		}
		return nil, nil, fmt.Errorf("query document: %w", err)
	}
	if doc.StorageBackend == StorageBackendDB {
		return doc, io.NopCloser(strings.NewReader(doc.Content)), nil
	}
	stream, err := s.storage.Open(ctx, doc.ObjectKey)
	if err != nil {
		return nil, nil, fmt.Errorf("open file: %w", err)
	}
	return doc, stream, nil
}

// DeleteDocument 删除文档及其向量数据与存储文件。
//
// 顺序：先清理向量（便于后续失败时按 uuid 重试）→ 删存储文件 → 删 DB 记录 → 写审计。
//
// 参数：
//   - ctx: 请求上下文。
//   - documentID: 文档对外标识 uuid（与向量元数据 document_id 一致）。
//   - actorUserID: 操作者用户ID（审计用）。
//   - actorRole: 操作者角色（审计用）。
//
// 返回：
//   - error: 文档不存在或任一删除步骤失败。
func (s *KnowledgeService) DeleteDocument(ctx context.Context, documentID string, actorUserID int64, actorRole string) error {
	doc, err := GetDocumentByUUID(ctx, s.db, documentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return common.NotFound("文档不存在")
		}
		return fmt.Errorf("query document: %w", err)
	}
	docPK := doc.ID
	docName := doc.FileName

	// 先清理向量：后续步骤失败时 DB 记录仍在，可按 uuid 重试删除
	if err := s.vectorStore.DeleteByDocumentID(ctx, doc.UUID); err != nil {
		return fmt.Errorf("delete vectors: %w", err)
	}
	// 再删存储文件（内容入库的文档无文件可删）
	if doc.StorageBackend != StorageBackendDB {
		if err := s.storage.Delete(ctx, doc.ObjectKey); err != nil {
			return fmt.Errorf("delete file: %w", err)
		}
	}
	// 最后删 DB 记录
	if _, err := s.db.ExecContext(ctx, "DELETE FROM documents WHERE id = ?", docPK); err != nil {
		return fmt.Errorf("delete document: %w", err)
	}

	// 审计
	s.audit.RecordEvent(ctx, audit.AuditEvent{
		EventType:    "document_deleted",
		ActorUserID:  sql.NullInt64{Int64: actorUserID, Valid: actorUserID > 0},
		ActorRole:    sql.NullString{String: actorRole, Valid: actorRole != ""},
		RequestID:    common.RequestIDFromContext(ctx),
		ResourceType: sql.NullString{String: "document", Valid: true},
		ResourceID:   sql.NullString{String: strconv.FormatInt(docPK, 10), Valid: true},
		Action:       sql.NullString{String: "delete", Valid: true},
		Status:       sql.NullString{String: "success", Valid: true},
		Metadata:     marshalJSON(map[string]any{"file_name": docName}),
	})
	return nil
}

// marshalJSON 序列化为 json.RawMessage，失败返回 nil（写入 NULL）。
func marshalJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}
