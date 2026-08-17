// File bm25_index_test.go: BM25 索引构建、检索、删除与停用词过滤单元测试。
// 对齐 Python test_hybrid_search.py 中 BM25Index 部分（Go BM25.Search 不支持 filters，跳过相关用例）。
package common

import (
	"testing"
)

// addBM25Docs 辅助构造 BM25Doc 切片并追加到索引。
func addBM25Docs(t *testing.T, idx *BM25Index, texts []string, meta []map[string]string) {
	t.Helper()
	docs := make([]BM25Doc, 0, len(texts))
	for i, text := range texts {
		m := map[string]string{}
		if i < len(meta) {
			m = meta[i]
		}
		docs = append(docs, BM25Doc{
			ID:       m["document_id"],
			Text:     text,
			Metadata: m,
		})
	}
	idx.AddDocuments(docs)
}

// TestBM25SearchRanksKeywordMatchFirst 验证 BM25 把含查询关键词的片段排在前面。
func TestBM25SearchRanksKeywordMatchFirst(t *testing.T) {
	idx := NewBM25Index()
	addBM25Docs(t, idx,
		[]string{"RocketMQ 消费堆积处理与集群排查", "Redis 内存优化与持久化配置"},
		[]map[string]string{{"document_id": "d1"}, {"document_id": "d2"}},
	)
	hits := idx.Search("RocketMQ 堆积", nil, 2)
	if len(hits) == 0 {
		t.Fatal("期望非空结果")
	}
	if !startsWith(hits[0].Text, "RocketMQ") {
		t.Errorf("首位应命中 RocketMQ 文档, 实际 %s", hits[0].Text)
	}
	if hits[0].Metadata["document_id"] != "d1" {
		t.Errorf("首位 document_id 期望 d1, 实际 %s", hits[0].Metadata["document_id"])
	}
}

// TestBM25RemoveByDocumentID 验证按 document_id 移除后该文档不再被检索。
func TestBM25RemoveByDocumentID(t *testing.T) {
	idx := NewBM25Index()
	addBM25Docs(t, idx,
		[]string{"RocketMQ 堆积", "Redis 优化"},
		[]map[string]string{{"document_id": "d1"}, {"document_id": "d2"}},
	)
	idx.RemoveByDocumentID("d1")
	hits := idx.Search("RocketMQ", nil, 10)
	for _, h := range hits {
		if h.Metadata["document_id"] == "d1" {
			t.Error("删除后 d1 不应再被检索")
		}
	}
}

// TestBM25ResetClearsIndex 验证 Reset 清空索引。
func TestBM25ResetClearsIndex(t *testing.T) {
	idx := NewBM25Index()
	addBM25Docs(t, idx,
		[]string{"RocketMQ 堆积"},
		[]map[string]string{{"document_id": "d1"}},
	)
	if !idx.IsAvailable() {
		t.Fatal("添加后应可用")
	}
	idx.Reset()
	if idx.IsAvailable() {
		t.Error("Reset 后应不可用")
	}
	if idx.Size() != 0 {
		t.Errorf("Reset 后 Size 期望 0, 实际 %d", idx.Size())
	}
}

// TestBM25EmptyIndexReturnsEmpty 验证空索引检索返回空，不抛异常。
func TestBM25EmptyIndexReturnsEmpty(t *testing.T) {
	idx := NewBM25Index()
	if hits := idx.Search("任意", nil, 5); len(hits) != 0 {
		t.Errorf("空索引期望空结果, 实际 %v", hits)
	}
	if idx.IsAvailable() {
		t.Error("空索引 IsAvailable 应为 false")
	}
}

// TestBM25SearchExcludesZeroScoreDocs 验证 BM25 不返回与查询无 token 交集的文档。
// 否则零分文档经 RRF 归一化绕过相关性阈值。
func TestBM25SearchExcludesZeroScoreDocs(t *testing.T) {
	idx := NewBM25Index()
	addBM25Docs(t, idx,
		[]string{"RocketMQ 堆积", "Redis 优化"},
		[]map[string]string{{"document_id": "d1"}, {"document_id": "d2"}},
	)
	hits := idx.Search("RocketMQ", nil, 10)
	if len(hits) == 0 {
		t.Fatal("期望非空结果")
	}
	for _, h := range hits {
		if h.Metadata["document_id"] != "d1" {
			t.Errorf("只应命中 d1, 实际命中 %s", h.Metadata["document_id"])
		}
	}
}

// TestBM25StopwordOnlyQueryReturnsEmpty 验证全停用词查询不命中任何文档。
// "你是谁" 全是停用词，分词后为空，不应命中。
func TestBM25StopwordOnlyQueryReturnsEmpty(t *testing.T) {
	idx := NewBM25Index()
	addBM25Docs(t, idx,
		[]string{"RocketMQ 是消息队列", "Redis 是缓存"},
		[]map[string]string{{"document_id": "d1"}, {"document_id": "d2"}},
	)
	if hits := idx.Search("你是谁", nil, 5); len(hits) != 0 {
		t.Errorf("全停用词查询期望空结果, 实际 %v", hits)
	}
}

// TestBM25SearchEmptyQueryReturnsEmpty 验证空查询返回空。
func TestBM25SearchEmptyQueryReturnsEmpty(t *testing.T) {
	idx := NewBM25Index()
	addBM25Docs(t, idx,
		[]string{"RocketMQ 堆积"},
		[]map[string]string{{"document_id": "d1"}},
	)
	if hits := idx.Search("", nil, 5); len(hits) != 0 {
		t.Errorf("空查询期望空结果, 实际 %v", hits)
	}
}

// TestBM25SearchZeroTopKReturnsEmpty 验证 topK<=0 返回空。
func TestBM25SearchZeroTopKReturnsEmpty(t *testing.T) {
	idx := NewBM25Index()
	addBM25Docs(t, idx,
		[]string{"RocketMQ 堆积"},
		[]map[string]string{{"document_id": "d1"}},
	)
	if hits := idx.Search("RocketMQ", nil, 0); len(hits) != 0 {
		t.Errorf("topK=0 期望空结果, 实际 %v", hits)
	}
}

// TestBM25SearchRespectsTopK 验证 topK 限制结果数。
func TestBM25SearchRespectsTopK(t *testing.T) {
	idx := NewBM25Index()
	addBM25Docs(t, idx,
		[]string{"RocketMQ 堆积", "RocketMQ 集群", "RocketMQ 配置"},
		[]map[string]string{
			{"document_id": "d1"},
			{"document_id": "d2"},
			{"document_id": "d3"},
		},
	)
	hits := idx.Search("RocketMQ", nil, 2)
	if len(hits) > 2 {
		t.Errorf("topK=2 期望最多 2 条, 实际 %d", len(hits))
	}
}

// TestBM25AddDocumentsIncremental 验证增量追加文档后检索能命中新增内容。
func TestBM25AddDocumentsIncremental(t *testing.T) {
	idx := NewBM25Index()
	addBM25Docs(t, idx,
		[]string{"RocketMQ 堆积"},
		[]map[string]string{{"document_id": "d1"}},
	)
	addBM25Docs(t, idx,
		[]string{"Kafka 集群"},
		[]map[string]string{{"document_id": "d2"}},
	)
	hits := idx.Search("Kafka", nil, 5)
	if len(hits) == 0 {
		t.Fatal("增量追加的 Kafka 文档应被检索到")
	}
	if hits[0].Metadata["document_id"] != "d2" {
		t.Errorf("期望命中 d2, 实际 %s", hits[0].Metadata["document_id"])
	}
}

// startsWith 检查字符串是否以指定前缀开头。
func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
