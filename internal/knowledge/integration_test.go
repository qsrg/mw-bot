// File integration_test.go: 走真实 MySQL 的集成测试，覆盖文档列表/在线新建/更新/
// 内容读取/上传/删除全链路（不含 embedding，向量库用内存实现）。
//
// 默认跳过；设置 KNOWLEDGE_IT_DATABASE_URL 后启用，例如：
//
//	KNOWLEDGE_IT_DATABASE_URL=mysql://root:pass@127.0.0.1:3306/ai_qa go test ./internal/knowledge/ -run IT -v
//
// 前置条件：db/migrations/001~004 已在该库执行。测试数据用唯一前缀标记，
// 结束时清理（t.Cleanup 兜底 + 开头的遗留清理，防止上次中断残留）。
package knowledge

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"mw-bot/internal/audit"
	"mw-bot/internal/auth"
	"mw-bot/internal/common"
)

// itPrefix 集成测试数据唯一前缀。
const itPrefix = "__it_knowledge__"

// newITEnv 建立测试环境：真实 DB + 内存向量库 + 临时目录本地存储。
// 返回 nil 表示未设置环境变量，调用方应 t.Skip。
func newITEnv(t *testing.T) (*Handler, *sql.DB, *[]int64) {
	t.Helper()
	databaseURL := os.Getenv("KNOWLEDGE_IT_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未设置 KNOWLEDGE_IT_DATABASE_URL，跳过集成测试")
	}
	db, err := common.NewDB(common.Settings{DatabaseURL: databaseURL})
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	storage, err := common.NewLocalFileStorage(t.TempDir())
	if err != nil {
		t.Fatalf("创建本地存储失败: %v", err)
	}

	var dispatched []int64
	h := NewHandler(db, storage, common.NewInMemoryVectorStore(),
		audit.NewAuditService(db), nil, func(id int64) { dispatched = append(dispatched, id) })

	// 清理上次运行可能的残留，并在本次结束时删除全部测试文档
	cleanupITRows := func() {
		_, _ = db.Exec("DELETE FROM documents WHERE file_name LIKE ?", itPrefix+"%")
	}
	cleanupITRows()
	t.Cleanup(cleanupITRows)
	return h, db, &dispatched
}

// itRequest 构造带 admin 身份的 JSON 请求。
func itRequest(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	identity := &auth.IdentityContext{
		UserID:      1,
		Username:    "it-tester",
		Role:        "admin",
		Permissions: common.PermissionsForRole("admin"),
	}
	return r.WithContext(auth.WithIdentity(r.Context(), identity))
}

// decodeDocumentResponse 解析 DocumentResponse，断言 HTTP 200。
func decodeDocumentResponse(t *testing.T, rec *httptest.ResponseRecorder) DocumentResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body.String())
	}
	var resp DocumentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v (%s)", err, rec.Body.String())
	}
	return resp
}

// TestITDocumentLifecycle 列表 -> 在线新建 -> 读取 -> 更新 -> 列表 -> 删除 全链路。
// 覆盖文档管理页面打开时调用的列表接口（曾因 SELECT 列与 Scan 目标数不一致报 500）。
func TestITDocumentLifecycle(t *testing.T) {
	h, db, dispatched := newITEnv(t)
	ctx := context.Background()

	// 1. 列表：页面打开路径，必须不报错
	if _, err := h.newService().ListDocuments(ctx, 0); err != nil {
		t.Fatalf("列表文档失败: %v", err)
	}

	// 2. 在线新建（JSON）
	name := itPrefix + "生命周期测试"
	rec := httptest.NewRecorder()
	h.createDocument(rec, itRequest(t, "POST", "/api/knowledge/documents",
		fmt.Sprintf(`{"file_name":%q,"content":"# 标题\n\nRocketMQ NameServer 测试内容","knowledge_base_id":1}`, name)))
	created := decodeDocumentResponse(t, rec)
	if created.FileName != name+".md" {
		t.Errorf("文件名应为自动补 .md: %q", created.FileName)
	}
	if created.StorageBackend != "db" || created.IndexStatus != "pending" {
		t.Errorf("新文档应为 db 后端 + pending，实际 %s/%s", created.StorageBackend, created.IndexStatus)
	}
	if len(*dispatched) != 1 {
		t.Errorf("应投递 1 次索引任务，实际 %d", len(*dispatched))
	}

	// DB 侧核对：content 已入库，无文件存储字段
	var content, backend, objectKey string
	if err := db.QueryRow("SELECT content, storage_backend, object_key FROM documents WHERE uuid = ?",
		created.ID).Scan(&content, &backend, &objectKey); err != nil {
		t.Fatalf("查询新建文档失败: %v", err)
	}
	if !strings.Contains(content, "NameServer") || backend != "db" || objectKey != "" {
		t.Errorf("入库字段不符: backend=%s object_key=%q content_len=%d", backend, objectKey, len(content))
	}

	// 3. 内容读取（预览/编辑加载路径）
	rec = httptest.NewRecorder()
	h.getDocumentContent(rec, itRequest(t, "GET", "/api/knowledge/documents/x/content", ""), created.ID)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "NameServer") {
		t.Fatalf("读取内容失败: HTTP %d, body=%q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/markdown") {
		t.Errorf("Content-Type 应为 text/markdown，实际 %q", ct)
	}

	// 4. 在线更新：内容变化 + 状态重置 pending + 再次投递
	rec = httptest.NewRecorder()
	h.updateDocument(rec, itRequest(t, "PUT", "/api/knowledge/documents/x",
		fmt.Sprintf(`{"file_name":%q,"content":"# 更新后\n\n消费者堆积排查"}`, name+"-v2")),
		created.ID)
	updated := decodeDocumentResponse(t, rec)
	if updated.FileName != name+"-v2.md" || updated.IndexStatus != "pending" {
		t.Errorf("更新后应为新文件名 + pending，实际 %s/%s", updated.FileName, updated.IndexStatus)
	}
	if len(*dispatched) != 2 {
		t.Errorf("更新后应再投递 1 次（共 2 次），实际 %d", len(*dispatched))
	}
	if err := db.QueryRow("SELECT content FROM documents WHERE uuid = ?", created.ID).Scan(&content); err != nil {
		t.Fatalf("查询更新后文档失败: %v", err)
	}
	if !strings.Contains(content, "消费者堆积") {
		t.Error("更新后 DB 内容应包含新内容")
	}

	// 5. 上传（multipart）：Markdown 成功、PDF 拒绝
	markup := func(t *testing.T, fileName, contentType string, payload string) *http.Request {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("knowledge_base_id", "1")
		fw, _ := mw.CreateFormFile("file", fileName)
		_, _ = fw.Write([]byte(payload))
		_ = mw.Close()
		r := httptest.NewRequest("POST", "/api/knowledge/documents", &buf)
		r.Header.Set("Content-Type", mw.FormDataContentType())
		identity := &auth.IdentityContext{UserID: 1, Role: "admin", Permissions: common.PermissionsForRole("admin")}
		return r.WithContext(auth.WithIdentity(r.Context(), identity))
	}
	rec = httptest.NewRecorder()
	h.uploadDocument(rec, markup(t, itPrefix+"上传.md", "text/markdown", "上传内容"))
	up := decodeDocumentResponse(t, rec)
	if up.StorageBackend != "db" {
		t.Errorf("上传入库应为 db 后端，实际 %s", up.StorageBackend)
	}
	rec = httptest.NewRecorder()
	h.uploadDocument(rec, markup(t, itPrefix+"报告.pdf", "application/pdf", "%PDF-1.4"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("PDF 上传应返回 400，实际 %d: %s", rec.Code, rec.Body.String())
	}

	// 6. 删除：记录消失
	svc := h.newService()
	if err := svc.DeleteDocument(ctx, created.ID, 1, "admin"); err != nil {
		t.Fatalf("删除文档失败: %v", err)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM documents WHERE uuid = ?", created.ID).Scan(&n); err != nil {
		t.Fatalf("查询删除后文档失败: %v", err)
	}
	if n != 0 {
		t.Error("删除后记录应不存在")
	}

	// 7. 列表再跑一次（含刚上传的 db 文档）
	docs, err := svc.ListDocuments(ctx, 0)
	if err != nil {
		t.Fatalf("列表文档失败: %v", err)
	}
	found := false
	for _, d := range docs {
		if d.UUID == up.ID {
			found = true
		}
	}
	if !found {
		t.Error("上传的文档应出现在列表中")
	}
}

// TestITUpdateLegacyFileDocument 历史 local 文件型 Markdown 更新后转为 db 后端。
func TestITUpdateLegacyFileDocument(t *testing.T) {
	h, db, _ := newITEnv(t)
	ctx := context.Background()

	// 直接插入一条模拟历史文件型记录（object_key 指向不存在的文件，更新路径不应打开它）
	uuid := "itkn-0000-4000-8000-0000000000000001"
	if _, err := db.Exec(`INSERT INTO documents
		(uuid, knowledge_base_id, file_name, content_type, file_size, file_hash, storage_backend, object_key, index_status, uploaded_by)
		VALUES (?, 1, ?, 'text/markdown', 1, 'legacyhash', 'local', ?, 'indexed', 1)`,
		uuid, itPrefix+"遗留.md", "documents/nonexistent/file.md"); err != nil {
		t.Fatalf("插入遗留文档失败: %v", err)
	}

	svc := h.newService()
	doc, err := svc.UpdateDocumentContent(ctx, uuid, "", "# 迁移后内容", 1, "admin")
	if err != nil {
		t.Fatalf("更新遗留文档失败: %v", err)
	}
	if doc.StorageBackend != "db" || doc.IndexStatus != "pending" {
		t.Errorf("更新后应转为 db + pending，实际 %s/%s", doc.StorageBackend, doc.IndexStatus)
	}
	var backend string
	if err := db.QueryRow("SELECT storage_backend FROM documents WHERE uuid = ?", uuid).Scan(&backend); err != nil {
		t.Fatalf("查询后端失败: %v", err)
	}
	if backend != "db" {
		t.Errorf("DB 中后端应为 db，实际 %s", backend)
	}

	if err := svc.DeleteDocument(ctx, uuid, 1, "admin"); err != nil {
		t.Fatalf("删除遗留文档失败: %v", err)
	}
}

// TestITListExcludesContent 断言列表查询结果不含 content 列（文档数为 0 时也走通流程）。
func TestITSingleDocQueriesIncludeContent(t *testing.T) {
	_, db, _ := newITEnv(t)
	ctx := context.Background()

	uuid := "itkn-0000-4000-8000-0000000000000002"
	if _, err := db.Exec(`INSERT INTO documents
		(uuid, knowledge_base_id, file_name, content_type, file_size, file_hash, storage_backend, object_key, content, index_status, uploaded_by)
		VALUES (?, 1, ?, 'text/markdown', 4, 'h2', 'db', '', '正文', 'indexed', 1)`,
		uuid, itPrefix+"单查.md"); err != nil {
		t.Fatalf("插入文档失败: %v", err)
	}
	doc, err := GetDocumentByUUID(ctx, db, uuid)
	if err != nil {
		t.Fatalf("按 uuid 查询失败: %v", err)
	}
	if doc.Content != "正文" {
		t.Errorf("单条查询应返回内容，实际 %q", doc.Content)
	}
	if doc.IndexStatus != "indexed" {
		t.Errorf("index_status 应为 indexed，实际 %q（列清单错位会在这里暴露）", doc.IndexStatus)
	}
	byID, err := GetDocumentByID(ctx, db, doc.ID)
	if err != nil || byID.Content != "正文" || byID.IndexStatus != "indexed" {
		t.Errorf("按 id 查询不符: doc=%+v err=%v", byID, err)
	}
}
