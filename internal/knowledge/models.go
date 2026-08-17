// Package knowledge 实现知识库文档的上传、查询、删除与下载，对齐 Python app/knowledge 模块。
//
// Document 实体对应 documents 表；KnowledgeService 封装文件保存、去重、DB 写入、
// 索引任务投递与审计；HTTP Handler 暴露 /api/knowledge/documents 系列 REST 端点。
package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Document 知识库文档实体，对应数据库 documents 表。
// 字段映射与 db/migrations/001_init_schema.sql 及 004 迁移中 documents 列定义对齐。
type Document struct {
	ID              int64     `json:"id"`                // 主键ID（INT AUTO_INCREMENT）
	UUID            string    `json:"uuid"`              // 对外标识（CHAR(36)），用于 object_key 与向量元数据 document_id
	KnowledgeBaseID int64     `json:"knowledge_base_id"` // 所属知识库ID(knowledge_bases.id)
	FileName        string    `json:"file_name"`         // 文件名
	ContentType     string    `json:"content_type"`      // 内容类型
	FileSize        int64     `json:"file_size"`         // 文件大小(字节)
	FileHash        string    `json:"file_hash"`         // 文件 SHA256 哈希
	StorageBackend  string    `json:"storage_backend"`   // 存储后端：db（内容入库）/local/minio
	Bucket          string    `json:"bucket"`            // 存储桶（local/db 为空）
	ObjectKey       string    `json:"object_key"`        // 对象键（db 为空）
	Content         string    `json:"-"`                 // Markdown 文档内容（storage_backend=db 时有值）
	IndexStatus     string    `json:"index_status"`      // 索引状态：pending/indexing/indexed/failed
	IndexError      string    `json:"index_error"`       // 索引错误（可空）
	UploadedBy      int64     `json:"uploaded_by"`       // 上传人(users.id)
	CreatedAt       time.Time `json:"created_at"`        // 创建时间
	UpdatedAt       time.Time `json:"updated_at"`        // 更新时间
}

// StorageBackendDB 内容入库的存储后端标识。
const StorageBackendDB = "db"

// docColumnsBase 单条/列表文档查询共用的列模板，%s 为 content 列占位：
// 单条查询传 COALESCE(content,'')，列表查询传 ''（不把全库文档内容拉进内存）。
const docColumnsBase = `id, uuid, knowledge_base_id, file_name, content_type, file_size,
	file_hash, storage_backend, COALESCE(bucket, ''), object_key, index_status, %s,
	COALESCE(index_error, ''), uploaded_by, created_at, updated_at`

// docColumnsWithContent 单条查询列：包含文档内容。
var docColumnsWithContent = fmt.Sprintf(docColumnsBase, "COALESCE(content, '')")

// docColumnsWithoutContent 列表查询列：不含文档内容。
var docColumnsWithoutContent = fmt.Sprintf(docColumnsBase, "''")

// SHA256Bytes 计算字节的 SHA256 哈希，返回十六进制字符串。
// 与 Python hashlib.sha256(data).hexdigest() 输出一致。
func SHA256Bytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// GetDocumentByUUID 按 uuid 查询文档（含内容）。未找到返回 sql.ErrNoRows。
//
// 参数：
//   - ctx: 请求上下文。
//   - db: 数据库连接池。
//   - uuid: 文档对外标识。
//
// 返回：
//   - *Document: 文档实例。
//   - error: 查询失败或未找到。
func GetDocumentByUUID(ctx context.Context, db *sql.DB, uuid string) (*Document, error) {
	query := `SELECT ` + docColumnsWithContent + ` FROM documents WHERE uuid = ?`
	row := db.QueryRowContext(ctx, query, uuid)
	return scanDocument(row)
}

// GetDocumentByID 按自增主键 ID 查询文档（含内容）。未找到返回 sql.ErrNoRows。
func GetDocumentByID(ctx context.Context, db *sql.DB, id int64) (*Document, error) {
	query := `SELECT ` + docColumnsWithContent + ` FROM documents WHERE id = ?`
	row := db.QueryRowContext(ctx, query, id)
	return scanDocument(row)
}

// GetDocumentByHashAndKB 按知识库ID + 文件哈希查询文档，用于上传去重。
// 未找到返回 (nil, nil)。
func GetDocumentByHashAndKB(ctx context.Context, db *sql.DB, knowledgeBaseID int64, fileHash string) (*Document, error) {
	query := `SELECT ` + docColumnsWithoutContent +
		` FROM documents WHERE knowledge_base_id = ? AND file_hash = ? LIMIT 1`
	row := db.QueryRowContext(ctx, query, knowledgeBaseID, fileHash)
	doc, err := scanDocument(row)
	if err != nil {
		if errIsNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return doc, nil
}

// ListDocumentsByKB 列出文档，可按知识库过滤。knowledgeBaseID <= 0 表示不过滤。
// 按 created_at 降序排列，与 Python order_by(Document.created_at.desc()) 一致。
// 不查询 content 列，避免列表接口把全库文档内容拉进内存。
func ListDocumentsByKB(ctx context.Context, db *sql.DB, knowledgeBaseID int64) ([]*Document, error) {
	query := `SELECT ` + docColumnsWithoutContent + ` FROM documents`
	var rows *sql.Rows
	var err error
	if knowledgeBaseID > 0 {
		query += ` WHERE knowledge_base_id = ? ORDER BY created_at DESC`
		rows, err = db.QueryContext(ctx, query, knowledgeBaseID)
	} else {
		query += ` ORDER BY created_at DESC`
		rows, err = db.QueryContext(ctx, query)
	}
	if err != nil {
		return nil, fmt.Errorf("query documents: %w", err)
	}
	defer rows.Close()

	docs := make([]*Document, 0)
	for rows.Next() {
		doc, err := scanDocument(rows)
		if err != nil {
			return nil, fmt.Errorf("scan document: %w", err)
		}
		docs = append(docs, doc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate documents: %w", err)
	}
	return docs, nil
}

// rowScanner 抽象 *sql.Row 与 *sql.Rows 的 Scan 接口，便于复用 scanDocument。
type rowScanner interface {
	Scan(dest ...any) error
}

// scanDocument 将一行扫描到 *Document。未找到返回包装了 sql.ErrNoRows 的错误。
// 目标顺序必须与 docColumnsBase 的列顺序一致：... object_key, index_status, content, index_error ...
func scanDocument(row rowScanner) (*Document, error) {
	d := &Document{}
	err := row.Scan(&d.ID, &d.UUID, &d.KnowledgeBaseID, &d.FileName, &d.ContentType, &d.FileSize,
		&d.FileHash, &d.StorageBackend, &d.Bucket, &d.ObjectKey, &d.IndexStatus, &d.Content, &d.IndexError,
		&d.UploadedBy, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return d, nil
}

// IsMarkdownDocument 判断文档是否为 Markdown（按扩展名或内容类型），在线编辑仅支持 Markdown。
func (d *Document) IsMarkdownDocument() bool {
	lower := strings.ToLower(d.FileName)
	return strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".markdown") ||
		d.ContentType == "text/markdown"
}

// errIsNoRows 判断错误是否为 sql.ErrNoRows（用 errors.Is 兼容 wrapped 错误）。
func errIsNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
