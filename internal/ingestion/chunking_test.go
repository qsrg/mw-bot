// File chunking_test.go: 递归字符分块单元测试，对齐 Python test_chunking.py。
// 期望值与 langchain RecursiveCharacterTextSplitter 实际输出一致。
package ingestion

import (
	"testing"
)

// TestChunkTextPreservesMetadata 验证分块保留来源元数据与 chunk 序号。
func TestChunkTextPreservesMetadata(t *testing.T) {
	parsed := ParsedDocument{
		Text:     "第一段内容。\n第二段内容。",
		Metadata: map[string]any{"file_name": "a.md"},
	}
	chunks := ChunkText(parsed, 8, 2, "", 0, nil, nil)
	if len(chunks) < 2 {
		t.Fatalf("期望至少 2 块, 实际 %d", len(chunks))
	}
	if chunks[0].Metadata["file_name"] != "a.md" {
		t.Errorf("file_name 期望 a.md, 实际 %v", chunks[0].Metadata["file_name"])
	}
	if chunks[0].ChunkIndex != 0 {
		t.Errorf("首块 chunk_index 期望 0, 实际 %d", chunks[0].ChunkIndex)
	}
}

// TestChunkTextCarriesSourceIDs 验证分块携带 document_id 与 knowledge_base_id 来源元数据。
func TestChunkTextCarriesSourceIDs(t *testing.T) {
	parsed := ParsedDocument{
		Text:     repeatStr("abcdefgh", 200),
		Metadata: map[string]any{"file_name": "b.pdf"},
	}
	chunks := ChunkText(parsed, 800, 120, "d1", 1, nil, nil)
	if len(chunks) == 0 {
		t.Fatal("期望非空分块")
	}
	for i, chunk := range chunks {
		if chunk.Metadata["document_id"] != "d1" {
			t.Errorf("块 %d document_id 期望 d1, 实际 %v", i, chunk.Metadata["document_id"])
		}
		if chunk.Metadata["knowledge_base_id"] != int64(1) {
			t.Errorf("块 %d knowledge_base_id 期望 1, 实际 %v", i, chunk.Metadata["knowledge_base_id"])
		}
		if chunk.Metadata["file_name"] != "b.pdf" {
			t.Errorf("块 %d file_name 期望 b.pdf, 实际 %v", i, chunk.Metadata["file_name"])
		}
	}
}

// TestChunkTextCarriesMiddlewares 验证分块携带 mw_<name> 中间件元数据。
func TestChunkTextCarriesMiddlewares(t *testing.T) {
	parsed := ParsedDocument{
		Text:     repeatStr("abcdefgh", 200),
		Metadata: map[string]any{},
	}
	chunks := ChunkText(parsed, 800, 120, "d1", 1, []string{"rocketmq", "kafka"}, nil)
	if len(chunks) == 0 {
		t.Fatal("期望非空分块")
	}
	for i, chunk := range chunks {
		if v, ok := chunk.Metadata["mw_rocketmq"]; !ok || v != true {
			t.Errorf("块 %d mw_rocketmq 期望 true, 实际 %v", i, chunk.Metadata["mw_rocketmq"])
		}
		if v, ok := chunk.Metadata["mw_kafka"]; !ok || v != true {
			t.Errorf("块 %d mw_kafka 期望 true, 实际 %v", i, chunk.Metadata["mw_kafka"])
		}
	}
}

// TestChunkEmptyText 验证空文本返回空列表。
func TestChunkEmptyText(t *testing.T) {
	parsed := ParsedDocument{
		Text:     "   ",
		Metadata: map[string]any{"file_name": "c.md"},
	}
	if chunks := ChunkText(parsed, 0, 0, "", 0, nil, nil); len(chunks) != 0 {
		t.Errorf("空文本期望空列表, 实际 %v", chunks)
	}
}

// TestRecursiveAvoidsMidWordSplit 验证递归分块在句子边界切分，不会把词从中间截断。
// langchain 实际输出：["句子一。句子二", "。句子三。"]
func TestRecursiveAvoidsMidWordSplit(t *testing.T) {
	parsed := ParsedDocument{
		Text:     "句子一。句子二。句子三。",
		Metadata: map[string]any{},
	}
	chunks := ChunkText(parsed, 10, 0, "", 0, nil, nil)
	want := []string{"句子一。句子二", "。句子三。"}
	if len(chunks) != len(want) {
		t.Fatalf("期望 %d 块, 实际 %d", len(want), len(chunks))
	}
	for i, w := range want {
		if chunks[i].Text != w {
			t.Errorf("块 %d 期望 %q, 实际 %q", i, w, chunks[i].Text)
		}
	}
	// 每个句子词必须完整出现在某一块中
	joined := ""
	for _, c := range chunks {
		joined += c.Text + "\n"
	}
	for _, word := range []string{"句子一", "句子二", "句子三"} {
		if !containsStr(joined, word) {
			t.Errorf("句子词 %q 应完整出现在分块中", word)
		}
	}
}

// TestRecursiveOverlapBetweenChunks 验证相邻块保留 overlap 字符重叠。
// langchain 实际输出：["句子一。句子二", "。句子二。句子三", "。句子三。句子四", "。句子四。句子五。"]
func TestRecursiveOverlapBetweenChunks(t *testing.T) {
	parsed := ParsedDocument{
		Text:     "句子一。句子二。句子三。句子四。句子五。",
		Metadata: map[string]any{},
	}
	chunks := ChunkText(parsed, 10, 4, "", 0, nil, nil)
	want := []string{
		"句子一。句子二",
		"。句子二。句子三",
		"。句子三。句子四",
		"。句子四。句子五。",
	}
	if len(chunks) != len(want) {
		t.Fatalf("期望 %d 块, 实际 %d (%v)", len(want), len(chunks), chunkTexts(chunks))
	}
	for i, w := range want {
		if chunks[i].Text != w {
			t.Errorf("块 %d 期望 %q, 实际 %q", i, w, chunks[i].Text)
		}
	}
	// 后一块的开头是前一块的尾部子串（overlap=4 字符）
	for i := 1; i < len(chunks); i++ {
		prev := chunks[i-1].Text
		curr := chunks[i].Text
		prevRunes := []rune(prev)
		if len(prevRunes) < 4 {
			continue
		}
		tail := string(prevRunes[len(prevRunes)-4:])
		currRunes := []rune(curr)
		currHead := string(currRunes[:min(4, len(currRunes))])
		if !startsWithStr(curr, tail) {
			t.Errorf("块 %d 开头应与块 %d 尾部重叠, 期望前缀 %q, 实际 %q", i, i-1, tail, currHead)
		}
	}
}

// TestRecursiveSplitsLongParagraphOnSentence 验证单段超长文本在句号边界递归切分。
// langchain 实际输出：["句子一。句子二", "。句子三。句子四。"]
func TestRecursiveSplitsLongParagraphOnSentence(t *testing.T) {
	parsed := ParsedDocument{
		Text:     "句子一。句子二。句子三。句子四。",
		Metadata: map[string]any{},
	}
	chunks := ChunkText(parsed, 10, 0, "", 0, nil, nil)
	want := []string{"句子一。句子二", "。句子三。句子四。"}
	if len(chunks) != len(want) {
		t.Fatalf("期望 %d 块, 实际 %d (%v)", len(want), len(chunks), chunkTexts(chunks))
	}
	for i, w := range want {
		if chunks[i].Text != w {
			t.Errorf("块 %d 期望 %q, 实际 %q", i, w, chunks[i].Text)
		}
	}
}

// TestRecursivePrefersParagraphOverSentence 验证两段均较短时按段落分隔符合并为一块。
func TestRecursivePrefersParagraphOverSentence(t *testing.T) {
	parsed := ParsedDocument{
		Text:     "第一段内容。\n\n第二段内容。",
		Metadata: map[string]any{},
	}
	chunks := ChunkText(parsed, 20, 0, "", 0, nil, nil)
	if len(chunks) != 1 {
		t.Fatalf("期望 1 块, 实际 %d (%v)", len(chunks), chunkTexts(chunks))
	}
	if !containsStr(chunks[0].Text, "第一段内容") {
		t.Errorf("应包含 '第一段内容', 实际 %q", chunks[0].Text)
	}
	if !containsStr(chunks[0].Text, "第二段内容") {
		t.Errorf("应包含 '第二段内容', 实际 %q", chunks[0].Text)
	}
}

// TestCustomSeparators 验证可通过 separators 自定义分隔符优先级。
// langchain 实际输出：["a;b;c", ";d;e", ";f"]
func TestCustomSeparators(t *testing.T) {
	parsed := ParsedDocument{
		Text:     "a;b;c;d;e;f",
		Metadata: map[string]any{},
	}
	chunks := ChunkText(parsed, 5, 0, "", 0, nil, []string{";", ""})
	want := []string{"a;b;c", ";d;e", ";f"}
	if len(chunks) != len(want) {
		t.Fatalf("期望 %d 块, 实际 %d (%v)", len(want), len(chunks), chunkTexts(chunks))
	}
	for i, w := range want {
		if chunks[i].Text != w {
			t.Errorf("块 %d 期望 %q, 实际 %q", i, w, chunks[i].Text)
		}
	}
}

// TestChunkTextDefaultsApplied 验证 chunkSize<=0 与 overlap<0 时应用默认值。
func TestChunkTextDefaultsApplied(t *testing.T) {
	parsed := ParsedDocument{
		Text:     repeatStr("句子一。", 300), // 1200 字符，超过默认 chunkSize=800
		Metadata: map[string]any{},
	}
	// chunkSize=0 用默认 800, overlap=-1 用默认 120
	chunks := ChunkText(parsed, 0, -1, "", 0, nil, nil)
	if len(chunks) < 2 {
		t.Fatalf("期望至少 2 块, 实际 %d", len(chunks))
	}
	// 验证每块不超过默认 800 字符
	for i, c := range chunks {
		if len([]rune(c.Text)) > 800 {
			t.Errorf("块 %d 长度 %d 超过默认 800", i, len([]rune(c.Text)))
		}
	}
}

// TestChunkTextChunkIndexSequential 验证 chunk_index 从 0 顺序递增。
func TestChunkTextChunkIndexSequential(t *testing.T) {
	parsed := ParsedDocument{
		Text:     repeatStr("句子一。句子二。", 100),
		Metadata: map[string]any{},
	}
	chunks := ChunkText(parsed, 20, 0, "", 0, nil, nil)
	for i, c := range chunks {
		if c.ChunkIndex != i {
			t.Errorf("块 %d chunk_index 期望 %d, 实际 %d", i, i, c.ChunkIndex)
		}
		if v, ok := c.Metadata["chunk_index"]; !ok || v != i {
			t.Errorf("块 %d metadata chunk_index 期望 %d, 实际 %v", i, i, c.Metadata["chunk_index"])
		}
	}
}

// repeatStr 重复字符串 n 次。
func repeatStr(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

// containsStr 检查字符串是否包含子串。
func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

// indexOf 返回子串首次出现位置，未找到返回 -1。
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// startsWithStr 检查字符串是否以指定前缀开头。
func startsWithStr(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// chunkTexts 提取分块的文本切片，用于错误信息展示。
func chunkTexts(chunks []DocumentChunk) []string {
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = c.Text
	}
	return out
}

// min 返回两整数较小值。
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
