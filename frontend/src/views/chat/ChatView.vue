<template>
  <AppLayout>
    <div class="chat-page">
      <aside class="history">
        <div class="history-head">
          <span class="history-title">历史会话</span>
          <span class="mw-wordmark history-count">{{ conversations.length }}</span>
        </div>
        <el-button type="primary" plain class="history-new" @click="newConversation">
          <el-icon><Plus /></el-icon>
          新建会话
        </el-button>
        <div
          v-for="conv in conversations"
          :key="conv.id"
          class="history-item"
          :class="{ active: conv.id === conversationId }"
          @click="openConversation(conv.id)"
        >
          <span class="history-item-title">{{ conv.title }}</span>
          <el-button
            class="history-item-delete"
            link
            size="small"
            aria-label="删除会话"
            @click.stop="removeConversation(conv.id)"
          >
            <el-icon><Delete /></el-icon>
          </el-button>
        </div>
      </aside>
      <section class="main">
        <div class="messages" ref="messagesRef">
          <div class="messages-inner">
            <div v-if="messages.length === 0" class="empty">
              <span class="mw-dot empty-dot"></span>
              <div class="empty-title">向 mw-bot 提问</div>
              <div class="empty-sub">回答基于已索引的企业文档，并附带参考来源。</div>
              <div class="empty-examples">
                <button
                  v-for="ex in examples"
                  :key="ex"
                  class="example-chip"
                  type="button"
                  @click="pickExample(ex)"
                >
                  {{ ex }}
                </button>
              </div>
            </div>
            <article v-for="message in messages" :key="message.id" :class="message.role">
              <div class="bubble">
                <div
                  v-if="message.role === 'assistant' && message.reasoning"
                  class="reasoning"
                >
                  <div class="reasoning-header" @click="toggleReasoning(message.id)">
                    <span class="reasoning-arrow">{{
                      isReasoningOpen(message.id) ? "▾" : "▸"
                    }}</span>
                    <span>思考过程</span>
                  </div>
                  <div v-show="isReasoningOpen(message.id)" class="reasoning-text">
                    {{ message.reasoning }}
                  </div>
                </div>
                <div
                  v-if="message.role === 'assistant' && message.streaming && !message.content"
                  class="thinking"
                >
                  <span class="mw-dot thinking-dot"></span>
                  正在生成回答
                </div>
                <div
                  v-if="message.role === 'assistant'"
                  class="content markdown-body"
                  :class="{ streaming: message.streaming }"
                  v-html="renderMarkdown(message.content)"
                ></div>
                <div v-else class="content user-content">{{ message.content }}</div>
                <el-tag v-if="message.used_model_inference" type="warning" size="small" effect="plain">
                  模型推断
                </el-tag>
                <div v-if="message.citations?.length && !message.streaming" class="citations">
                  <div class="citations-title">参考来源</div>
                  <div v-for="(c, idx) in message.citations" :key="idx" class="citation">
                    <span class="mw-wordmark citation-idx">{{ idx + 1 }}</span>
                    {{ c.file_name }}
                  </div>
                </div>
              </div>
            </article>
          </div>
        </div>
        <footer class="composer">
          <div class="composer-box">
            <el-input
              v-model="question"
              type="textarea"
              :rows="3"
              resize="none"
              placeholder="请输入问题"
              @keydown.enter.ctrl="submit"
            />
            <div class="composer-bar">
              <span class="composer-hint">Ctrl + Enter 发送</span>
              <el-button
                type="primary"
                :loading="loading"
                :disabled="!question.trim()"
                @click="submit"
              >
                发送
                <el-icon class="send-icon"><Promotion /></el-icon>
              </el-button>
            </div>
          </div>
        </footer>
      </section>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { nextTick, onMounted, ref } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { Delete, Plus, Promotion } from "@element-plus/icons-vue";
import MarkdownIt from "markdown-it";
import AppLayout from "../../layouts/AppLayout.vue";
import {
  deleteConversation,
  listConversations,
  listMessages,
  sendMessageStream,
  type Citation,
  type ConversationSummary,
} from "../../api/chat";

// markdown 渲染器：默认禁用 html（转义原始标签，防 XSS），开启换行与链接识别
const md = new MarkdownIt({ breaks: true, linkify: true });
function renderMarkdown(text: string): string {
  return md.render(text || "");
}

interface ChatMessage {
  id: string;
  role: "user" | "assistant";
  content: string;
  reasoning?: string;
  used_model_inference?: boolean;
  citations?: Citation[];
  streaming?: boolean;
}

const question = ref("");
const loading = ref(false);
const messages = ref<ChatMessage[]>([]);
const conversations = ref<ConversationSummary[]>([]);
const conversationId = ref<string | undefined>(undefined);
const messagesRef = ref<HTMLElement | null>(null);
// 推理过程折叠态：按消息 id 记录，缺省（流式中）为展开，便于实时观察思考
const reasoningOpen = ref<Record<string, boolean>>({});

// 空状态示例问题：点击直接填入输入框
const examples = [
  "默认集群的 broker 配置怎么看？",
  "Kafka 消费积压的排查步骤",
  "给一个 RocketMQ 生产者的配置示例",
];

function pickExample(text: string): void {
  question.value = text;
}

function isReasoningOpen(id: string): boolean {
  return reasoningOpen.value[id] ?? true;
}

function toggleReasoning(id: string): void {
  reasoningOpen.value[id] = !isReasoningOpen(id);
}

function newConversation(): void {
  conversationId.value = undefined;
  messages.value = [];
}

async function openConversation(id: string): Promise<void> {
  conversationId.value = id;
  messages.value = [];
  try {
    messages.value = await listMessages(id);
    await nextTick(() => {
      if (messagesRef.value) messagesRef.value.scrollTop = messagesRef.value.scrollHeight;
    });
  } catch {
    // 加载失败：http 拦截器已提示错误，保留空消息列表
  }
}

// 删除会话：确认后调接口；若删的是当前会话则清空消息与会话 ID，再刷新列表
async function removeConversation(id: string): Promise<void> {
  try {
    await ElMessageBox.confirm("确定删除该会话？删除后不可恢复。", "删除会话", {
      confirmButtonText: "删除",
      cancelButtonText: "取消",
      type: "warning",
    });
  } catch {
    return; // 用户取消
  }
  await deleteConversation(id);
  if (conversationId.value === id) {
    conversationId.value = undefined;
    messages.value = [];
  }
  await loadConversations();
}

// 生成消息 ID：优先 crypto.randomUUID；非安全上下文（如经 LAN IP 访问）下
// crypto.randomUUID 不可用，降级为时间戳+随机串，避免 submit 抛错导致"发送无反应"
function genId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `id-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
}

async function submit(): Promise<void> {
  const text = question.value.trim();
  if (!text) return;
  messages.value.push({ id: genId(), role: "user", content: text });
  question.value = "";
  loading.value = true;
  // 先插入占位助手消息，流式 delta 逐步追加内容
  const idx =
    messages.value.push({ id: genId(), role: "assistant", content: "", streaming: true }) - 1;
  await nextTick(() => {
    if (messagesRef.value) messagesRef.value.scrollTop = messagesRef.value.scrollHeight;
  });
  try {
    await sendMessageStream(text, conversationId.value, {
      onMeta: (meta) => {
        conversationId.value = meta.conversation_id;
        messages.value[idx].used_model_inference = meta.used_model_inference;
        messages.value[idx].citations = meta.citations;
      },
      onReasoning: (piece) => {
        messages.value[idx].reasoning = (messages.value[idx].reasoning ?? "") + piece;
        if (messagesRef.value) messagesRef.value.scrollTop = messagesRef.value.scrollHeight;
      },
      onDelta: (piece) => {
        messages.value[idx].content += piece;
        if (messagesRef.value) messagesRef.value.scrollTop = messagesRef.value.scrollHeight;
      },
      onDone: (messageId, memoryExtractionFailed) => {
        messages.value[idx].id = messageId;
        messages.value[idx].streaming = false;
        // 记忆提取失败不阻断回答，仅轻提示，便于用户知晓偏好未被记住
        if (memoryExtractionFailed) {
          ElMessage.warning("本轮长期记忆提取失败，不影响回答内容");
        }
      },
      onError: () => {
        messages.value[idx].streaming = false;
        if (!messages.value[idx].content) {
          messages.value[idx].content = "请求失败，请稍后重试。";
        }
      },
    });
  } finally {
    loading.value = false;
  }
}

async function loadConversations(): Promise<void> {
  conversations.value = await listConversations();
}

onMounted(loadConversations);
</script>

<style scoped>
.chat-page {
  display: flex;
  height: 100%;
}
.history {
  width: 210px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 14px 10px;
  background: #ffffff;
  border-right: 1px solid var(--el-border-color-light);
  overflow-y: auto;
}
.history-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  padding: 0 6px;
}
.history-title {
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.06em;
  color: var(--el-text-color-secondary);
}
.history-count {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.history-new {
  width: 100%;
}
.history-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 8px 10px;
  cursor: pointer;
  border-radius: 6px;
  transition: background-color 0.15s ease;
}
.history-item-title {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13px;
}
.history-item-delete {
  flex-shrink: 0;
  opacity: 0;
  color: var(--el-text-color-secondary);
}
.history-item:hover .history-item-delete {
  opacity: 1;
}
.history-item:hover {
  background: var(--el-fill-color-light);
}
.history-item.active {
  background: var(--mw-signal-soft);
  box-shadow: inset 3px 0 0 var(--mw-signal);
}
.main {
  flex: 1;
  display: flex;
  flex-direction: column;
  /* min-height: 0 让 flex item 在纵向布局中可被压缩，避免被 .messages 内容撑开，
     进而撑破 .chat-page / .app-main 触发整页滚动 */
  min-height: 0;
}
.messages {
  flex: 1;
  overflow-y: auto;
  /* 水平 padding 与 .composer(16px) 一致，保证窄屏下消息容器与输入框左右对齐 */
  padding: 24px 16px;
  /* 关键：覆盖 flex item 默认的 min-height: auto，
     使内容超出时 .messages 可缩小到容器范围内，触发自身 overflow-y: auto 滚动，
     而不是撑开父级导致整页滚动 */
  min-height: 0;
}
/* 消息内容容器：与底部输入框（.composer-box）共用的自适应列宽，
   宽屏最多 1080px、窄屏自动收窄，保证消息气泡与输入框左右对齐 */
.messages-inner {
  max-width: var(--chat-column, 1080px);
  min-height: 100%;
  margin: 0 auto;
}
/* 消息之间的垂直间隔，避免相邻气泡边框贴在一起 */
.messages-inner article {
  margin-bottom: 14px;
}
.messages-inner article:last-child {
  margin-bottom: 0;
}
/* 空状态：邀请用户直接提问 */
.empty {
  height: 100%;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  text-align: center;
}
.empty-dot {
  width: 10px;
  height: 10px;
  margin-bottom: 8px;
}
.empty-title {
  font-size: 17px;
  font-weight: 600;
}
.empty-sub {
  font-size: 13px;
  color: var(--el-text-color-secondary);
}
.empty-examples {
  display: flex;
  flex-wrap: wrap;
  justify-content: center;
  gap: 10px;
  margin-top: 14px;
  max-width: 460px;
}
.example-chip {
  padding: 7px 14px;
  font-size: 13px;
  font-family: inherit;
  color: var(--el-text-color-regular);
  background: #ffffff;
  border: 1px solid var(--el-border-color);
  border-radius: 999px;
  cursor: pointer;
  transition: border-color 0.15s ease, color 0.15s ease;
}
.example-chip:hover {
  border-color: var(--mw-signal);
  color: var(--mw-signal-strong);
}
.example-chip:focus-visible {
  outline: 2px solid var(--mw-signal);
  outline-offset: 2px;
}
.bubble {
  padding: 12px 14px;
  border-radius: 10px;
  background: #ffffff;
  border: 1px solid var(--el-border-color-lighter);
}
.user .bubble {
  /* 用户气泡：宽度随内容自适应收缩、右对齐；max-width 限制超长文本 */
  width: fit-content;
  max-width: 72%;
  margin-left: auto;
  background: var(--mw-signal-soft);
  border-color: var(--el-color-primary-light-7);
}
/* 流式回答中，正文末尾跟随一个闪烁光标，表示仍在生成 */
.content.streaming::after {
  content: "▍";
  display: inline-block;
  color: var(--mw-signal);
  animation: caret-blink 1s steps(1) infinite;
  margin-left: 2px;
}
@keyframes caret-blink {
  50% {
    opacity: 0;
  }
}
/* 助手尚未产出首个字符时的等待提示 */
.thinking {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}
.thinking-dot {
  animation: thinking-pulse 1.2s ease-in-out infinite;
}
@keyframes thinking-pulse {
  50% {
    opacity: 0.25;
  }
}
/* 用户消息保留换行与原始字符，不做 markdown 解析 */
.user-content {
  white-space: pre-wrap;
  word-break: break-word;
}
/* 推理模型思考过程：可折叠块，置于正式回答之上，流式中实时展开 */
.reasoning {
  margin-bottom: 8px;
  border: 1px solid var(--el-border-color-light);
  border-radius: 6px;
  background: var(--el-fill-color-lighter);
  font-size: 13px;
  color: var(--el-text-color-secondary);
}
.reasoning-header {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 4px 8px;
  cursor: pointer;
  user-select: none;
}
.reasoning-arrow {
  display: inline-block;
  width: 1em;
  text-align: center;
}
.reasoning-text {
  padding: 0 10px 8px;
  white-space: pre-wrap;
  word-break: break-word;
  line-height: 1.5;
}
/* 助手消息 markdown 渲染样式：v-html 注入的元素需用 :deep 穿透 scoped */
.markdown-body :deep(p) {
  margin: 0.4em 0;
}
.markdown-body :deep(p:first-child) {
  margin-top: 0;
}
.markdown-body :deep(p:last-child) {
  margin-bottom: 0;
}
.markdown-body :deep(ul),
.markdown-body :deep(ol) {
  margin: 0.4em 0;
  padding-left: 1.5em;
}
.markdown-body :deep(li) {
  margin: 0.2em 0;
}
.markdown-body :deep(table) {
  border-collapse: collapse;
  width: 100%;
  margin: 0.5em 0;
  font-size: 13px;
}
.markdown-body :deep(th),
.markdown-body :deep(td) {
  border: 1px solid var(--el-border-color);
  padding: 6px 10px;
  text-align: left;
}
.markdown-body :deep(th) {
  background: var(--el-fill-color);
}
.markdown-body :deep(code) {
  background: var(--el-fill-color-dark);
  padding: 2px 5px;
  border-radius: 3px;
  font-family: var(--mw-mono);
  font-size: 0.9em;
}
.markdown-body :deep(pre) {
  background: var(--el-fill-color-dark);
  padding: 10px;
  border-radius: 6px;
  overflow-x: auto;
  margin: 0.5em 0;
}
.markdown-body :deep(pre code) {
  background: none;
  padding: 0;
}
.markdown-body :deep(h1),
.markdown-body :deep(h2),
.markdown-body :deep(h3),
.markdown-body :deep(h4) {
  margin: 0.6em 0 0.3em;
  font-weight: 600;
}
.markdown-body :deep(a) {
  color: var(--el-color-primary);
}
.markdown-body :deep(blockquote) {
  margin: 0.4em 0;
  padding-left: 1em;
  border-left: 3px solid var(--el-border-color);
  color: var(--el-text-color-secondary);
}
.citations {
  margin-top: 10px;
  padding-top: 8px;
  border-top: 1px dashed var(--el-border-color-light);
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.citations-title {
  font-weight: 600;
  margin-bottom: 4px;
}
.citation {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 2px 0;
}
.citation-idx {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  border-radius: 4px;
  font-size: 11px;
  background: var(--el-fill-color);
  color: var(--el-text-color-regular);
}
/* 输入区：卡片式组合框，说明与发送按钮收在底部一行 */
.composer {
  padding: 12px 16px 14px;
  background: #ffffff;
  border-top: 1px solid var(--el-border-color-light);
}
.composer-box {
  max-width: var(--chat-column, 1080px);
  margin: 0 auto;
  border: 1px solid var(--el-border-color);
  border-radius: 10px;
  overflow: hidden;
  transition: border-color 0.15s ease;
}
.composer-box:focus-within {
  border-color: var(--mw-signal);
}
.composer-box :deep(.el-textarea__inner) {
  border: none;
  box-shadow: none;
}
.composer-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 6px 10px 8px;
}
.composer-hint {
  font-size: 12px;
  color: var(--el-text-color-placeholder);
}
.send-icon {
  margin-left: 4px;
}
</style>
