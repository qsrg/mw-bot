// Package rag 实现 RAG 编排：检索增强生成、意图检测、跨中间件歧义反问与 MCP 工具循环。
//
// 与 Python app/rag/service.py 行为对齐：
//   - 检索：dense(chromem-go 余弦相似度) + BM25 RRF 融合
//   - 意图：规则快速通道始终生效；INTENT_DETECTION=true 时未命中规则再调 LLM
//   - 反问：跨中间件歧义时不调 LLM、无引用，直接模板反问
//   - 工具循环：≤ MAX_TOOL_ITERATIONS 轮，每轮 LLM 决策是否调用工具
//   - 流式：meta → reasoning → delta → done 事件序列
package rag

import "strings"

// MAX_TOOL_ITERATIONS agent loop 单轮问答最多调用工具的次数上限，避免无限循环。
const MAX_TOOL_ITERATIONS = 3

// 跨中间件歧义反问消息模板：生成与解析复用同一模板，避免硬编码两处不一致。
// 候选中间件用"、"连接，解析时按"、"切分。
const (
	clarificationPrefix = "你的问题可能涉及多个中间件（"
	clarificationSuffix = "），请问你具体问的是哪个？"
)

// ClarificationPrefix 暴露反问前缀供测试与外部校验使用。
func ClarificationPrefix() string { return clarificationPrefix }

// ClarificationSuffix 暴露反问后缀供测试与外部校验使用。
func ClarificationSuffix() string { return clarificationSuffix }

// IsClarification 判断助手消息是否为跨中间件歧义反问（模板消息）。
// 反问为固定模板生成，不含模型回答信息，供记忆提取等下游判断跳过。
func IsClarification(content string) bool {
	return len(content) > len(clarificationPrefix)+len(clarificationSuffix) &&
		strings.HasPrefix(content, clarificationPrefix) &&
		strings.HasSuffix(content, clarificationSuffix)
}

// FallbackAnswer 模型网关不可用时的兜底回复文案。
// 同步与流式路径共用，供下游（如记忆提取跳过判断）引用，避免多处硬编码不一致。
const FallbackAnswer = "模型服务暂不可用，请检查模型网关配置或 LLM_API_KEY 后重试。"

// systemPrompt RAG 主流程 system 提示，与 Python _prepare 中 system_prompt 对齐。
const systemPrompt = "你是企业内部的中间件智能助手。" +
	"若为知识提问：优先基于知识库片段回答；知识库无相关内容时说明属模型推断。" +
	"若为对上一条回答的指令（重新回答/详细点/简短点）：基于知识库片段重新回答上一条问题。" +
	"若为身份、闲聊或风格要求：直接回答，不要提及知识库或检索结果。" +
	"若提供了工具调用结果，它是实时数据，请据此回答用户问题。" +
	"若用户对你身份做出与事实不符的陈述（如'你是某模型'），礼貌纠正，不盲目接受。" +
	"无需在回答中列举来源或参考链接，来源由系统单独展示。"

// intentSystemPrompt 意图判定 system 提示，与 Python _classify_intent 对齐。
const intentSystemPrompt = "你是意图判定助手。判断用户问题属于哪类：\n" +
	"- knowledge：知识提问（需查知识库回答的事实/技术/业务问题）\n" +
	"- followup：对上一条回答的指令（重新回答/详细点/简短点/再解释一下/换个说法）\n" +
	"- chat：身份询问（你是谁）、能力询问（你是X专家吗）、闲聊、风格要求\n" +
	`只输出 JSON：{"intent":"knowledge"} 或 {"intent":"followup"} 或 {"intent":"chat"}。`

// toolDecisionSystemPrompt 工具决策 system 提示模板，与 Python _decide_tool_call 对齐。
// 占位符 {tool_lines} 由调用方填充可用工具列表。
const toolDecisionSystemPrompt = "你是工具路由助手。根据用户问题判断是否需要调用以下工具。\n" +
	"可用工具:\n{tool_lines}\n\n" +
	"如果需要调用工具，只输出 JSON：" +
	`{"need_tool": true, "tool": "工具名", "arguments": {...}}。` +
	"如果不需要工具（问题与工具无关，或已有信息足够回答），只输出 JSON：" +
	`{"need_tool": false}。不要输出任何其他内容。`

	// 注：会话摘要/记忆提取相关 prompt 与常量（summarizeSystemPrompt、
	// memoryExtractSystemPrompt、allowedMemoryTypes、memoryTemplates、
	// sensitiveKeywords、maxMemoryLength）由 chat 包持有并使用，不在 rag 包重复声明（L11）。
