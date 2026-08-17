// File chunking.go: 递归字符分块，对齐 Python app/ingestion/chunking.py。
//
// 实现与 langchain RecursiveCharacterTextSplitter 等价的算法：
//   - 按分隔符优先级（从粗到细）递归切分文本
//   - 长度 < chunkSize 的片段直接保留，超长片段用更细的分隔符递归
//   - 合并相邻片段到 chunkSize 大小的块，相邻块共享 overlap 字符重叠
//   - keep_separator=true：分隔符作为独立 split 保留，合并时重新拼接
package ingestion

import (
	"strings"
	"unicode/utf8"
)

// runeLen 返回字符串的字符数（rune 计数），对齐 Python len() 行为。
// Go 的 len(string) 返回字节数，中文 UTF-8 占 3 字节会导致 chunk 边界与 Python 不一致。
func runeLen(s string) int {
	return utf8.RuneCountInString(s)
}

// DocumentChunk 分块结果，携带来源元数据用于答案引用。
// 字段与 Python DocumentChunk dataclass 对齐。
type DocumentChunk struct {
	Text       string         `json:"text"`        // chunk 文本
	ChunkIndex int            `json:"chunk_index"` // chunk 序号
	Metadata   map[string]any `json:"metadata"`    // 来源元数据
}

// DefaultSeparators 默认分隔符优先级：从语义边界最粗到最细，末尾空串为字符级兜底。
// 兼顾中文（。；！？）与英文（. ! ?）句子边界，以及段落、换行、空格。
var DefaultSeparators = []string{
	"\n\n",
	"\n",
	"。",
	"；",
	"！",
	"？",
	". ",
	"! ",
	"? ",
	" ",
	"",
}

// DefaultChunkSize 默认单块最大字符数。
const DefaultChunkSize = 800

// DefaultOverlap 默认相邻块重叠字符数。
const DefaultOverlap = 120

// ChunkText 递归字符分块：按分隔符优先级切分，保留重叠与来源元数据。
//
// 参数：
//   - parsed: 解析后的文档。
//   - chunkSize: 单块最大字符数，<=0 用默认 800。
//   - overlap: 相邻块重叠字符数，<0 用默认 120。
//   - documentID: 文档对外标识 uuid，写入来源元数据 document_id。
//   - knowledgeBaseID: 知识库自增主键ID，写入来源元数据 knowledge_base_id。
//   - middlewares: 文档涉及的中间件列表，每个写成 mw_<name>=true 写入元数据。
//   - separators: 分隔符优先级列表，nil 用默认 DefaultSeparators。
//
// 返回：
//   - []DocumentChunk: 分块列表，每块含 chunk_index 与来源元数据；空文本返回 nil。
func ChunkText(
	parsed ParsedDocument,
	chunkSize, overlap int,
	documentID string,
	knowledgeBaseID int64,
	middlewares []string,
	separators []string,
) []DocumentChunk {
	text := strings.TrimSpace(parsed.Text)
	if text == "" {
		return nil
	}
	if separators == nil {
		separators = DefaultSeparators
	}
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	if overlap < 0 {
		overlap = DefaultOverlap
	}
	// overlap >= chunkSize 会导致合并逻辑异常，钳制为 chunkSize/2（对齐 langchain 校验语义，L13）
	if overlap >= chunkSize {
		overlap = chunkSize / 2
	}

	// 递归切分并合并为 chunks（splitText 内部已调用 mergeSplits）
	pieces := splitText(text, separators, chunkSize, overlap)

	chunks := make([]DocumentChunk, 0, len(pieces))
	for index, piece := range pieces {
		meta := make(map[string]any, len(parsed.Metadata)+4)
		for k, v := range parsed.Metadata {
			meta[k] = v
		}
		if documentID != "" {
			meta["document_id"] = documentID
		}
		if knowledgeBaseID > 0 {
			meta["knowledge_base_id"] = knowledgeBaseID
		}
		for _, mw := range middlewares {
			meta["mw_"+mw] = true
		}
		meta["chunk_index"] = index
		chunks = append(chunks, DocumentChunk{
			Text:       piece,
			ChunkIndex: index,
			Metadata:   meta,
		})
	}
	return chunks
}

// splitText 递归切分文本并合并为 chunks，对齐 langchain _split_text。
//
// 算法：
//  1. 找到 separators 中第一个在 text 中出现的分隔符（空串视为字符级兜底）。
//  2. 用该分隔符切分 text，keep_separator=true：separator 附加到下一个 split 开头，
//     对齐 langchain _split_text_with_keep_separator（[splits[0]] + [sep+s for s in splits[1:]]）。
//  3. 长度 < chunkSize 的 split 累积到 goodSplits；超长 split 先合并 goodSplits 再递归切分。
//  4. goodSplits 合并调用 mergeSplits，保证 overlap 与 chunkSize 约束。
func splitText(text string, separators []string, chunkSize, overlap int) []string {
	if len(text) == 0 || len(separators) == 0 {
		return []string{text}
	}

	// 找到第一个在文本中出现的分隔符
	separator := separators[len(separators)-1]
	var newSeparators []string
	for i, sep := range separators {
		if sep == "" {
			separator = sep
			break
		}
		if strings.Contains(text, sep) {
			separator = sep
			newSeparators = separators[i+1:]
			break
		}
	}

	// 切分文本，keep_separator=true：separator 附加到下一个 split 开头
	// 对齐 langchain _split_text_with_keep_separator
	var splits []string
	if separator == "" {
		// 字符级兜底：每个 rune 独立成 split
		for _, r := range text {
			splits = append(splits, string(r))
		}
	} else {
		parts := strings.Split(text, separator)
		// 首个 split 不带 separator
		if len(parts) > 0 && parts[0] != "" {
			splits = append(splits, parts[0])
		}
		// 后续 splits 每个开头附加 separator（即使 parts[i] 为空也保留 separator）
		for i := 1; i < len(parts); i++ {
			splits = append(splits, separator+parts[i])
		}
	}

	// 合并 < chunkSize 的 splits，超长的递归切分
	var finalSplits []string
	var goodSplits []string
	for _, s := range splits {
		if runeLen(s) < chunkSize {
			goodSplits = append(goodSplits, s)
		} else {
			if len(goodSplits) > 0 {
				finalSplits = append(finalSplits, mergeSplits(goodSplits, chunkSize, overlap)...)
				goodSplits = nil
			}
			if len(newSeparators) == 0 {
				finalSplits = append(finalSplits, s)
			} else {
				finalSplits = append(finalSplits, splitText(s, newSeparators, chunkSize, overlap)...)
			}
		}
	}
	if len(goodSplits) > 0 {
		finalSplits = append(finalSplits, mergeSplits(goodSplits, chunkSize, overlap)...)
	}
	return finalSplits
}

// mergeSplits 将 splits 合并为不超过 chunkSize 的 chunks，相邻 chunk 共享 overlap 字符。
// 对齐 langchain _merge_splits。
//
// 算法：
//  1. 累积 splits 到 currentDoc，加入 d 后若超过 chunkSize 则形成 chunk。
//  2. 形成 chunk 后，从 currentDoc 头部逐个移除 split，直到 total <= overlap 或能放下 d。
//  3. 继续累积 d 到 currentDoc。
//
// separator 在 langchain 中用于 join splits，这里因为 splitText 已保留分隔符作为独立 split，
// 直接拼接即可，不需要额外 separator。
func mergeSplits(splits []string, chunkSize, overlap int) []string {
	var docs []string
	var currentDoc []string
	total := 0
	for _, d := range splits {
		dLen := runeLen(d)
		// 检查加入 d 后是否超过 chunkSize
		if total+dLen > chunkSize && len(currentDoc) > 0 {
			// 形成 chunk
			doc := strings.TrimSpace(strings.Join(currentDoc, ""))
			if doc != "" {
				docs = append(docs, doc)
			}
			// 处理 overlap：从 currentDoc 头部移除，直到 total <= overlap 或能放下 d
			for len(currentDoc) > 0 && (total > overlap || (total+dLen > chunkSize && total > 0)) {
				removedLen := runeLen(currentDoc[0])
				total -= removedLen
				currentDoc = currentDoc[1:]
			}
		}
		currentDoc = append(currentDoc, d)
		total += dLen
	}
	if len(currentDoc) > 0 {
		doc := strings.TrimSpace(strings.Join(currentDoc, ""))
		if doc != "" {
			docs = append(docs, doc)
		}
	}
	return docs
}
