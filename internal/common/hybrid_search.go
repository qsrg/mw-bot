// File hybrid_search.go: 混合检索 RRF 融合实现，对齐 Python app/common/vector_store.py 的 rrf_fuse 与 hybrid_search。
//
// dense(向量) + sparse(BM25) 两路结果经 Reciprocal Rank Fusion 融合，
// 尺度无关，按融合后分数降序取 topN。BM25 不可用时降级为纯向量检索。
package common

import (
	"context"
	"sort"
)

// rrfDefaultK RRF 融合的默认 k 值（与 Python 对齐）。
// k 越大，排名靠后的结果贡献衰减越慢；k=60 是常见经验值。
const rrfDefaultK = 60

// rrfEntry RRF 融合过程中的中间结果，按合并键聚合两路贡献。
type rrfEntry struct {
	Text     string
	Metadata map[string]string
	RRFScore float64
}

// chunkKey 生成 RRF 融合的合并键：chunk_id 优先，否则 document_id_chunk_index，无则用 text。
// 同一 chunk 在 dense/sparse 两路命中时应合并而非重复计入。
func chunkKey(meta map[string]string, text string) string {
	if v, ok := meta["chunk_id"]; ok && v != "" {
		return v
	}
	docID, hasDoc := meta["document_id"]
	chunkIdx, hasIdx := meta["chunk_index"]
	if hasDoc && hasIdx && docID != "" {
		return docID + "_" + chunkIdx
	}
	return text
}

// RRFFusion 对 dense 与 sparse 两路结果做 Reciprocal Rank Fusion。
// 公式：score = sum(1/(k+rank+1))，rank 从 0 开始（与 Python rrf_fuse 对齐）；
// 按融合分数降序取 topK。分数归一化到 [0,1]（最高分为 1.0）以便引用展示。
func RRFFusion(denseResults, sparseResults []SearchResult, k int, topK int) []SearchResult {
	if k <= 0 {
		k = rrfDefaultK
	}
	if topK <= 0 {
		return nil
	}
	scores := make(map[string]*rrfEntry)
	// 累加每路排名贡献：1/(k+rank+1)，rank 从 0 开始
	contribute := func(ranked []SearchResult) {
		for rank, item := range ranked {
			key := chunkKey(item.Metadata, item.Text)
			entry, ok := scores[key]
			if !ok {
				// 深拷贝 metadata，避免外部修改
				meta := make(map[string]string, len(item.Metadata))
				for k, v := range item.Metadata {
					meta[k] = v
				}
				entry = &rrfEntry{
					Text:     item.Text,
					Metadata: meta,
				}
				scores[key] = entry
			}
			entry.RRFScore += 1.0 / float64(k+rank+1)
		}
	}
	contribute(denseResults)
	contribute(sparseResults)
	if len(scores) == 0 {
		return nil
	}
	// 转 slice 并按 RRF 分数降序
	ranked := make([]*rrfEntry, 0, len(scores))
	for _, e := range scores {
		ranked = append(ranked, e)
	}
	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].RRFScore > ranked[j].RRFScore
	})
	if len(ranked) > topK {
		ranked = ranked[:topK]
	}
	// 归一化：最高分为 1.0
	top := ranked[0].RRFScore
	if top == 0 {
		top = 1
	}
	out := make([]SearchResult, 0, len(ranked))
	for _, e := range ranked {
		out = append(out, SearchResult{
			ID:       e.Metadata["chunk_id"],
			Text:     e.Text,
			Score:    e.RRFScore / top,
			Metadata: e.Metadata,
		})
	}
	return out
}

// HybridSearch 应用层混合检索：dense(向量) + sparse(BM25) 经 RRF 融合。
// 两路各取 max(limit*3, 20) 候选，融合后取 topK=limit。BM25 不可用（nil 或无文档）时降级为纯向量。
// filters 同时作用于 dense 与 sparse 路（与 Python hybrid_search 对齐）。
func HybridSearch(ctx context.Context, vs VectorStore, bm25 *BM25Index, query string, queryVec []float32, filters map[string]string, limit int, scoreThreshold float64) ([]SearchResult, error) {
	if vs == nil {
		return nil, nil
	}
	if limit <= 0 {
		return nil, nil
	}
	// 两路各过采样 max(limit*3, 20)，给 RRF 融合留余量（与 Python 对齐）
	oversample := limit * 3
	if oversample < 20 {
		oversample = 20
	}
	dense, err := vs.SimilaritySearch(ctx, queryVec, filters, oversample, scoreThreshold)
	if err != nil {
		return nil, err
	}
	// BM25 不可用：直接返回 dense 的 topN
	if bm25 == nil || !bm25.IsAvailable() {
		if len(dense) > limit {
			dense = dense[:limit]
		}
		return dense, nil
	}
	sparseHits := bm25.Search(query, filters, oversample)
	if len(sparseHits) == 0 {
		// BM25 无命中：降级为纯向量
		if len(dense) > limit {
			dense = dense[:limit]
		}
		return dense, nil
	}
	// 转 sparse 为 SearchResult
	sparse := make([]SearchResult, 0, len(sparseHits))
	for _, h := range sparseHits {
		sparse = append(sparse, SearchResult{
			ID:       h.ID,
			Text:     h.Text,
			Score:    h.Score,
			Metadata: h.Metadata,
		})
	}
	fused := RRFFusion(dense, sparse, rrfDefaultK, limit)
	return fused, nil
}
