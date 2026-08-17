// File bm25_index.go: BM25 稀疏检索索引，对齐 Python app/common/bm25_index.py。
//
// chromem-go 不支持原生混合检索（dense + sparse/BM25 融合），这里在应用层
// 维护一份独立的 BM25 索引，与向量库的稠密检索结果做 RRF 融合。
//
// 线程安全：所有写操作在内部 RWMutex 内完成；search 取快照后在锁外打分。
// 数据源来自向量库的 GetAll/Add；本实现不依赖外部存储。
package common

import (
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/go-ego/gse"
)

// BM25 参数常量，与 rank_bm25.BM25Okapi 默认值一致。
const (
	defaultBM25K1 = 1.5 // 词频饱和参数
	defaultBM25B  = 0.75 // 文档长度归一化参数
)

// chineseStopwords 中文停用词集合，与 Python bm25_index.py 对齐。
// 高频虚词/代词/助词在 BM25 中几乎无区分度却会引发误匹配
// （如"你是谁"经"是"命中所有文档，经 RRF 归一化为高分绕过相关性阈值）。
var chineseStopwords = map[string]struct{}{
	"的": {}, "了": {}, "是": {}, "在": {}, "和": {}, "与": {}, "或": {}, "也": {},
	"都": {}, "就": {}, "还": {}, "又": {}, "不": {}, "没": {}, "有": {}, "为": {},
	"以": {}, "及": {}, "而": {}, "但": {}, "对": {}, "把": {}, "被": {}, "让": {},
	"给": {}, "向": {}, "从": {}, "到": {}, "上": {}, "下": {}, "中": {}, "里": {},
	"这": {}, "那": {}, "之": {}, "其": {}, "此": {}, "每": {}, "各": {}, "我": {},
	"你": {}, "您": {}, "他": {}, "她": {}, "它": {}, "谁": {}, "呢": {}, "吗": {},
	"啊": {}, "呀": {}, "哈": {}, "哦": {}, "嗯": {}, "嘛": {}, "吧": {}, "啦": {},
	"个": {}, "些": {}, "等": {}, "着": {}, "地": {}, "得": {}, "过": {},
	"什么": {}, "怎么": {}, "如何": {}, "为什么": {}, "可以": {}, "应该": {}, "需要": {},
	"我们": {}, "你们": {}, "他们": {}, "这种": {}, "那种": {}, "一个": {},
}

// BM25Doc BM25 索引中的一条文档。Tokens 为分词后的 token 列表。
type BM25Doc struct {
	ID       string            // 文档/chunk 全局唯一 ID
	Tokens   []string          // 分词后的 token 列表
	Text     string            // 原始文本，用于结果展示
	Metadata map[string]string // 元数据，用于过滤
}

// BM25SearchResult BM25 检索单条结果。
type BM25SearchResult struct {
	ID       string            // 文档/chunk ID
	Score    float64          // BM25 分数，越高越相关
	Text     string            // 原始文本
	Metadata map[string]string // 元数据
}

// BM25Index 进程内 BM25 索引。
//
// 实现 BM25Okapi 算法；分词使用 gse 加载内嵌中文词典。
// 增删时直接更新内部结构，无需重建；search 取快照后打分。
type BM25Index struct {
	k1        float64           // 词频饱和参数
	b         float64           // 文档长度归一化参数
	mu        sync.RWMutex      // 保护 docs/avgDocLen/docFreq
	docs      []BM25Doc         // 文档列表
	avgDocLen float64           // 平均文档长度（token 数）
	docFreq   map[string]int    // 每个 token 在多少文档中出现过（document frequency）
	avgIDF    float64           // 全语料 IDF 均值，用于负 IDF 的 epsilon 兜底（对齐 rank_bm25）
	tokenizer *gse.Segmenter    // gse 分词器，nil 表示降级为字符级切分
}

// NewBM25Index 创建 BM25 索引并加载 gse 中文词典。
// 词典加载失败时不阻断，降级为按字符切分（每个汉字/字母独立成 token）。
func NewBM25Index() *BM25Index {
	seg := &gse.Segmenter{SkipLog: true} // 抑制 gse 内部 log.Print 输出
	// 优先使用内嵌词典，无需外部文件；"zh" 加载简繁中文词典
	if err := seg.LoadDictEmbed("zh"); err != nil {
		// 降级：tokenizer 仍非 nil，但 Dict 为空，Cut 会按字符切分
		seg = &gse.Segmenter{SkipLog: true}
	}
	return &BM25Index{
		k1:        defaultBM25K1,
		b:         defaultBM25B,
		docFreq:   make(map[string]int),
		tokenizer: seg,
	}
}

// tokenize 将文本切分为 token，过滤标点、空白与中文停用词。
// 词典未加载时按字符切分；保留中英文词与数字。
func (idx *BM25Index) tokenize(text string) []string {
	if text == "" {
		return nil
	}
	var rawTokens []string
	if idx.tokenizer != nil && idx.tokenizer.Dict != nil {
		rawTokens = idx.tokenizer.Cut(text)
	} else {
		// 降级：按 rune 切分，每个字符一个 token
		for _, r := range text {
			s := string(r)
			if strings.TrimSpace(s) != "" {
				rawTokens = append(rawTokens, s)
			}
		}
	}
	result := make([]string, 0, len(rawTokens))
	for _, tok := range rawTokens {
		// 过滤纯标点/空白（无字母数字的 token）
		if !hasAlnum(tok) {
			continue
		}
		// 过滤中文停用词
		if _, stop := chineseStopwords[tok]; stop {
			continue
		}
		result = append(result, tok)
	}
	return result
}

// hasAlnum 判断字符串是否包含字母或数字。
// 与 Python bm25_index.py 的 ch.isalnum() 语义对齐：中文标点（，。、等）
// 不算字母数字，避免标点 token 污染索引与文档长度归一化。
func hasAlnum(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// AddDocuments 分词后追加文档到索引，同步更新 avgDocLen 与 docFreq。
// 重复 ID 的文档会被当作新条目追加（调用方需自行去重）。
func (idx *BM25Index) AddDocuments(docs []BM25Doc) {
	if len(docs) == 0 {
		return
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	for i := range docs {
		if docs[i].Tokens == nil {
			docs[i].Tokens = idx.tokenize(docs[i].Text)
		}
		idx.docs = append(idx.docs, docs[i])
		// 更新 docFreq：每个 token 在本文档中首次出现才计数
		seen := make(map[string]struct{})
		for _, tok := range docs[i].Tokens {
			if _, ok := seen[tok]; ok {
				continue
			}
			seen[tok] = struct{}{}
			idx.docFreq[tok]++
		}
	}
	// 重新计算平均文档长度与 IDF 均值
	idx.recomputeAvgDocLenLocked()
	idx.recomputeAvgIDFLocked()
}

// recomputeAvgDocLenLocked 全量重算平均文档长度，调用方需持锁。
func (idx *BM25Index) recomputeAvgDocLenLocked() {
	if len(idx.docs) == 0 {
		idx.avgDocLen = 0
		return
	}
	total := 0
	for _, d := range idx.docs {
		total += len(d.Tokens)
	}
	idx.avgDocLen = float64(total) / float64(len(idx.docs))
}

// recomputeAvgIDFLocked 全量重算语料 IDF 均值，调用方需持锁。
// 对齐 rank_bm25.BM25Okapi：负 IDF 不直接截断为 0，而用 epsilon=0.25*average_idf 兜底，
// 避免高频词贡献被完全抹零导致排序偏差。
func (idx *BM25Index) recomputeAvgIDFLocked() {
	if len(idx.docFreq) == 0 {
		idx.avgIDF = 0
		return
	}
	n := len(idx.docs)
	sum := 0.0
	for _, df := range idx.docFreq {
		sum += math.Log((float64(n-df) + 0.5) / (float64(df) + 0.5))
	}
	idx.avgIDF = sum / float64(len(idx.docFreq))
}

// RemoveByDocumentID 按 document_id 移除所有相关文档，并更新 docFreq。
func (idx *BM25Index) RemoveByDocumentID(documentID string) {
	if documentID == "" {
		return
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	filtered := idx.docs[:0]
	removed := make([]BM25Doc, 0)
	for _, d := range idx.docs {
		if d.Metadata["document_id"] == documentID {
			removed = append(removed, d)
			continue
		}
		filtered = append(filtered, d)
	}
	idx.docs = filtered
	// 同步递减 docFreq；为 0 时删除键
	for _, d := range removed {
		seen := make(map[string]struct{})
		for _, tok := range d.Tokens {
			if _, ok := seen[tok]; ok {
				continue
			}
			seen[tok] = struct{}{}
			if idx.docFreq[tok] > 0 {
				idx.docFreq[tok]--
				if idx.docFreq[tok] == 0 {
					delete(idx.docFreq, tok)
				}
			}
		}
	}
	idx.recomputeAvgDocLenLocked()
	idx.recomputeAvgIDFLocked()
}

// Size 返回当前索引的文档数。
func (idx *BM25Index) Size() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.docs)
}

// IsAvailable 是否已加载文档（可参与稀疏检索）。
func (idx *BM25Index) IsAvailable() bool {
	return idx.Size() > 0
}

// Search 分词查询后按 BM25 公式打分，返回 topK 结果。
// filters 为元数据等值过滤（与稠密路保持一致），仅命中过滤条件的文档参与打分，
// 对齐 Python bm25_index.py 的 search(query_tokens, filters, limit)。
// 索引为空或查询分词为空时返回 nil。结果按分数降序，排除与查询无 token 交集的文档。
func (idx *BM25Index) Search(query string, filters map[string]string, topK int) []BM25SearchResult {
	if topK <= 0 || query == "" {
		return nil
	}
	queryTokens := idx.tokenize(query)
	if len(queryTokens) == 0 {
		return nil
	}
	idx.mu.RLock()
	docs := make([]BM25Doc, len(idx.docs))
	copy(docs, idx.docs)
	avgDocLen := idx.avgDocLen
	avgIDF := idx.avgIDF
	docFreq := make(map[string]int, len(idx.docFreq))
	for k, v := range idx.docFreq {
		docFreq[k] = v
	}
	idx.mu.RUnlock()

	if len(docs) == 0 || avgDocLen == 0 {
		return nil
	}
	N := len(docs)
	// 查询 token 去重，避免重复加分
	queryTokenSet := make(map[string]struct{})
	for _, t := range queryTokens {
		queryTokenSet[t] = struct{}{}
	}
	// 负 IDF 兜底值：对齐 rank_bm25 的 epsilon=0.25*average_idf
	epsilon := 0.25 * avgIDF
	results := make([]BM25SearchResult, 0, topK)
	for _, doc := range docs {
		// 元数据过滤：不满足 filters 的文档不参与稀疏检索
		if !matchFilters(doc.Metadata, filters) {
			continue
		}
		// 计算 token 频次
		tf := make(map[string]int)
		for _, t := range doc.Tokens {
			tf[t]++
		}
		// 是否与查询有 token 交集（无交集则跳过，避免无关文档经 RRF 归一化）
		hasOverlap := false
		for t := range queryTokenSet {
			if _, ok := tf[t]; ok {
				hasOverlap = true
				break
			}
		}
		if !hasOverlap {
			continue
		}
		score := 0.0
		docLen := len(doc.Tokens)
		for t := range queryTokenSet {
			f := tf[t]
			if f == 0 {
				continue
			}
			n := docFreq[t]
			// IDF: log((N - n + 0.5) / (n + 0.5))；负值用 epsilon 兜底（对齐 rank_bm25）
			idf := math.Log((float64(N-n) + 0.5) / (float64(n) + 0.5))
			if idf < 0 {
				idf = epsilon
			}
			// BM25 公式：IDF * (f * (k1+1)) / (f + k1 * (1 - b + b * |d|/avgdl))
			denom := float64(f) + idx.k1*(1-idx.b+idx.b*float64(docLen)/avgDocLen)
			if denom == 0 {
				continue
			}
			score += idf * (float64(f) * (idx.k1 + 1)) / denom
		}
		// 注意：BM25 分数为 0 不代表无重叠——小语料下 IDF 可能为 0，
		// 故按 token 交集（hasOverlap）而非分数判断是否命中。
		// 与 Python bm25_index.py 行为对齐，避免小语料下漏召回。
		results = append(results, BM25SearchResult{
			ID:       doc.ID,
			Score:    score,
			Text:     doc.Text,
			Metadata: doc.Metadata,
		})
	}
	// 按分数降序取 topK
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	if len(results) > topK {
		results = results[:topK]
	}
	return results
}

// Reset 清空索引。
func (idx *BM25Index) Reset() {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.docs = nil
	idx.docFreq = make(map[string]int)
	idx.avgDocLen = 0
	idx.avgIDF = 0
}
