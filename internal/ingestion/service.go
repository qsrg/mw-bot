// File service.go: 异步索引服务（goroutine 池 + MySQL 持久化），
// 对齐 Python app/ingestion/tasks.py。
//
// 文档解析、分块、embedding 与向量写入在后台 goroutine 异步执行，不阻塞 HTTP 上传请求。
//
// 持久化策略：
//   - 任务状态即 documents.index_status，MySQL 为持久真相源，goroutine 池仅作执行缓冲。
//   - 状态机：pending（已入队）→ indexing（goroutine 开始处理）→ indexed/failed。
//   - 进程重启时 RecoverPendingIndexing 将 indexing 重置为 pending，重新投递所有 pending 文档。
//   - 重投/重试依赖幂等：写入前先 DeleteByDocumentID 清理旧向量，避免重复 chunk。
package ingestion

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
	"time"

	"mw-bot/internal/audit"
	"mw-bot/internal/common"
	"mw-bot/internal/knowledge"
)

// MaxRetries 失败后最多重试次数。
const MaxRetries = 3

// RetryDelay 重试间隔。
const RetryDelay = 30 * time.Second

// workerCount 后台索引工作 goroutine 数量。
const workerCount = 2

// submitBufferSize 投递通道缓冲大小。
const submitBufferSize = 100

// IngestionService 索引任务管理服务。
// 持有 db/storage/vectorStore/embedder/audit 依赖，提供后台索引与启动恢复能力。
type IngestionService struct {
	db          *sql.DB
	storage     common.FileStorage
	vectorStore common.VectorStore
	embedder    common.EmbeddingProvider
	audit       *audit.AuditService
	submitCh    chan int64 // 任务投递通道
	workerSem   chan struct{}
}

// NewIngestionService 创建索引服务实例。
// embedder 可为 nil（测试或本地无网关场景），此时跳过 embedding 与向量写入。
//
// 参数：
//   - db: 已就绪的 MySQL 连接池。
//   - storage: 文件存储实现。
//   - vectorStore: 向量库实例。
//   - embedder: Embedding 提供者，可为 nil。
//   - auditSvc: 审计服务。
func NewIngestionService(
	db *sql.DB,
	storage common.FileStorage,
	vectorStore common.VectorStore,
	embedder common.EmbeddingProvider,
	auditSvc *audit.AuditService,
) *IngestionService {
	return &IngestionService{
		db:          db,
		storage:     storage,
		vectorStore: vectorStore,
		embedder:    embedder,
		audit:       auditSvc,
		submitCh:    make(chan int64, submitBufferSize),
		workerSem:   make(chan struct{}, workerCount),
	}
}

// Start 启动后台 worker goroutine，监听 submitCh 处理索引任务。
// 应在 main 启动时调用一次；ctx 取消时停止接收新任务。
func (s *IngestionService) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case docID := <-s.submitCh:
				// 用信号量限制并发 goroutine 数
				select {
				case s.workerSem <- struct{}{}:
					go func(id int64) {
						defer func() { <-s.workerSem }()
						defer func() {
							if r := recover(); r != nil {
								slog.Error("索引任务 panic", "document_id", id, "panic", r)
							}
						}()
						s.indexDocument(ctx, id)
					}(docID)
				case <-ctx.Done():
					return
				}
			}
		}
	}()
}

// SubmitIndexDocument 投递索引任务到后台 goroutine 池，立即返回不阻塞。
// 不丢弃任务：投递阻塞由独立 goroutine 承担，与 Python ThreadPoolExecutor 无界队列语义一致。
// 持久化真相源是 DB（文档上传时已置 pending），即使进程退出，下次启动 RecoverPendingIndexing 也会重投。
func (s *IngestionService) SubmitIndexDocument(documentID int64) {
	go func() {
		s.submitCh <- documentID
	}()
}

// RecoverPendingIndexing 进程启动时恢复未完成索引任务。
// 将所有 indexing 状态（上次进程被打断）重置为 pending，再重新投递全部 pending 文档。
//
// 返回重新投递的文档数量。
func (s *IngestionService) RecoverPendingIndexing(ctx context.Context) int {
	// 重置上次中断的 indexing 任务为 pending
	if _, err := s.db.ExecContext(ctx,
		"UPDATE documents SET index_status = 'pending' WHERE index_status = 'indexing'"); err != nil {
		slog.Error("reset indexing to pending failed", "error", err)
		return 0
	}
	// 收集所有 pending 文档
	rows, err := s.db.QueryContext(ctx,
		"SELECT id FROM documents WHERE index_status = 'pending'")
	if err != nil {
		slog.Error("query pending documents failed", "error", err)
		return 0
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		slog.Error("iterate pending documents failed", "error", err)
		return 0
	}
	for _, id := range ids {
		s.SubmitIndexDocument(id)
	}
	slog.Info("启动恢复：重新投递待索引文档", "count", len(ids))
	return len(ids)
}

// indexDocument 索引文档：解析→分块→embedding→向量写入→状态更新。
// 开始处理时置 indexing，成功置 indexed，最终失败置 failed。
// 失败按 MaxRetries 重试；写入前先删除旧向量保证重投幂等。
func (s *IngestionService) indexDocument(ctx context.Context, documentID int64) {
	// 阶段一：置 indexing（独立 UPDATE，崩溃后启动恢复可识别）
	s.setStatus(ctx, documentID, "indexing", "")

	var lastErr error
	for attempt := 0; attempt <= MaxRetries; attempt++ {
		err := s.tryIndex(ctx, documentID)
		if err == nil {
			return
		}
		lastErr = err
		slog.Warn("索引失败",
			"document_id", documentID,
			"attempt", attempt+1,
			"max_attempts", MaxRetries+1,
			"error", err)
		if attempt < MaxRetries {
			select {
			case <-ctx.Done():
				return
			case <-time.After(RetryDelay):
			}
		}
	}
	// 重试耗尽：标记 failed
	s.setStatus(ctx, documentID, "failed", lastErr.Error())
	slog.Error("索引最终失败", "document_id", documentID, "error", lastErr)
}

// tryIndex 单次尝试索引。返回 nil 表示成功（状态已置 indexed）。
func (s *IngestionService) tryIndex(ctx context.Context, documentID int64) error {
	doc, err := knowledge.GetDocumentByID(ctx, s.db, documentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// 文档不存在，视为已完成（与 Python not_found 行为一致）
			return nil
		}
		return fmt.Errorf("query document: %w", err)
	}

	// 读取文档内容：内容入库（storage_backend=db）的文档直接用 DB 中的 Markdown，
	// 历史文件型文档（local/minio）仍从文件存储读取。
	var reader io.Reader
	if doc.StorageBackend == knowledge.StorageBackendDB {
		reader = strings.NewReader(doc.Content)
	} else {
		stream, err := s.storage.Open(ctx, doc.ObjectKey)
		if err != nil {
			return fmt.Errorf("open file: %w", err)
		}
		defer stream.Close() // 解析 panic 时也保证文件句柄释放（M19）
		reader = stream
	}
	parsed, err := ParseDocument(doc.FileName, doc.ContentType, reader)
	if err != nil {
		return fmt.Errorf("parse document: %w", err)
	}

	// 从文件名+全文识别文档涉及的中间件
	docMiddlewares := common.DetectMiddlewares(doc.FileName + " " + parsed.Text)

	// 分块
	chunks := ChunkText(parsed, DefaultChunkSize, DefaultOverlap, doc.UUID, doc.KnowledgeBaseID, docMiddlewares, nil)

	// embedding 与向量写入：embedder 可用时清理旧向量并写入新向量；任意失败降级为仅解析分块
	// （仍标记 indexed），对齐 Python tasks.py 的 try/except 容错（H9）。空 chunks 也清理旧向量（M17）。
	if s.embedder != nil {
		if err := s.writeVectors(ctx, doc, chunks); err != nil {
			slog.Warn("embedding/向量写入跳过", "document_id", documentID, "error", err)
		}
	}

	// 状态更新：indexed
	if _, err := s.db.ExecContext(ctx,
		"UPDATE documents SET index_status = 'indexed', index_error = NULL WHERE id = ?",
		documentID); err != nil {
		return fmt.Errorf("update status to indexed: %w", err)
	}

	// 索引完成审计
	s.audit.RecordEvent(ctx, audit.AuditEvent{
		EventType:    "document_indexed",
		ActorUserID:  sql.NullInt64{Int64: doc.UploadedBy, Valid: doc.UploadedBy > 0},
		RequestID:    common.RequestIDFromContext(ctx),
		ResourceType: sql.NullString{String: "document", Valid: true},
		ResourceID:   sql.NullString{String: strconv.FormatInt(doc.ID, 10), Valid: true},
		Action:       sql.NullString{String: "index", Valid: true},
		Status:       sql.NullString{String: "success", Valid: true},
		Metadata:     marshalJSON(map[string]any{"chunks": len(chunks)}),
	})
	return nil
}

// setStatus 更新文档索引状态，独立提交，避免与主流程事务耦合。
// errMsg 非空时截断到 1000 字符写入 index_error。
func (s *IngestionService) setStatus(ctx context.Context, documentID int64, status, errMsg string) {
	var errVal any
	if errMsg != "" {
		// 按 Unicode 字符截断到 1000，避免按字节切断多字节字符产生无效 UTF-8（M18），
		// 对齐 Python str(error)[:1000]。
		if r := []rune(errMsg); len(r) > 1000 {
			errMsg = string(r[:1000])
		}
		errVal = errMsg
	}
	if _, err := s.db.ExecContext(ctx,
		"UPDATE documents SET index_status = ?, index_error = ? WHERE id = ?",
		status, errVal, documentID); err != nil {
		slog.Error("set status failed", "document_id", documentID, "status", status, "error", err)
	}
}

// writeVectors 写入文档向量：先 embedding，成功后删除旧向量再写入新向量。
// 顺序与 Python tasks.py 一致（embed -> delete -> upsert），embedding 失败时保留旧向量。
// 空 chunks 仅清理旧向量（避免旧版本残留）。任意步骤失败返回 error，由调用方降级处理。
func (s *IngestionService) writeVectors(ctx context.Context, doc *knowledge.Document, chunks []DocumentChunk) error {
	if len(chunks) == 0 {
		// 空 chunks：仅清理旧向量（M17）
		if err := s.vectorStore.DeleteByDocumentID(ctx, doc.UUID); err != nil {
			return fmt.Errorf("delete old vectors: %w", err)
		}
		return nil
	}
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
	}
	embeddings, err := s.embedder.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("embed: %w", err)
	}
	// 幂等：embedding 成功后删除旧向量
	if err := s.vectorStore.DeleteByDocumentID(ctx, doc.UUID); err != nil {
		return fmt.Errorf("delete old vectors: %w", err)
	}
	docs := make([]common.Document, len(chunks))
	for i, c := range chunks {
		chunkID := fmt.Sprintf("%s_%d", doc.UUID, c.ChunkIndex)
		docs[i] = common.Document{
			ID:        chunkID,
			Text:      c.Text,
			Embedding: embeddings[i],
			Metadata:  toMetadata(c.Metadata),
		}
	}
	if err := s.vectorStore.Add(ctx, docs); err != nil {
		return fmt.Errorf("add vectors: %w", err)
	}
	return nil
}

// toMetadata 将 chunk.Metadata（map[string]any）转换为向量库需要的 map[string]string。
// 非字符串值（如 bool true、int）转为字符串；mw_<name>=true 转为 "true"。
func toMetadata(meta map[string]any) map[string]string {
	out := make(map[string]string, len(meta))
	for k, v := range meta {
		switch sv := v.(type) {
		case string:
			out[k] = sv
		case bool:
			if sv {
				out[k] = "true"
			} else {
				out[k] = "false"
			}
		case int:
			out[k] = strconv.Itoa(sv)
		case int64:
			out[k] = strconv.FormatInt(sv, 10)
		default:
			out[k] = fmt.Sprintf("%v", v)
		}
	}
	return out
}

// marshalJSON 序列化为 json.RawMessage，失败返回 nil（写入 NULL）。
func marshalJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}
