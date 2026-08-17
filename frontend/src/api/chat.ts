import { http } from "./http";

// 引用来源
export interface Citation {
  document_id: string | null;
  chunk_id: string | null;
  file_name: string | null;
  score: number;
  snippet: string;
}

// 问答响应
export interface ChatResponse {
  message_id: string;
  conversation_id: string;
  content: string;
  citations: Citation[];
  used_model_inference: boolean;
  memory_extraction_failed?: boolean;
}

// 会话摘要
export interface ConversationSummary {
  id: string;
  title: string;
  updated_at: string;
}

// 历史消息项
export interface MessageItem {
  id: string;
  role: "user" | "assistant";
  content: string;
  citations: Citation[];
  used_model_inference: boolean;
  created_at: string;
}

// 提交问题
export async function sendMessage(
  question: string,
  conversationId?: string,
): Promise<ChatResponse> {
  const response = await http.post<ChatResponse>(
    "/chat/messages",
    { question },
    { params: conversationId ? { conversation_id: conversationId } : {} },
  );
  return response.data;
}

// 流式问答回调
export interface StreamHandlers {
  onMeta(meta: {
    conversation_id: string;
    used_model_inference: boolean;
    citations: Citation[];
  }): void;
  onReasoning?(text: string): void;
  onDelta(text: string): void;
  onDone(messageId: string, memoryExtractionFailed?: boolean): void;
  onError?(err: unknown): void;
}

// 流式问答（SSE）：原生 fetch 读取事件流（axios 不支持流式读取），带 JWT，
// 按空行切分 SSE 事件并派发 meta/delta/done 回调
export async function sendMessageStream(
  question: string,
  conversationId: string | undefined,
  handlers: StreamHandlers,
): Promise<void> {
  const base = import.meta.env.VITE_API_BASE_URL || "/api";
  const query = conversationId
    ? `?conversation_id=${encodeURIComponent(conversationId)}`
    : "";
  const token = localStorage.getItem("access_token");
  let response: Response;
  try {
    response = await fetch(`${base}/chat/messages/stream${query}`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      body: JSON.stringify({ question }),
    });
  } catch (err) {
    handlers.onError?.(err);
    return;
  }
  // 401：清理登录态并跳登录页（fetch 不走 axios 拦截器，需手动处理）
  if (response.status === 401) {
    localStorage.removeItem("access_token");
    localStorage.removeItem("username");
    localStorage.removeItem("role");
    window.location.href = "/login";
    return;
  }
  if (!response.ok || !response.body) {
    handlers.onError?.(new Error(`流式请求失败: ${response.status}`));
    return;
  }
  const reader = response.body.getReader();
  const decoder = new TextDecoder("utf-8");
  let buffer = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let sep: number;
    while ((sep = buffer.indexOf("\n\n")) >= 0) {
      const raw = buffer.slice(0, sep);
      buffer = buffer.slice(sep + 2);
      const dataLine = raw.split("\n").find((line) => line.startsWith("data: "));
      if (!dataLine) continue;
      const payload = dataLine.slice("data: ".length);
      if (payload === "[DONE]") return;
      let event: { type: string; [key: string]: unknown };
      try {
        event = JSON.parse(payload) as { type: string; [key: string]: unknown };
      } catch {
        continue;
      }
      if (event.type === "meta") {
        handlers.onMeta({
          conversation_id: String(event.conversation_id ?? ""),
          used_model_inference: Boolean(event.used_model_inference),
          citations: (event.citations as Citation[]) ?? [],
        });
      } else if (event.type === "reasoning") {
        handlers.onReasoning?.(String(event.text ?? ""));
      } else if (event.type === "delta") {
        handlers.onDelta(String(event.text ?? ""));
      } else if (event.type === "done") {
        handlers.onDone(String(event.message_id ?? ""), Boolean(event.memory_extraction_failed));
        return;
      }
    }
  }
}

// 历史会话列表
export async function listConversations(): Promise<ConversationSummary[]> {
  const response = await http.get<ConversationSummary[]>("/chat/conversations");
  return response.data;
}

// 历史会话消息列表
export async function listMessages(conversationId: string): Promise<MessageItem[]> {
  const response = await http.get<MessageItem[]>(
    `/chat/conversations/${conversationId}/messages`,
  );
  return response.data;
}

// 删除会话（含消息与引用，硬删除）
export async function deleteConversation(conversationId: string): Promise<void> {
  await http.delete(`/chat/conversations/${conversationId}`);
}

// 长期记忆项
export interface MemoryItem {
  id: string;
  memory_type: string;
  content: string;
  enabled: boolean;
}

// 列出长期记忆
export async function listMemories(): Promise<MemoryItem[]> {
  const response = await http.get<MemoryItem[]>("/chat/memories");
  return response.data;
}

// 启用或关闭长期记忆
export async function toggleMemory(memoryId: string, enabled: boolean): Promise<MemoryItem> {
  const response = await http.patch<MemoryItem>(
    `/chat/memories/${memoryId}`,
    null,
    { params: { enabled } },
  );
  return response.data;
}

// 删除长期记忆
export async function deleteMemory(memoryId: string): Promise<void> {
  await http.delete(`/chat/memories/${memoryId}`);
}
