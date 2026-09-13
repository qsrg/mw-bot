// File clarification_test.go: 跨中间件歧义反问触发条件的回归测试。
//
// 覆盖历史缺陷：探测误走 HybridSearch 时，RRF 融合分归一化到 [0,1] 且不与阈值比较，
// BM25 高频词弱命中（如"使用/回答"出现在各中间件文档中）会绕过阈值，
// 导致风格指令类输入（"使用英文回答"）被误反问。
// 修复后探测走纯向量检索，余弦分与阈值语义匹配。
package rag

import (
	"context"
	"io"
	"strings"
	"testing"

	"mw-bot/internal/common"
)

// fakeEmbedding 按文本映射固定向量：queryVec 对应查询，docVec 对应文档。
// 两者正交，余弦相似度为 0，模拟"语义无关但 BM25 词面命中"的场景。
type fakeEmbedding struct {
	queryVec []float32
	docVec   []float32
}

func (f *fakeEmbedding) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for _, t := range texts {
		if strings.Contains(t, "使用英文回答") {
			out = append(out, f.queryVec)
		} else {
			out = append(out, f.docVec)
		}
	}
	return out, nil
}

// fakeClarificationLLM 意图判定固定返回 knowledge（最坏情况），正文返回固定文本。
type fakeClarificationLLM struct{}

func (f *fakeClarificationLLM) Chat(_ context.Context, msgs []common.Message) (string, error) {
	if strings.Contains(msgs[0].Content, "意图判定") {
		return `{"intent":"knowledge"}`, nil
	}
	return "OK", nil
}

func (f *fakeClarificationLLM) StreamChat(_ context.Context, _ []common.Message) (func() (string, string, error), error) {
	return func() (string, string, error) { return "", "", io.EOF }, nil
}

// newClarificationTestService 构建使用临时 chromem 库的 RagService。
// 两个中间件各写入一篇文档，文本含与查询重叠的高频词（使用/回答），向量与查询正交。
func newClarificationTestService(t *testing.T, docVec []float32) *RagService {
	t.Helper()
	t.Setenv("MIDDLEWARES", "kafka,rocketmq")
	settings := common.Settings{
		HybridSearch:                 true,
		RetrieveLimit:                5,
		RetrieveScoreThreshold:       0.3,
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
		{ID: "k1", Text: "kafka 使用文档中包含回答示例，介绍基本用法", Embedding: docVec,
			Metadata: map[string]string{"document_id": "d1", "chunk_index": "0", "mw_kafka": "true"}},
		{ID: "r1", Text: "rocketmq 使用文档中包含回答示例，介绍基本用法", Embedding: docVec,
			Metadata: map[string]string{"document_id": "d2", "chunk_index": "0", "mw_rocketmq": "true"}},
	}
	if err := store.Add(ctx, docs); err != nil {
		t.Fatalf("add docs: %v", err)
	}
	if err := store.Warmup(ctx); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	return &RagService{
		embedding: &fakeEmbedding{queryVec: []float32{1, 0}, docVec: docVec},
		vector:    store,
		llm:       &fakeClarificationLLM{},
		settings:  settings,
	}
}

// TestClarificationNotTriggeredByWeakHits 风格指令语义无关（余弦 0）时不应反问，
// 即使 BM25 高频词在多个中间件文档中弱命中。
func TestClarificationNotTriggeredByWeakHits(t *testing.T) {
	svc := newClarificationTestService(t, []float32{0, 1})
	ans, err := svc.Answer(context.Background(), "使用英文回答", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if IsClarification(ans.Content) {
		t.Fatalf("弱命中不应触发反问，got %q", ans.Content)
	}
	if ans.Content != "OK" {
		t.Fatalf("expected normal answer, got %q", ans.Content)
	}
}

// TestClarificationTriggeredByStrongHits 语义强相关（余弦 1.0 超过探测阈值）且
// 未指定中间件时，仍应正常反问。
func TestClarificationTriggeredByStrongHits(t *testing.T) {
	svc := newClarificationTestService(t, []float32{1, 0})
	ans, err := svc.Answer(context.Background(), "使用英文回答", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if !IsClarification(ans.Content) {
		t.Fatalf("强相关多中间件命中应触发反问，got %q", ans.Content)
	}
}
