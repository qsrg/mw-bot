// File hybrid_search_test.go: RRF 融合与混合检索单元测试。
// 对齐 Python test_hybrid_search.py 中 rrf_fuse 与 hybrid_search 部分。
package common

import (
	"context"
	"testing"
)

// TestRRFFusionMergesAndNormalizes 验证两路结果按 RRF 融合，最高分归一为 1.0。
// b 在 dense 与 sparse 两路均靠前，应居首。
func TestRRFFusionMergesAndNormalizes(t *testing.T) {
	dense := []SearchResult{
		{Text: "a", Score: 0.9, Metadata: map[string]string{"chunk_id": "c1"}},
		{Text: "b", Score: 0.8, Metadata: map[string]string{"chunk_id": "c2"}},
	}
	sparse := []SearchResult{
		{Text: "b", Score: 2.5, Metadata: map[string]string{"chunk_id": "c2"}},
		{Text: "c", Score: 1.0, Metadata: map[string]string{"chunk_id": "c3"}},
	}
	fused := RRFFusion(dense, sparse, 60, 3)
	if len(fused) == 0 {
		t.Fatal("期望非空融合结果")
	}
	if fused[0].Metadata["chunk_id"] != "c2" {
		t.Errorf("首位应命中 c2, 实际 %s", fused[0].Metadata["chunk_id"])
	}
	if fused[0].Score != 1.0 {
		t.Errorf("归一化后最高分应 1.0, 实际 %f", fused[0].Score)
	}
	ids := make(map[string]bool)
	for _, f := range fused {
		ids[f.Metadata["chunk_id"]] = true
	}
	for _, want := range []string{"c1", "c2", "c3"} {
		if !ids[want] {
			t.Errorf("融合结果应包含 %s", want)
		}
	}
}

// TestRRFFusionDedupSameChunk 验证同一 chunk 在两路出现应合并分数而非重复。
func TestRRFFusionDedupSameChunk(t *testing.T) {
	dense := []SearchResult{
		{Text: "a", Score: 0.9, Metadata: map[string]string{"chunk_id": "c1"}},
	}
	sparse := []SearchResult{
		{Text: "a", Score: 2.0, Metadata: map[string]string{"chunk_id": "c1"}},
	}
	fused := RRFFusion(dense, sparse, 60, 5)
	if len(fused) != 1 {
		t.Errorf("去重后期望 1 条, 实际 %d", len(fused))
	}
	if fused[0].Metadata["chunk_id"] != "c1" {
		t.Errorf("期望 c1, 实际 %s", fused[0].Metadata["chunk_id"])
	}
}

// TestRRFFusionEmptyLists 验证空输入返回空。
func TestRRFFusionEmptyLists(t *testing.T) {
	if got := RRFFusion(nil, nil, 60, 5); len(got) != 0 {
		t.Errorf("双空输入期望空结果, 实际 %v", got)
	}
	if got := RRFFusion([]SearchResult{}, []SearchResult{}, 60, 5); len(got) != 0 {
		t.Errorf("双空切片期望空结果, 实际 %v", got)
	}
}

// TestRRFFusionZeroTopKReturnsEmpty 验证 topK<=0 返回空。
func TestRRFFusionZeroTopKReturnsEmpty(t *testing.T) {
	dense := []SearchResult{{Text: "a", Metadata: map[string]string{"chunk_id": "c1"}}}
	if got := RRFFusion(dense, nil, 60, 0); len(got) != 0 {
		t.Errorf("topK=0 期望空结果, 实际 %v", got)
	}
}

// TestRRFFusionDefaultK 验证 k<=0 时使用默认 k=60。
func TestRRFFusionDefaultK(t *testing.T) {
	dense := []SearchResult{
		{Text: "a", Metadata: map[string]string{"chunk_id": "c1"}},
		{Text: "b", Metadata: map[string]string{"chunk_id": "c2"}},
	}
	// k<=0 应使用默认 60，不 panic
	fused := RRFFusion(dense, nil, 0, 2)
	if len(fused) != 2 {
		t.Errorf("期望 2 条, 实际 %d", len(fused))
	}
}

// TestHybridSearchWithBM25 验证 dense + BM25 融合后返回相关片段。
// 查询向量偏向 d1，关键词也指向 d1，融合后 d1 应居首。
func TestHybridSearchWithBM25(t *testing.T) {
	vs := NewInMemoryVectorStore()
	docs := []Document{
		{
			ID:        "c1",
			Text:      "RocketMQ 消费堆积处理与集群排查",
			Embedding: []float32{1.0, 0.0},
			Metadata:  map[string]string{"document_id": "d1", "knowledge_base_id": "1"},
		},
		{
			ID:        "c2",
			Text:      "Redis 内存优化与持久化配置",
			Embedding: []float32{0.0, 1.0},
			Metadata:  map[string]string{"document_id": "d2", "knowledge_base_id": "1"},
		},
	}
	if err := vs.Add(context.Background(), docs); err != nil {
		t.Fatalf("Add 失败: %v", err)
	}
	bm25 := NewBM25Index()
	bm25.AddDocuments([]BM25Doc{
		{ID: "c1", Text: "RocketMQ 消费堆积处理与集群排查", Metadata: map[string]string{"document_id": "d1"}},
		{ID: "c2", Text: "Redis 内存优化与持久化配置", Metadata: map[string]string{"document_id": "d2"}},
	})
	results, err := HybridSearch(context.Background(), vs, bm25, "RocketMQ 堆积", []float32{1.0, 0.0}, nil, 2, 0)
	if err != nil {
		t.Fatalf("HybridSearch 失败: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("期望非空结果")
	}
	if results[0].Metadata["document_id"] != "d1" {
		t.Errorf("首位 document_id 期望 d1, 实际 %s", results[0].Metadata["document_id"])
	}
}

// TestHybridSearchBM25UnavailableDegradesToDense 验证 BM25 不可用时降级为纯向量。
func TestHybridSearchBM25UnavailableDegradesToDense(t *testing.T) {
	vs := NewInMemoryVectorStore()
	docs := []Document{
		{
			ID:        "c1",
			Text:      "相关内容",
			Embedding: []float32{1.0, 0.0},
			Metadata:  map[string]string{"document_id": "d1"},
		},
	}
	if err := vs.Add(context.Background(), docs); err != nil {
		t.Fatalf("Add 失败: %v", err)
	}
	// bm25 为 nil，应降级为纯向量
	results, err := HybridSearch(context.Background(), vs, nil, "任意", []float32{1.0, 0.0}, nil, 5, 0)
	if err != nil {
		t.Fatalf("HybridSearch 失败: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("降级后期望 1 条, 实际 %d", len(results))
	}
}

// TestHybridSearchEmptyStoreReturnsEmpty 验证空库检索返回空。
func TestHybridSearchEmptyStoreReturnsEmpty(t *testing.T) {
	vs := NewInMemoryVectorStore()
	bm25 := NewBM25Index()
	results, err := HybridSearch(context.Background(), vs, bm25, "任意", []float32{1.0, 0.0}, nil, 5, 0)
	if err != nil {
		t.Fatalf("HybridSearch 失败: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("空库期望空结果, 实际 %v", results)
	}
}

// TestHybridSearchZeroLimitReturnsEmpty 验证 limit<=0 返回空。
func TestHybridSearchZeroLimitReturnsEmpty(t *testing.T) {
	vs := NewInMemoryVectorStore()
	bm25 := NewBM25Index()
	results, err := HybridSearch(context.Background(), vs, bm25, "任意", []float32{1.0, 0.0}, nil, 0, 0)
	if err != nil {
		t.Fatalf("HybridSearch 失败: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("limit=0 期望空结果, 实际 %v", results)
	}
}

// TestHybridSearchNilVectorStoreReturnsEmpty 验证 vs 为 nil 返回空。
func TestHybridSearchNilVectorStoreReturnsEmpty(t *testing.T) {
	bm25 := NewBM25Index()
	results, err := HybridSearch(context.Background(), nil, bm25, "任意", []float32{1.0, 0.0}, nil, 5, 0)
	if err != nil {
		t.Fatalf("HybridSearch 失败: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("nil vs 期望空结果, 实际 %v", results)
	}
}

// TestHybridSearchFiltersByThreshold 验证 dense 路按 score_threshold 过滤低相关片段。
func TestHybridSearchFiltersByThreshold(t *testing.T) {
	vs := NewInMemoryVectorStore()
	docs := []Document{
		{
			ID:        "c1",
			Text:      "相关内容",
			Embedding: []float32{1.0, 0.0},
			Metadata:  map[string]string{"document_id": "d1"},
		},
		{
			ID:        "c2",
			Text:      "无关内容",
			Embedding: []float32{0.0, 1.0},
			Metadata:  map[string]string{"document_id": "d2"},
		},
	}
	if err := vs.Add(context.Background(), docs); err != nil {
		t.Fatalf("Add 失败: %v", err)
	}
	// 查询向量偏向 d1，阈值 0.5 过滤掉 d2（余弦相似度 0）
	results, err := HybridSearch(context.Background(), vs, nil, "", []float32{1.0, 0.0}, nil, 10, 0.5)
	if err != nil {
		t.Fatalf("HybridSearch 失败: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("阈值过滤后期望 1 条, 实际 %d", len(results))
	}
	if results[0].Metadata["document_id"] != "d1" {
		t.Errorf("期望 d1, 实际 %s", results[0].Metadata["document_id"])
	}
}

// TestHybridSearchFiltersByMetadata 验证 filters 限制 dense 路到指定知识库。
func TestHybridSearchFiltersByMetadata(t *testing.T) {
	vs := NewInMemoryVectorStore()
	docs := []Document{
		{
			ID:        "c1",
			Text:      "RocketMQ 堆积",
			Embedding: []float32{1.0, 0.0},
			Metadata:  map[string]string{"document_id": "d1", "knowledge_base_id": "1"},
		},
		{
			ID:        "c2",
			Text:      "RocketMQ 堆积",
			Embedding: []float32{1.0, 0.0},
			Metadata:  map[string]string{"document_id": "d2", "knowledge_base_id": "2"},
		},
	}
	if err := vs.Add(context.Background(), docs); err != nil {
		t.Fatalf("Add 失败: %v", err)
	}
	results, err := HybridSearch(context.Background(), vs, nil, "", []float32{1.0, 0.0}, map[string]string{"knowledge_base_id": "2"}, 5, 0)
	if err != nil {
		t.Fatalf("HybridSearch 失败: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("过滤后期望 1 条, 实际 %d", len(results))
	}
	if results[0].Metadata["knowledge_base_id"] != "2" {
		t.Errorf("期望 knowledge_base_id=2, 实际 %s", results[0].Metadata["knowledge_base_id"])
	}
}

// TestChunkKeyPriority 验证 chunkKey 合并键优先级：
// chunk_id 优先，其次 document_id_chunk_index，最后 text。
func TestChunkKeyPriority(t *testing.T) {
	// chunk_id 优先
	if got := chunkKey(map[string]string{"chunk_id": "x", "document_id": "d", "chunk_index": "0"}, "text"); got != "x" {
		t.Errorf("chunk_id 优先期望 x, 实际 %s", got)
	}
	// document_id + chunk_index 次之
	if got := chunkKey(map[string]string{"document_id": "d", "chunk_index": "0"}, "text"); got != "d_0" {
		t.Errorf("document_id_chunk_index 期望 d_0, 实际 %s", got)
	}
	// 兜底 text
	if got := chunkKey(map[string]string{}, "text"); got != "text" {
		t.Errorf("兜底期望 text, 实际 %s", got)
	}
}
