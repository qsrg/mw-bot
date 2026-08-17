// File service.go: RAG 编排核心，对齐 Python app/rag/service.py。
//
// 串联意图检测 → 检索（混合/纯向量）→ 跨中间件歧义反问 → 工具循环 → 答案生成。
// 同步入口 Answer 与流式入口 AnswerStream 共用 prepare() 检索与拼 prompt 流程。
package rag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"sort"
	"strings"

	"mw-bot/internal/common"
)

// CitationItem 引用来源项，与 Python chat/schemas.CitationItem 对齐。
// document_id/chunk_id/file_name 可为空（JSON 输出 null），与 Python 行为一致。
type CitationItem struct {
	DocumentID *string `json:"document_id"` // 文档对外标识 uuid（可空）
	ChunkID    *string `json:"chunk_id"`    // 分块标识（可空）
	FileName   *string `json:"file_name"`   // 文件名（可空）
	Score      float64 `json:"score"`       // 相关性分数
	Snippet    string  `json:"snippet"`     // 摘要片段（默认空串）
}

// RagAnswer RAG 答案，含内容、引用、模型推断标识与工具调用记录。
// 与 Python RagAnswer dataclass 对齐。
type RagAnswer struct {
	Content            string           `json:"content"`              // 助手回答文本
	Citations          []CitationItem   `json:"citations"`            // 引用列表
	UsedModelInference bool             `json:"used_model_inference"` // 是否模型推断（无引用且无工具结果）
	ToolCalls          []map[string]any `json:"tool_calls"`           // 工具调用记录（tool/arguments/result）
}

// ToolDef 工具定义，传给 LLM 决策用。
type ToolDef struct {
	Name        string         `json:"name"`         // 工具名
	Description string         `json:"description"`  // 描述
	InputSchema map[string]any `json:"input_schema"` // 输入 schema
}

// ToolExecutor 工具执行回调：按工具名+参数调用 MCP 网关，返回 output。
type ToolExecutor func(ctx context.Context, toolName string, arguments map[string]any) (map[string]any, error)

// StreamEvent 流式事件，Type 取值 meta/reasoning/delta/done。
// 不同 Type 使用不同字段，未使用字段不输出（omitempty）。
type StreamEvent struct {
	Type               string           `json:"type"`                           // 事件类型：meta/reasoning/delta/done
	Citations          []CitationItem   `json:"citations,omitempty"`            // meta：引用列表
	UsedModelInference bool             `json:"used_model_inference,omitempty"` // meta/done：是否模型推断
	ToolCalls          []map[string]any `json:"tool_calls,omitempty"`           // meta：工具调用记录
	Text               string           `json:"text,omitempty"`                 // reasoning/delta：文本片段
	Content            string           `json:"content,omitempty"`              // done：完整内容
}

// StreamEmitter 流式事件发射回调。返回 error 中止流。
type StreamEmitter func(event StreamEvent) error

// RagService 检索增强生成服务。
type RagService struct {
	embedding common.EmbeddingProvider // Embedding 提供者
	vector    common.VectorStore       // 向量库
	llm       common.LLMProvider       // LLM 提供者
	settings  common.Settings          // 应用配置
}

// NewRagService 创建 RAG 服务。
//
// 参数：
//   - embedding: Embedding 提供者，用于问题向量化。
//   - vector: 向量库实例，用于检索。
//   - llm: LLM 提供者，用于意图判定、工具决策、答案生成与摘要。
//   - settings: 应用配置（提供 HybridSearch/RetrieveLimit 等检索参数）。
func NewRagService(
	embedding common.EmbeddingProvider,
	vector common.VectorStore,
	llm common.LLMProvider,
	settings common.Settings,
) *RagService {
	return &RagService{
		embedding: embedding,
		vector:    vector,
		llm:       llm,
		settings:  settings,
	}
}

// LLMProvider 暴露 LLM 提供者，供 ChatService 复用做会话摘要与记忆提取。
func (s *RagService) LLMProvider() common.LLMProvider { return s.llm }

// Retrieve 检索知识库相关片段（开启混合检索时走 dense+sparse RRF 融合）。
// 流程：问题文本 → embedding 向量化 → 向量库检索 → top-K 片段。
//
// 参数：
//   - ctx: 请求上下文。
//   - question: 用户问题文本。
//   - limit: 返回的最大片段数，<=0 用配置默认值。
//   - filters: 元数据等值过滤（如 {"mw_rocketmq":"true"} 按中间件过滤），nil 不过滤。
//
// 返回：
//   - []common.SearchResult: 与问题相关的知识库片段列表，按相关性降序。
//   - error: embedding 或检索失败。
func (s *RagService) Retrieve(ctx context.Context, question string, limit int, filters map[string]string) ([]common.SearchResult, error) {
	if limit <= 0 {
		limit = s.settings.RetrieveLimit
	}
	embeddings, err := s.embedding.Embed(ctx, []string{question})
	if err != nil {
		return nil, fmt.Errorf("embed question: %w", err)
	}
	if len(embeddings) == 0 {
		return nil, fmt.Errorf("embedding returned empty result")
	}
	return s.search(ctx, question, embeddings[0], filters, limit)
}

// search 按已向量化的问题检索知识库（混合/纯向量）。
// score_threshold 按余弦相似度在融合前过滤无关片段（作用于 dense 路）。
// 供 retrieve 与 prepare 的逐中间件歧义检测复用，避免重复 embedding 调用。
func (s *RagService) search(ctx context.Context, question string, queryEmbedding []float32, filters map[string]string, limit int) ([]common.SearchResult, error) {
	ft := filters
	if ft == nil {
		ft = map[string]string{}
	}
	if s.settings.HybridSearch {
		return s.vector.HybridSearch(ctx, question, queryEmbedding, ft, limit, s.settings.RetrieveScoreThreshold)
	}
	return s.vector.SimilaritySearch(ctx, queryEmbedding, ft, limit, s.settings.RetrieveScoreThreshold)
}

// buildCitations 将检索结果拼装为前端展示用的引用结构（文件名、分数、摘要）。
// 按 file_name 去重：results 已按相关性排序，每篇文档只保留得分最高的片段。
func (s *RagService) buildCitations(results []common.SearchResult) []CitationItem {
	citations := make([]CitationItem, 0, len(results))
	seen := make(map[string]bool, len(results))
	for _, item := range results {
		name := item.Metadata["file_name"]
		// 按 file_name 去重（缺失视为 ""，仅保留首个无文件名片段，对齐 Python 用 None 作 key，M14）
		if seen[name] {
			continue
		}
		seen[name] = true
		citation := CitationItem{
			Score:   item.Score,
			Snippet: truncate(item.Text, 300),
		}
		// document_id / chunk_id / file_name 可空，与 Python None 对齐
		if v, ok := item.Metadata["document_id"]; ok && v != "" {
			vv := v
			citation.DocumentID = &vv
		}
		chunkID := item.Metadata["chunk_id"]
		if chunkID == "" {
			if idx, ok := item.Metadata["chunk_index"]; ok && idx != "" {
				chunkID = idx
			}
		}
		if chunkID != "" {
			v := chunkID
			citation.ChunkID = &v
		}
		if name != "" {
			v := name
			citation.FileName = &v
		}
		citations = append(citations, citation)
	}
	return citations
}

// truncate 截断字符串到指定长度，避免引用摘要过长。
func truncate(s string, n int) string {
	// 按 rune 截断，避免截断中文字符产生乱码
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// dedup 按片段文本内容去重，避免同一文档重复上传导致重复引用。
// 保留首次出现的顺序。
func dedup(results []common.SearchResult) []common.SearchResult {
	unique := make([]common.SearchResult, 0, len(results))
	seen := make(map[string]bool, len(results))
	for _, item := range results {
		key := strings.TrimSpace(item.Text)
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, item)
	}
	return unique
}

// Rerank 对召回片段重排序（cross-encoder 精排），提升注入 LLM 的片段排序精度。
//
// TODO: 接入 reranker 模型（如 bge-reranker 或模型网关的 rerank 接口）。
// 当前为透传占位，直接返回原顺序，后续实现。
func (s *RagService) Rerank(_ string, results []common.SearchResult) []common.SearchResult {
	return results
}

// classifyIntent 分类用户问题意图：knowledge / followup / chat。
//   - knowledge：知识提问，检索后回答。
//   - followup：对上一条回答的指令（重新回答/详细点/简短点），用上一条问题检索后重答，带引用。
//   - chat：身份询问、能力询问、闲聊、风格要求，直接回答不检索。
//
// 规则快速通道始终生效（免 LLM）。未命中规则时：intent_detection=true 再调 LLM
// 精判；false 则默认按 knowledge 处理。LLM 调用或解析失败时保守返回 knowledge。
func (s *RagService) classifyIntent(ctx context.Context, question string) string {
	if rule := ruleIntent(question); rule != "" {
		return rule
	}
	if !s.settings.IntentDetection {
		return "knowledge"
	}
	messages := []common.Message{
		{Role: "system", Content: intentSystemPrompt},
		{Role: "user", Content: "问题：" + question},
	}
	raw, err := s.llm.Chat(ctx, messages)
	if err != nil {
		slog.WarnContext(ctx, "意图判定调用失败，按知识提问处理", "error", err)
		return "knowledge"
	}
	intent := parseIntent(raw)
	if intent != "knowledge" && intent != "followup" && intent != "chat" {
		return "knowledge"
	}
	return intent
}

// ruleIntent 明显短句的规则快速通道，返回意图或空串（需 LLM 判定）。
// 仅精确匹配，避免前缀匹配误伤"详细点介绍X"类知识提问。
func ruleIntent(question string) string {
	q := strings.TrimSpace(question)
	if q == "" {
		return "chat"
	}
	ql := strings.ToLower(q)
	followup := map[string]bool{
		"重新回答": true, "再答一次": true, "再说一遍": true, "详细点": true, "详细一些": true,
		"简短点": true, "简短一些": true, "简洁点": true, "再解释一下": true, "换个说法": true,
	}
	chat := map[string]bool{
		"你是谁": true, "你好": true, "在吗": true, "谢谢": true, "感谢": true, "多谢": true,
		"好的": true, "收到": true, "嗯": true, "ok": true,
		"言简意赅": true, "不要客套话": true, "不要使用客套话": true,
	}
	if followup[ql] {
		return "followup"
	}
	if chat[ql] {
		return "chat"
	}
	// "你是…" 开头的身份/能力询问（你是谁/你是X专家吗/你是AI吗）视为 chat
	if strings.HasPrefix(q, "你是") {
		return "chat"
	}
	return ""
}

// lastUserQuestion 从会话历史取最后一条 user 消息，供 followup 指令检索复用。
// 无历史或无 user 消息返回空串。
func lastUserQuestion(history []common.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" {
			return history[i].Content
		}
	}
	return ""
}

// intentJSONRE 从 LLM 输出提取首个 JSON 对象（intent）。
var intentJSONRE = regexp.MustCompile(`(?s)\{.*?\}`)

// parseIntent 从 LLM 文本提取意图 JSON，返回 intent 值；解析失败返回空串。
func parseIntent(raw string) string {
	match := intentJSONRE.FindString(raw)
	if match == "" {
		return ""
	}
	var data struct {
		Intent string `json:"intent"`
	}
	if err := json.Unmarshal([]byte(match), &data); err != nil {
		return ""
	}
	return data.Intent
}

// extractClarificationCandidates 从会话历史最后一条 assistant 反问消息中抽取候选中间件名列表。
//
// 用于识别"上一轮刚反问过中间件歧义"的上下文：若上一轮是反问消息，
// 本轮用户回复应视为澄清，即使输入有错别字也不应再次反问。
//
// 返回候选中间件名列表（小写）；最后一条非反问消息或无历史返回 nil。
func extractClarificationCandidates(history []common.Message) []string {
	if len(history) == 0 {
		return nil
	}
	last := history[len(history)-1]
	if last.Role != "assistant" {
		return nil
	}
	content := last.Content
	if !strings.HasPrefix(content, clarificationPrefix) || !strings.HasSuffix(content, clarificationSuffix) {
		return nil
	}
	inner := content[len(clarificationPrefix) : len(content)-len(clarificationSuffix)]
	candidates := []string{}
	for _, mw := range strings.Split(inner, "、") {
		mw = strings.TrimSpace(strings.ToLower(mw))
		if mw != "" {
			candidates = append(candidates, mw)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	return candidates
}

// prepareResult prepare 方法的返回值，含 messages/citations/标志与反问候选。
type prepareResult struct {
	Messages           []common.Message // 拼装好的 LLM 消息列表
	Citations          []CitationItem   // 引用列表
	UsedModelInference bool             // 是否模型推断
	HasToolResults     bool             // 是否有工具结果
	Clarification      []string         // 反问候选中间件（非 nil 表示需反问）
}

// prepare 检索、去重、重排、构建引用与提示消息，供同步与流式生成共用。
//
// 这是 RAG 的核心编排方法，串联检索->去重->重排->拼 prompt 全流程：
//  1. search() - 混合检索召回 top-K 候选片段
//  2. dedup() - 按文本去重，避免重复上传导致重复引用
//  3. Rerank() - cross-encoder 精排重序（当前透传占位，待接入 reranker）
//  4. buildCitations() - 构建前端展示用的引用信息
//
// 参数：
//   - ctx: 请求上下文。
//   - question: 用户问题。
//   - history: 近期会话历史，用于续接多轮。
//   - memories: 用户启用的长期记忆内容列表。
//   - toolResults: agent loop 已执行的工具结果描述块列表。
//
// 返回：
//   - *prepareResult: 拼装好的消息、引用、推断标识与反问候选。
//   - error: embedding 或检索失败。
func (s *RagService) prepare(
	ctx context.Context,
	question string,
	history []common.Message,
	memories []string,
	toolResults []string,
) (*prepareResult, error) {
	// ── 意图检测 ──
	// has_tool_results 时已由 agent loop 判定为动作查询，跳过意图检测
	hasToolResults := len(toolResults) > 0
	intent := "knowledge"
	if !hasToolResults {
		intent = s.classifyIntent(ctx, question)
	}

	citations := []CitationItem{}
	knowledgeBlock := ""
	usedModelInference := false
	var clarification []string

	if intent != "chat" {
		// knowledge 或 followup：检索知识库
		retrieveQ := question
		if intent == "followup" {
			// followup 指令（重新回答/详细点）针对上一条问题，用上一条 user 问题检索
			if q := lastUserQuestion(history); q != "" {
				retrieveQ = q
			}
		}

		// 中间件处理：识别提问涉及的中间件
		qMws := common.DetectMiddlewares(question)
		filters := map[string]string{}
		// 反问上下文：上一轮若为跨中间件歧义反问，本轮可能为澄清回复。
		clarificationCandidates := extractClarificationCandidates(history)
		matchedClarificationMw := ""
		if intent == "knowledge" && len(qMws) == 0 && len(clarificationCandidates) > 0 {
			matchedClarificationMw = common.MatchCandidate(question, clarificationCandidates)
		}
		if intent == "knowledge" && len(qMws) == 1 {
			mw := qMws[0]
			// 反问后的澄清回复：消息基本只是中间件名 + 有历史 -> 用上一条问题+该中间件检索
			remainder := strings.TrimSpace(strings.ReplaceAll(strings.ToLower(question), mw, ""))
			if len(history) > 0 && len(remainder) <= 2 {
				if q := lastUserQuestion(history); q != "" {
					retrieveQ = q
				}
			}
			// 提问明确指定单个中间件 -> 按该中间件过滤检索
			filters["mw_"+mw] = "true"
		} else if intent == "knowledge" && len(qMws) == 0 && matchedClarificationMw != "" {
			// 反问后澄清回复且错别字容错匹配命中候选：
			// 用上一条用户问题 + 候选中间件过滤检索，绝不再触发反问。
			if q := lastUserQuestion(history); q != "" {
				retrieveQ = q
			}
			filters["mw_"+matchedClarificationMw] = "true"
		}

		// 复用同一 embedding 做逐中间件歧义检测与主检索，避免重复向量化
		embeddings, err := s.embedding.Embed(ctx, []string{retrieveQ})
		if err != nil {
			return nil, fmt.Errorf("embed retrieve question: %w", err)
		}
		if len(embeddings) == 0 {
			return nil, fmt.Errorf("embedding returned empty result")
		}
		queryEmbedding := embeddings[0]

		// 跨中间件歧义：提问未指定中间件时，逐个中间件过滤检索看是否有命中。
		// 反问上下文下不再触发反问（无论是否匹配候选），避免反复追问用户。
		if intent == "knowledge" && len(qMws) == 0 && len(clarificationCandidates) == 0 {
			relevantMws := []string{}
			for _, mw := range common.MiddlewareList() {
				hits, err := s.search(ctx, retrieveQ, queryEmbedding, map[string]string{"mw_" + mw: "true"}, 3)
				if err != nil {
					slog.WarnContext(ctx, "逐中间件检索失败", "middleware", mw, "error", err)
					continue
				}
				if len(hits) > 0 {
					relevantMws = append(relevantMws, mw)
				}
			}
			if len(relevantMws) >= 2 {
				clarification = sortedStrings(relevantMws)
			}
		}

		if clarification == nil {
			results, err := s.search(ctx, retrieveQ, queryEmbedding, filters, s.settings.RetrieveLimit)
			if err != nil {
				return nil, fmt.Errorf("search knowledge base: %w", err)
			}
			reranked := s.Rerank(retrieveQ, dedup(results))
			if len(reranked) > s.settings.RerankTopN {
				reranked = reranked[:s.settings.RerankTopN]
			}
			citations = s.buildCitations(reranked)
			context := joinTexts(reranked, "\n\n")
			// 无引用且无工具结果时，LLM 只能靠自身知识回答，标记为模型推断
			usedModelInference = len(citations) == 0 && !hasToolResults
			if context != "" {
				knowledgeBlock = "知识库片段:\n" + context
			} else {
				knowledgeBlock = "知识库片段:（无相关内容）"
			}
		}
	}
	// chat（身份/闲聊/风格）：不检索、不注入知识片段、无引用、不算模型推断

	// ── 拼装 user message：长期记忆 + 工具结果 + 知识片段 + 问题 ──
	memoryBlock := ""
	if len(memories) > 0 {
		lines := make([]string, 0, len(memories))
		for _, item := range memories {
			lines = append(lines, "- "+item)
		}
		memoryBlock = "用户长期记忆:\n" + strings.Join(lines, "\n") + "\n\n"
	}
	toolBlock := ""
	if len(toolResults) > 0 {
		toolBlock = "工具调用结果:\n" + strings.Join(toolResults, "\n\n") + "\n\n"
	}

	// 顺序：system prompt → 历史对话 → 当前 user message
	messages := make([]common.Message, 0, 2+len(history))
	messages = append(messages, common.Message{Role: "system", Content: systemPrompt})
	messages = append(messages, history...)
	messages = append(messages, common.Message{
		Role:    "user",
		Content: memoryBlock + toolBlock + knowledgeBlock + "\n\n用户问题:\n" + question,
	})

	return &prepareResult{
		Messages:           messages,
		Citations:          citations,
		UsedModelInference: usedModelInference,
		HasToolResults:     hasToolResults,
		Clarification:      clarification,
	}, nil
}

// joinTexts 用分隔符拼接多个片段文本，供 knowledge_block 拼装复用。
func joinTexts(results []common.SearchResult, sep string) string {
	if len(results) == 0 {
		return ""
	}
	parts := make([]string, 0, len(results))
	for _, r := range results {
		parts = append(parts, r.Text)
	}
	return strings.Join(parts, sep)
}

// sortedStrings 返回排序后的字符串切片副本。
func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// toolDecisionJSONRE 从 LLM 工具决策输出提取首个 JSON 对象。
var toolDecisionJSONRE = regexp.MustCompile(`(?s)\{.*\}`)

// decideToolCall 非流式让 LLM 决定是否调用工具，返回 {tool, arguments} 或 nil。
// 决策调用失败或输出不可解析时返回 nil（按无工具处理，不阻塞回答）。
func (s *RagService) decideToolCall(
	ctx context.Context,
	question string,
	tools []ToolDef,
	history []common.Message,
	priorResults []string,
) map[string]any {
	// 拼装工具列表行
	toolLines := make([]string, 0, len(tools))
	for _, t := range tools {
		schemaJSON, _ := json.Marshal(t.InputSchema)
		toolLines = append(toolLines, fmt.Sprintf("- %s: %s 参数: %s", t.Name, t.Description, string(schemaJSON)))
	}
	priorBlock := ""
	if len(priorResults) > 0 {
		priorBlock = "已获取的工具结果:\n" + strings.Join(priorResults, "\n\n") + "\n\n"
	}
	// 替换 system prompt 占位符
	sysPrompt := strings.Replace(toolDecisionSystemPrompt, "{tool_lines}", strings.Join(toolLines, "\n"), 1)
	messages := []common.Message{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: priorBlock + "用户问题：" + question},
	}
	raw, err := s.llm.Chat(ctx, messages)
	if err != nil {
		slog.WarnContext(ctx, "工具决策调用失败，按无工具处理", "error", err)
		return nil
	}
	return parseToolDecision(raw, tools)
}

// parseToolDecision 从 LLM 文本中提取工具调用 JSON，校验工具名存在与参数类型。
func parseToolDecision(raw string, tools []ToolDef) map[string]any {
	match := toolDecisionJSONRE.FindString(raw)
	if match == "" {
		return nil
	}
	var data struct {
		NeedTool  bool           `json:"need_tool"`
		Tool      string         `json:"tool"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(match), &data); err != nil {
		return nil
	}
	if !data.NeedTool {
		return nil
	}
	if data.Tool == "" {
		return nil
	}
	// 校验工具名存在
	found := false
	for _, t := range tools {
		if t.Name == data.Tool {
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	if data.Arguments == nil {
		data.Arguments = map[string]any{}
	}
	return map[string]any{
		"tool":      data.Tool,
		"arguments": data.Arguments,
	}
}

// RunToolLoop 循环决策-执行工具，返回 (工具结果描述块列表, tool_calls 记录)。
//
// 单轮最多调用 MAX_TOOL_ITERATIONS 次工具；执行异常记为错误结果，不中断。
// 由 ChatService 在持久化会话仍可用时调用，避免流式阶段 session 已关闭。
func (s *RagService) RunToolLoop(
	ctx context.Context,
	question string,
	tools []ToolDef,
	executor ToolExecutor,
	history []common.Message,
) ([]string, []map[string]any) {
	resultBlocks := []string{}
	toolCalls := []map[string]any{}
	prior := []string{}
	for i := 0; i < MAX_TOOL_ITERATIONS; i++ {
		decision := s.decideToolCall(ctx, question, tools, history, prior)
		if decision == nil {
			break
		}
		toolName, _ := decision["tool"].(string)
		arguments, _ := decision["arguments"].(map[string]any)
		if arguments == nil {
			arguments = map[string]any{}
		}
		var output map[string]any
		var resultText string
		output, err := executor(ctx, toolName, arguments)
		if err != nil {
			slog.WarnContext(ctx, "工具执行失败", "tool", toolName, "error", err)
			output = map[string]any{"error": err.Error()}
			resultText = "工具调用失败: " + err.Error()
		} else {
			b, _ := json.Marshal(output)
			resultText = string(b)
		}
		argsJSON, _ := json.Marshal(arguments)
		block := fmt.Sprintf("工具 %s（参数 %s）返回:\n%s", toolName, string(argsJSON), resultText)
		resultBlocks = append(resultBlocks, block)
		prior = append(prior, block)
		toolCalls = append(toolCalls, map[string]any{
			"tool":      toolName,
			"arguments": arguments,
			"result":    output,
		})
	}
	return resultBlocks, toolCalls
}

// Answer 基于知识库检索结果生成答案，并返回引用与推断标识。
//
// toolResults 为 agent loop 已执行的工具结果描述块，与知识库一起喂给 LLM。
// 跨中间件歧义时不调 LLM，直接返回模板反问。
// 模型网关异常时降级为兜底回复，避免问答整体 500。
func (s *RagService) Answer(
	ctx context.Context,
	question string,
	history []common.Message,
	memories []string,
	toolResults []string,
	toolCalls []map[string]any,
) (*RagAnswer, error) {
	prep, err := s.prepare(ctx, question, history, memories, toolResults)
	if err != nil {
		return nil, err
	}
	if len(prep.Clarification) > 0 {
		content := clarificationPrefix + strings.Join(prep.Clarification, "、") + clarificationSuffix
		return &RagAnswer{
			Content:            content,
			Citations:          []CitationItem{},
			UsedModelInference: false,
			ToolCalls:          toolCalls,
		}, nil
	}
	content := ""
	usedModelInference := prep.UsedModelInference
	raw, err := s.llm.Chat(ctx, prep.Messages)
	if err != nil {
		slog.WarnContext(ctx, "模型网关调用失败，降级为兜底回复", "error", err)
		usedModelInference = false
	} else {
		content = raw
	}
	if content == "" {
		content = FallbackAnswer
	}
	return &RagAnswer{
		Content:            content,
		Citations:          prep.Citations,
		UsedModelInference: usedModelInference,
		ToolCalls:          toolCalls,
	}, nil
}

// AnswerStream 流式生成答案：先产出引用元信息，再逐 token 产出文本片段。
//
// 事件序列：meta（引用/推断标识/工具调用）→ reasoning（推理模型思考链片段，多次，可选）
// → delta（正式回答文本片段，多次）→ done（完整内容）。
// 跨中间件歧义时不调 LLM，直接产出 delta（反问文案）+ done。
// 模型网关流式失败时降级为兜底文案，保证事件流始终以 done 结束。
func (s *RagService) AnswerStream(
	ctx context.Context,
	question string,
	history []common.Message,
	memories []string,
	toolResults []string,
	toolCalls []map[string]any,
	emit StreamEmitter,
) error {
	prep, err := s.prepare(ctx, question, history, memories, toolResults)
	if err != nil {
		return err
	}
	// meta 事件
	if err := emit(StreamEvent{
		Type:               "meta",
		Citations:          prep.Citations,
		UsedModelInference: prep.UsedModelInference,
		ToolCalls:          toolCalls,
	}); err != nil {
		return err
	}
	// 跨中间件歧义：不调 LLM，直接反问
	if len(prep.Clarification) > 0 {
		content := clarificationPrefix + strings.Join(prep.Clarification, "、") + clarificationSuffix
		if err := emit(StreamEvent{Type: "delta", Text: content}); err != nil {
			return err
		}
		return emit(StreamEvent{Type: "done", Content: content, UsedModelInference: false})
	}
	// 流式生成
	parts := []string{}
	usedModelInference := prep.UsedModelInference
	iter, err := s.llm.StreamChat(ctx, prep.Messages)
	if err != nil {
		slog.WarnContext(ctx, "模型网关流式调用失败，降级为兜底回复", "error", err)
		fallback := FallbackAnswer
		parts = append(parts, fallback)
		usedModelInference = false
		if err := emit(StreamEvent{Type: "delta", Text: fallback}); err != nil {
			return err
		}
	} else {
		for {
			kind, delta, iterErr := iter()
			if iterErr != nil {
				// io.EOF 正常结束；其他视为流式失败，降级兜底
				if errors.Is(iterErr, io.EOF) {
					break
				}
				slog.WarnContext(ctx, "模型网关流式调用失败，降级为兜底回复", "error", iterErr)
				fallback := FallbackAnswer
				parts = append(parts, fallback)
				usedModelInference = false
				if err := emit(StreamEvent{Type: "delta", Text: fallback}); err != nil {
					return err
				}
				break
			}
			if kind == "reasoning" {
				if err := emit(StreamEvent{Type: "reasoning", Text: delta}); err != nil {
					return err
				}
			} else {
				// content
				parts = append(parts, delta)
				if err := emit(StreamEvent{Type: "delta", Text: delta}); err != nil {
					return err
				}
			}
		}
	}
	return emit(StreamEvent{
		Type:               "done",
		Content:            strings.Join(parts, ""),
		UsedModelInference: usedModelInference,
	})
}
