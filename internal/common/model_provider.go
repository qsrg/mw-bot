// File model_provider.go: 模型 Provider 接口与 OpenAI 兼容网关实现，
// 对齐 Python app/common/model_provider.py。
//
// 业务模块只依赖 EmbeddingProvider 与 LLMProvider 接口，
// 不直接耦合具体网关实现，便于切换模型或网关实现。
package common

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Message 对话消息结构，OpenAI 兼容格式。
type Message struct {
	Role    string `json:"role"`    // 角色：system/user/assistant
	Content string `json:"content"` // 消息文本
}

// EmbeddingProvider Embedding 提供者接口。
type EmbeddingProvider interface {
	// Embed 将文本批量转为向量，返回顺序与输入一致。
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// LLMProvider LLM 提供者接口。
type LLMProvider interface {
	// Chat 同步对话补全，返回模型回答文本。
	Chat(ctx context.Context, messages []Message) (string, error)
	// StreamChat 流式对话补全。
	// iter 每次调用返回下一个 chunk 的 (kind, delta)；kind 为 "content"（正式回答）
	// 或 "reasoning"（推理模型思考链）；无更多数据时 err 为 io.EOF。
	StreamChat(ctx context.Context, messages []Message) (func() (string, string, error), error)
}

// httpTimeout 默认 HTTP 超时。
const httpTimeout = 60 * time.Second

// streamStallTimeout 流式读取无数据超时，对齐 Python httpx.stream(timeout=60)：
// 超过该时长未读到任何数据则中断读循环，避免上游网关卡住导致 goroutine 与连接永久挂起（H5）。
const streamStallTimeout = 60 * time.Second

// pendingChunk 缓存同一 SSE delta 中尚未返回的 chunk（reasoning 与 content 可能并存）。
type pendingChunk struct {
	kind string
	text string
}

// embeddingRequest OpenAI 兼容 embeddings 请求体。
type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// embeddingItem OpenAI 兼容 embeddings 响应中的单个条目。
type embeddingItem struct {
	Embedding []float32 `json:"embedding"`
}

// embeddingResponse OpenAI 兼容 embeddings 响应体。
type embeddingResponse struct {
	Data []embeddingItem `json:"data"`
}

// HttpEmbeddingProvider 基于 OpenAI 兼容网关的 Embedding 实现。
type HttpEmbeddingProvider struct {
	baseURL string // 网关地址，如 https://api.example.com/v1
	apiKey  string // Bearer token
	model   string // 模型名
	client  *http.Client
}

// NewHttpEmbeddingProvider 创建 HTTP Embedding Provider。
func NewHttpEmbeddingProvider(baseURL, apiKey, model string) *HttpEmbeddingProvider {
	return &HttpEmbeddingProvider{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		client:  &http.Client{Timeout: httpTimeout},
	}
}

// Embed 调用网关 embeddings 接口，返回每条文本对应的向量。
func (p *HttpEmbeddingProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(embeddingRequest{Model: p.model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("marshal embedding request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create embedding request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call embedding api: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embedding api status %d: %s", resp.StatusCode, string(respBody))
	}
	var parsed embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("embedding count mismatch: want %d got %d", len(texts), len(parsed.Data))
	}
	result := make([][]float32, len(parsed.Data))
	for i, item := range parsed.Data {
		result[i] = item.Embedding
	}
	return result, nil
}

// chatRequest OpenAI 兼容 chat completions 请求体。
type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

// chatResponse OpenAI 兼容 chat completions 响应体（非流式）。
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// chatStreamDelta 流式响应中 choices[0].delta。
type chatStreamDelta struct {
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content"`
}

// chatStreamChunk 流式响应单个 chunk。
type chatStreamChunk struct {
	Choices []struct {
		Delta chatStreamDelta `json:"delta"`
	} `json:"choices"`
}

// HttpLLMProvider 基于 OpenAI 兼容网关的 LLM 实现。
type HttpLLMProvider struct {
	baseURL string // 网关地址
	apiKey  string // Bearer token
	model   string // 模型名
	client  *http.Client
}

// NewHttpLLMProvider 创建 HTTP LLM Provider。
func NewHttpLLMProvider(baseURL, apiKey, model string) *HttpLLMProvider {
	return &HttpLLMProvider{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		client:  &http.Client{Timeout: httpTimeout},
	}
}

// Chat 调用网关 chat/completions 接口，返回模型回答文本。
func (p *HttpLLMProvider) Chat(ctx context.Context, messages []Message) (string, error) {
	body, err := json.Marshal(chatRequest{Model: p.model, Messages: messages, Stream: false})
	if err != nil {
		return "", fmt.Errorf("marshal chat request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create chat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("call chat api: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("chat api status %d: %s", resp.StatusCode, string(respBody))
	}
	var parsed chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("decode chat response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("chat response has no choices")
	}
	return parsed.Choices[0].Message.Content, nil
}

// StreamChat 流式调用网关 chat/completions 接口。
// iter 每次返回一个 chunk；kind 为 "content" 或 "reasoning"。
// 流结束或遇到错误时返回相应 err；正常结束时 err 为 io.EOF。
func (p *HttpLLMProvider) StreamChat(ctx context.Context, messages []Message) (func() (string, string, error), error) {
	body, err := json.Marshal(chatRequest{Model: p.model, Messages: messages, Stream: true})
	if err != nil {
		return nil, fmt.Errorf("marshal stream chat request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create stream chat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Accept", "text/event-stream")
	// 流式响应不能复用带 Timeout 的 client（会强制断流），这里用无超时 client
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call stream chat api: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("stream chat api status %d: %s", resp.StatusCode, string(respBody))
	}

	reader := bufio.NewReader(resp.Body)
	closed := false
	// 无数据读超时：超时未读到数据则关闭 body 中断阻塞中的 ReadString（H5）
	stallTimer := time.AfterFunc(streamStallTimeout, func() { resp.Body.Close() })
	stopTimer := func() { stallTimer.Stop() }
	// 同一 delta 的 reasoning+content 缓冲，保证两者都被透传（H6）
	var pending []pendingChunk
	iter := func() (string, string, error) {
		for {
			// 先消费同一 delta 缓冲的 chunk（reasoning 之后再吐 content）
			if len(pending) > 0 {
				c := pending[0]
				pending = pending[1:]
				return c.kind, c.text, nil
			}
			// 已关闭则直接返回 EOF
			if closed {
				return "", "", io.EOF
			}
			line, err := reader.ReadString('\n')
			if err != nil && err != io.EOF {
				resp.Body.Close()
				closed = true
				stopTimer()
				return "", "", fmt.Errorf("read stream: %w", err)
			}
			// 收到数据，重置无数据超时
			stallTimer.Reset(streamStallTimeout)
			line = strings.TrimRight(line, "\r\n")
			// 流真正结束（io.EOF 且无残余数据）
			if err == io.EOF && line == "" {
				resp.Body.Close()
				closed = true
				stopTimer()
				return "", "", io.EOF
			}
			if line == "" {
				if err == io.EOF {
					resp.Body.Close()
					closed = true
					stopTimer()
					return "", "", io.EOF
				}
				continue
			}
			// SSE 行格式：data: {json} 或 data: [DONE]
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				resp.Body.Close()
				closed = true
				stopTimer()
				return "", "", io.EOF
			}
			var chunk chatStreamChunk
			if jerr := json.Unmarshal([]byte(data), &chunk); jerr != nil {
				// 跳过无法解析的行（如心跳注释）
				continue
			}
			if len(chunk.Choices) == 0 {
				continue
			}
			delta := chunk.Choices[0].Delta
			// 推理模型同一 delta 可能同时含 reasoning_content 与 content，
			// 两者都入缓冲依次透传（对齐 Python yield 顺序，H6）
			if delta.ReasoningContent != "" {
				pending = append(pending, pendingChunk{"reasoning", delta.ReasoningContent})
			}
			if delta.Content != "" {
				pending = append(pending, pendingChunk{"content", delta.Content})
			}
			// 空增量：继续读下一行
		}
	}
	return iter, nil
}

// NewEmbeddingProvider 根据 Settings 返回 Embedding Provider。
// 始终使用真实模型网关（对齐 Python get_embedding_provider，ENVIRONMENT=local 也不回退伪向量，
// 否则 local 下 dense 检索会因伪向量无语义而失效）。
// EMBEDDING_BASE_URL/EMBEDDING_API_KEY 为空时回退到 LLM_BASE_URL/LLM_API_KEY。
func NewEmbeddingProvider(settings Settings) EmbeddingProvider {
	baseURL := settings.EmbeddingBaseURL
	apiKey := settings.EmbeddingAPIKey
	if baseURL == "" {
		baseURL = settings.LLMBaseURL
	}
	if apiKey == "" {
		apiKey = settings.LLMAPIKey
	}
	return NewHttpEmbeddingProvider(baseURL, apiKey, settings.EmbeddingModel)
}

// NewLLMProvider 根据 Settings 返回 LLM Provider。
// 始终使用真实网关（流式需要真实模型）；ENVIRONMENT=local 也不回退到 Fake。
func NewLLMProvider(settings Settings) LLMProvider {
	return NewHttpLLMProvider(settings.LLMBaseURL, settings.LLMAPIKey, settings.LLMModel)
}
