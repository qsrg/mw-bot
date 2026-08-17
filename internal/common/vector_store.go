// File vector_store.go: 向量库接口与 chromem-go 实现，对齐 Python app/common/vector_store.py。
//
// 通过 VectorStore 接口隔离具体向量数据库，业务模块只依赖该接口，
// 不允许直接依赖 chromem-go SDK。MVP 提供 ChromemVectorStore 与 InMemoryVectorStore。
package common

import (
	"context"
	"encoding/gob"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"

	"github.com/philippgille/chromem-go"
)

// SearchResult 检索结果。Score 为相似度或融合后的归一化分数。
type SearchResult struct {
	ID       string            // 文档/chunk 全局唯一 ID
	Text     string            // chunk 文本内容
	Score    float64          // 相似度或融合分数（与查询相关性，越高越相关）
	Metadata map[string]string // 元数据（document_id/knowledge_base_id/chunk_index 等）
}

// Document 待写入向量库的文档。Embedding 必须与查询向量同维度。
type Document struct {
	ID        string            // 全局唯一 ID，建议 chunk_id 或 document_id_chunk_index
	Text      string            // 文本内容，用于展示与 BM25 分词
	Embedding []float32         // 文本向量，必须为已归一化或可被 chromem-go 归一化
	Metadata  map[string]string // 元数据，可用于过滤
}

// VectorStore 向量库接口，所有实现必须满足该接口。
type VectorStore interface {
	// Add 写入/更新一批文档及其向量。
	Add(ctx context.Context, docs []Document) error
	// SimilaritySearch 按向量相似度检索，filters 为元数据等值过滤。
	SimilaritySearch(ctx context.Context, queryVec []float32, filters map[string]string, limit int, scoreThreshold float64) ([]SearchResult, error)
	// HybridSearch 混合检索：dense(向量) + sparse(BM25) 经 RRF 融合。
	HybridSearch(ctx context.Context, query string, queryVec []float32, filters map[string]string, limit int, scoreThreshold float64) ([]SearchResult, error)
	// DeleteByDocumentID 按 document_id 删除所有相关 chunk。
	DeleteByDocumentID(ctx context.Context, documentID string) error
	// Warmup 启动预热，best-effort 不阻断。
	Warmup(ctx context.Context) error
	// GetAll 返回所有文档，用于 BM25 索引重建。
	GetAll(ctx context.Context) ([]SearchResult, error)
}

// noopEmbeddingFunc 是一个占位 EmbeddingFunc。
// 我们始终在写入时提供 Embedding、查询时提供 query 向量，
// 不会触发 chromem-go 内部 embed 调用；这里返回错误以防误用。
func noopEmbeddingFunc(_ context.Context, _ string) ([]float32, error) {
	return nil, fmt.Errorf("embedding function not configured; provide embeddings explicitly")
}

// ChromemVectorStore chromem-go 适配器。
//
// 使用 chromem.NewPersistentDB 进程内运行，向量数据落本地目录，
// 无需单独部署向量数据库。距离度量默认 cosine（chromem-go 内部归一化向量后做点积）。
//
// 应用层 BM25 索引：chromem-go 无原生混合检索，bm25 字段在应用层维护稀疏索引，
// Warmup 时从内存副本全量构建，Add/Delete 时增量同步，HybridSearch 时与 dense 融合。
//
// 注意：chromem-go v0.7.0 未暴露 List/GetAll API，进程重启后从持久化目录加载的
// 旧文档无法通过 chromem-go 枚举。为此维护一份内存副本 documents 用于 GetAll 与
// BM25 重建，并通过 docsPath 旁路文件持久化（gob）该副本，使重启后 BM25 仍能全量重建。
type ChromemVectorStore struct {
	db         *chromem.DB
	collection *chromem.Collection
	mu         sync.RWMutex
	documents  map[string]Document // 内存副本，用于 GetAll 与 BM25 重建
	docsPath   string              // 内存副本的旁路持久化路径（gob），重启后据此重建 BM25
	bm25       *BM25Index          // 应用层 BM25 索引，HybridSearch 时与 dense 融合
}

// NewChromemVectorStore 创建 chromem-go 适配器。
// persistPath 为持久化目录，不存在会自动创建；collectionName 为集合名。
// embedding 函数传 noop，所有写入与查询都显式提供向量。
// BM25 索引内部创建，Warmup 时从内存副本全量构建。
func NewChromemVectorStore(persistPath, collectionName string) (*ChromemVectorStore, error) {
	if persistPath == "" {
		return nil, fmt.Errorf("persist path is empty")
	}
	if collectionName == "" {
		return nil, fmt.Errorf("collection name is empty")
	}
	// compress=false：MVP 数据量小，避免压缩开销
	db, err := chromem.NewPersistentDB(persistPath, false)
	if err != nil {
		return nil, fmt.Errorf("create chromem db: %w", err)
	}
	// metadata=nil 与 embeddingFunc=noopEmbeddingFunc；GetOrCreateCollection 会复用已存在集合
	collection, err := db.GetOrCreateCollection(collectionName, nil, noopEmbeddingFunc)
	if err != nil {
		return nil, fmt.Errorf("create chromem collection: %w", err)
	}
	store := &ChromemVectorStore{
		db:         db,
		collection: collection,
		documents:  make(map[string]Document),
		docsPath:   filepath.Join(persistPath, "documents.bm25.gob"),
		bm25:       NewBM25Index(),
	}
	// 重启后从旁路文件恢复内存副本，使 BM25 预热能覆盖历史文档
	store.mu.Lock()
	store.loadDocumentsLocked()
	store.mu.Unlock()
	return store, nil
}

// persistedDoc 内存副本的持久化形式，仅保留 BM25 重建所需字段（不含 Embedding）。
type persistedDoc struct {
	ID       string
	Text     string
	Metadata map[string]string
}

// loadDocumentsLocked 从旁路文件加载内存副本，重启后据此重建 BM25 索引。
// 文件不存在或解析失败时静默降级（BM25 暂空，待新文档 Add 后增量补充）。
// 调用方需持有 s.mu 写锁。
func (s *ChromemVectorStore) loadDocumentsLocked() {
	if s.docsPath == "" {
		return
	}
	f, err := os.Open(s.docsPath)
	if err != nil {
		return // 首次启动无文件，正常
	}
	defer f.Close()
	var docs []persistedDoc
	if err := gob.NewDecoder(f).Decode(&docs); err != nil {
		return
	}
	for _, d := range docs {
		s.documents[d.ID] = Document{ID: d.ID, Text: d.Text, Metadata: d.Metadata}
	}
}

// saveDocumentsLocked 将内存副本持久化到旁路文件，保证重启后 BM25 可全量重建。
// 调用方需持有 s.mu 写锁。
func (s *ChromemVectorStore) saveDocumentsLocked() {
	if s.docsPath == "" {
		return
	}
	docs := make([]persistedDoc, 0, len(s.documents))
	for _, d := range s.documents {
		docs = append(docs, persistedDoc{ID: d.ID, Text: d.Text, Metadata: d.Metadata})
	}
	tmp := s.docsPath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return
	}
	if err := gob.NewEncoder(f).Encode(docs); err != nil {
		f.Close()
		os.Remove(tmp)
		return
	}
	f.Close()
	_ = os.Rename(tmp, s.docsPath)
}

// Add 写入文档到 chromem-go 集合并同步内存副本与 BM25 索引。
// 并发数取 runtime.NumCPU()，与 chromem-go 推荐用法一致。
func (s *ChromemVectorStore) Add(ctx context.Context, docs []Document) error {
	if len(docs) == 0 {
		return nil
	}
	cmDocs := make([]chromem.Document, 0, len(docs))
	for _, d := range docs {
		if d.ID == "" {
			return fmt.Errorf("document ID is empty")
		}
		if len(d.Embedding) == 0 {
			return fmt.Errorf("document %s has empty embedding", d.ID)
		}
		cmDocs = append(cmDocs, chromem.Document{
			ID:        d.ID,
			Content:   d.Text,
			Embedding: d.Embedding,
			Metadata:  d.Metadata,
		})
	}
	if err := s.collection.AddDocuments(ctx, cmDocs, runtime.NumCPU()); err != nil {
		return fmt.Errorf("add documents to chromem: %w", err)
	}
	// 同步内存副本与 BM25 索引
	s.mu.Lock()
	bm25Docs := make([]BM25Doc, 0, len(docs))
	for _, d := range docs {
		// 深拷贝 metadata，避免外部修改影响内部状态
		meta := make(map[string]string, len(d.Metadata))
		for k, v := range d.Metadata {
			meta[k] = v
		}
		s.documents[d.ID] = Document{
			ID:        d.ID,
			Text:      d.Text,
			Embedding: d.Embedding,
			Metadata:  meta,
		}
		bm25Docs = append(bm25Docs, BM25Doc{
			ID:       d.ID,
			Text:     d.Text,
			Metadata: meta,
		})
	}
	// BM25 增量同步与旁路持久化均在 s.mu 内完成，与 Warmup 的全量重建互斥，
	// 避免 Warmup 的 Reset 清掉并发 Add 写入的条目（C5 竞态）。
	s.bm25.AddDocuments(bm25Docs)
	s.saveDocumentsLocked()
	s.mu.Unlock()
	return nil
}

// SimilaritySearch 按向量相似度检索。
// chromem-go QueryEmbedding 要求 nResults <= 文档总数；超出时降级为集合实际数量。
func (s *ChromemVectorStore) SimilaritySearch(ctx context.Context, queryVec []float32, filters map[string]string, limit int, scoreThreshold float64) ([]SearchResult, error) {
	if limit <= 0 {
		return nil, nil
	}
	if len(queryVec) == 0 {
		return nil, fmt.Errorf("query vector is empty")
	}
	count := s.collection.Count()
	if count == 0 {
		return nil, nil
	}
	// chromem-go 要求 nResults <= 文档数，取 min 避免 error
	nResults := limit
	if nResults > count {
		nResults = count
	}
	results, err := s.collection.QueryEmbedding(ctx, queryVec, nResults, filters, nil)
	if err != nil {
		return nil, fmt.Errorf("query chromem: %w", err)
	}
	out := make([]SearchResult, 0, len(results))
	for _, r := range results {
		// chromem-go Similarity 已是 cosine 相似度（dot product of normalized vectors）
		score := float64(r.Similarity)
		if scoreThreshold > 0 && score < scoreThreshold {
			continue
		}
		out = append(out, SearchResult{
			ID:       r.ID,
			Text:     r.Content,
			Score:    score,
			Metadata: r.Metadata,
		})
	}
	// 结果已按相似度降序返回，无需重排
	return out, nil
}

// HybridSearch 委托给包级 HybridSearch 函数实现 dense + BM25 RRF 融合。
// BM25 索引由本实例持有，Warmup 时从内存副本全量构建，Add/Delete 时增量同步。
func (s *ChromemVectorStore) HybridSearch(ctx context.Context, query string, queryVec []float32, filters map[string]string, limit int, scoreThreshold float64) ([]SearchResult, error) {
	// chromem-go 无内置混合检索，应用层实现见 hybrid_search.go
	return HybridSearch(ctx, s, s.bm25, query, queryVec, filters, limit, scoreThreshold)
}

// DeleteByDocumentID 按 document_id 删除所有相关 chunk。
// chromem-go v0.7.0 的 Delete 支持 where 过滤。
func (s *ChromemVectorStore) DeleteByDocumentID(ctx context.Context, documentID string) error {
	if documentID == "" {
		return fmt.Errorf("document id is empty")
	}
	where := map[string]string{"document_id": documentID}
	if err := s.collection.Delete(ctx, where, nil); err != nil {
		return fmt.Errorf("delete from chromem: %w", err)
	}
	// 同步内存副本与 BM25 索引（均在 s.mu 内，与 Warmup 互斥）
	s.mu.Lock()
	for id, doc := range s.documents {
		if doc.Metadata["document_id"] == documentID {
			delete(s.documents, id)
		}
	}
	s.bm25.RemoveByDocumentID(documentID)
	s.saveDocumentsLocked()
	s.mu.Unlock()
	return nil
}

// Warmup 预热：从内存副本全量构建 BM25 索引。
// 内存副本在构造时已从旁路文件恢复（覆盖历史文档），故预热后 BM25 覆盖全量已知文档。
// 全程持 s.mu 写锁，与 Add/Delete 的 BM25 同步互斥，避免重建窗口期丢条目（C5 竞态）。
func (s *ChromemVectorStore) Warmup(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	docs := make([]BM25Doc, 0, len(s.documents))
	for _, doc := range s.documents {
		docs = append(docs, BM25Doc{
			ID:       doc.ID,
			Text:     doc.Text,
			Metadata: doc.Metadata,
		})
	}
	if len(docs) == 0 {
		return nil
	}
	// Reset 后全量重建，保证幂等（多次 Warmup 不会重复追加）
	s.bm25.Reset()
	s.bm25.AddDocuments(docs)
	return nil
}

// GetAll 返回内存副本中所有文档，用于 BM25 重建。
func (s *ChromemVectorStore) GetAll(ctx context.Context) ([]SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SearchResult, 0, len(s.documents))
	for _, doc := range s.documents {
		out = append(out, SearchResult{
			ID:       doc.ID,
			Text:     doc.Text,
			Score:    0,
			Metadata: doc.Metadata,
		})
	}
	return out, nil
}

// InMemoryVectorStore 内存向量库实现，用于测试与本地无 chromem-go 持久化场景。
// 暴力遍历计算 cosine 相似度，适合小规模语料。
type InMemoryVectorStore struct {
	mu   sync.RWMutex
	docs []Document
}

// NewInMemoryVectorStore 创建内存向量库实例。
func NewInMemoryVectorStore() *InMemoryVectorStore {
	return &InMemoryVectorStore{}
}

// Add 追加文档到内存切片。
func (s *InMemoryVectorStore) Add(ctx context.Context, docs []Document) error {
	if len(docs) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs = append(s.docs, docs...)
	return nil
}

// SimilaritySearch 暴力遍历计算 cosine 相似度，过滤后取 topN。
func (s *InMemoryVectorStore) SimilaritySearch(ctx context.Context, queryVec []float32, filters map[string]string, limit int, scoreThreshold float64) ([]SearchResult, error) {
	if limit <= 0 {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	results := make([]SearchResult, 0, len(s.docs))
	for _, doc := range s.docs {
		// 元数据等值过滤：所有 filters 键必须匹配
		if !matchFilters(doc.Metadata, filters) {
			continue
		}
		score := cosineSimilarity(queryVec, doc.Embedding)
		if scoreThreshold > 0 && score < scoreThreshold {
			continue
		}
		results = append(results, SearchResult{
			ID:       doc.ID,
			Text:     doc.Text,
			Score:    score,
			Metadata: doc.Metadata,
		})
	}
	// 按分数降序取 topN
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// HybridSearch 内存向量库无 BM25，混合检索降级为纯向量检索。
func (s *InMemoryVectorStore) HybridSearch(ctx context.Context, query string, queryVec []float32, filters map[string]string, limit int, scoreThreshold float64) ([]SearchResult, error) {
	return s.SimilaritySearch(ctx, queryVec, filters, limit, scoreThreshold)
}

// DeleteByDocumentID 按 document_id 从内存切片移除。
func (s *InMemoryVectorStore) DeleteByDocumentID(ctx context.Context, documentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.docs)
	filtered := s.docs[:0]
	for _, doc := range s.docs {
		if doc.Metadata["document_id"] != documentID {
			filtered = append(filtered, doc)
		}
	}
	// 清零尾部槽位，释放被删文档的 Embedding 切片引用以便 GC（L8）
	for i := len(filtered); i < n; i++ {
		s.docs[i] = Document{}
	}
	s.docs = filtered
	return nil
}

// Warmup 内存向量库无需预热。
func (s *InMemoryVectorStore) Warmup(ctx context.Context) error {
	return nil
}

// GetAll 返回内存中所有文档。
func (s *InMemoryVectorStore) GetAll(ctx context.Context) ([]SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SearchResult, 0, len(s.docs))
	for _, doc := range s.docs {
		out = append(out, SearchResult{
			ID:       doc.ID,
			Text:     doc.Text,
			Metadata: doc.Metadata,
		})
	}
	return out, nil
}

// matchFilters 检查 metadata 是否满足所有等值过滤条件。
func matchFilters(metadata, filters map[string]string) bool {
	for k, v := range filters {
		if metadata[k] != v {
			return false
		}
	}
	return true
}

// cosineSimilarity 计算两个向量的余弦相似度。
// 任一向量为零向量时返回 0，避免除零。
func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	normA = math.Sqrt(normA)
	normB = math.Sqrt(normB)
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (normA * normB)
}

// NewVectorStore 根据 Settings 返回默认向量库实现。
// 当前固定返回 ChromemVectorStore；后续可按 settings 字段分支扩展。
func NewVectorStore(settings Settings) (VectorStore, error) {
	store, err := NewChromemVectorStore(settings.ChromaPersistPath, "ai_qa_chunks")
	if err != nil {
		return nil, err
	}
	return store, nil
}
