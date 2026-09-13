// File citations_test.go: 引用质量门控回归测试。
//
// 覆盖历史缺陷：语义相邻但无答案的弱命中（dense 余弦分压线过检索阈值，
// 如问 ZooKeeper 召回 Kafka 文档）进入引用列表与 prompt，
// 导致"模型推断"回复仍展示无关参考来源、used_model_inference 标识失真。
// 修复后 prepare 按 CitationScoreThreshold 过滤 DenseScore 不足的弱命中。
package rag

import (
	"context"
	"strings"
	"testing"

	"mw-bot/internal/common"
)

// zookeeperEmbedding 查询"什么是zookeeper"映射 queryVec，其余文本映射 docVec。
type zookeeperEmbedding struct {
	queryVec []float32
	docVec   []float32
}

func (f *zookeeperEmbedding) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for _, t := range texts {
		if strings.Contains(t, "什么是zookeeper") {
			out = append(out, f.queryVec)
		} else {
			out = append(out, f.docVec)
		}
	}
	return out, nil
}

// newCitationTestService 构建使用临时 chromem 库的 RagService，
// 写入一篇含中间件名标记、文本与查询有 BM25 词面重叠的文档。
func newCitationTestService(t *testing.T, docVec []float32) *RagService {
	t.Helper()
	t.Setenv("MIDDLEWARES", "kafka,rocketmq")
	settings := common.Settings{
		HybridSearch:                 true,
		RetrieveLimit:                5,
		RetrieveScoreThreshold:       0.3,
		CitationScoreThreshold:       0.4,
		AmbiguityProbeScoreThreshold: 0.5,
		RerankTopN:                   5,
		IntentDetection:              true,
	}
	store, err := common.NewChromemVectorStore(t.TempDir(), "test")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	ctx := context.Background()
	docs := []common.Document{
		{ID: "k1", Text: "kafka 集群协调与元数据管理说明", Embedding: docVec,
			Metadata: map[string]string{"document_id": "d1", "chunk_index": "0", "mw_kafka": "true"}},
	}
	if err := store.Add(ctx, docs); err != nil {
		t.Fatalf("add docs: %v", err)
	}
	if err := store.Warmup(ctx); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	return &RagService{
		embedding: &zookeeperEmbedding{queryVec: []float32{1, 0}, docVec: docVec},
		vector:    store,
		llm:       &fakeClarificationLLM{},
		settings:  settings,
	}
}

// TestCitationsGatedForWeakHits 弱命中（dense 余弦约 0.34，过检索阈值但不及引用阈值）
// 不应产生引用，prompt 中知识片段应为空，并标记为模型推断。
func TestCitationsGatedForWeakHits(t *testing.T) {
	// (1,0)·(0.34,0.94)≈0.34：语义相邻但无答案的典型弱命中分
	svc := newCitationTestService(t, []float32{0.34, 0.94})
	prep, err := svc.prepare(context.Background(), "什么是zookeeper", nil, nil, nil)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(prep.Citations) != 0 {
		t.Fatalf("弱命中不应产生引用, got %d 条", len(prep.Citations))
	}
	if !prep.UsedModelInference {
		t.Fatal("无引用时应标记模型推断")
	}
	lastUser := prep.Messages[len(prep.Messages)-1].Content
	if !strings.Contains(lastUser, "知识库片段:（无相关内容）") {
		t.Fatalf("prompt 知识片段应为空, got %.80q", lastUser)
	}
}

// TestCitationsKeptForStrongHits 强命中（dense 余弦 1.0）应保留引用、不算模型推断。
func TestCitationsKeptForStrongHits(t *testing.T) {
	svc := newCitationTestService(t, []float32{1, 0})
	prep, err := svc.prepare(context.Background(), "什么是zookeeper", nil, nil, nil)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(prep.Citations) != 1 {
		t.Fatalf("强命中应保留 1 条引用, got %d", len(prep.Citations))
	}
	if prep.UsedModelInference {
		t.Fatal("有引用时不应标记模型推断")
	}
}
