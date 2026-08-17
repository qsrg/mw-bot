// File schemas.go: 知识库相关请求/响应 schema。
package knowledge

import "time"

// DocumentResponse 文档上传/查询响应。
// id 为文档 uuid（对外标识），与 Python DocumentResponse.id 字段一致。
type DocumentResponse struct {
	ID             string    `json:"id"`             // 文档 uuid（对外标识）
	FileName       string    `json:"file_name"`      // 文件名
	ContentType    string    `json:"content_type"`   // 内容类型
	FileSize       int64     `json:"file_size"`      // 文件大小(字节)
	StorageBackend string    `json:"storage_backend"` // 存储后端：db（内容入库，可在线编辑）/local/minio
	IndexStatus    string    `json:"index_status"`   // 索引状态：pending/indexing/indexed/failed
	CreatedAt      time.Time `json:"created_at"`     // 创建时间
}

// CreateDocumentRequest 在线新建文档请求（JSON）。
type CreateDocumentRequest struct {
	FileName        string `json:"file_name"`        // 文件名（缺 .md 扩展名时自动补全）
	Content         string `json:"content"`          // Markdown 全文
	KnowledgeBaseID int64  `json:"knowledge_base_id"` // 知识库ID，<=0 时默认 1
}

// UpdateDocumentRequest 在线更新文档请求（JSON）。
type UpdateDocumentRequest struct {
	FileName string `json:"file_name"` // 新文件名，空串表示保持原文件名
	Content  string `json:"content"`   // 新的 Markdown 全文
}
