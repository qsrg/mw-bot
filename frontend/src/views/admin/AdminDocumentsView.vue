<template>
  <AppLayout>
    <div class="page">
      <div class="page-head">
        <el-icon class="page-head-icon"><Document /></el-icon>
        <div>
          <h2>文档管理</h2>
          <p class="page-head-sub">上传文档后自动建立索引，供智能问答检索引用</p>
        </div>
      </div>
      <el-card shadow="never" class="block">
        <div class="upload-row">
          <el-upload :auto-upload="false" :on-change="onChange" :limit="1" :file-list="fileList">
            <el-button type="primary" plain>
              <el-icon><FolderOpened /></el-icon>
              选择文档
            </el-button>
            <template #tip>
              <div class="upload-tip">仅支持 Markdown（.md），上传后内容入库，可在线编辑</div>
            </template>
          </el-upload>
          <div class="upload-actions">
            <el-button plain @click="openCreate">
              <el-icon><EditPen /></el-icon>
              新建文档
            </el-button>
            <el-button
              type="primary"
              :disabled="!selectedFile"
              :loading="uploading"
              @click="submit"
            >
              上传并索引
            </el-button>
          </div>
        </div>
        <el-alert
          v-if="lastUploaded"
          type="success"
          :title="`文档已提交索引：${lastUploaded.file_name}（状态：${lastUploaded.index_status}）`"
          class="block"
        />
      </el-card>
      <el-divider />
      <el-table :data="documents" v-loading="loadingList" border>
        <el-table-column prop="file_name" label="文件名" min-width="180" />
        <el-table-column prop="content_type" label="类型" width="140" />
        <el-table-column prop="file_size" label="大小(字节)" width="120" />
        <el-table-column prop="index_status" label="索引状态" width="110">
          <template #default="{ row }">
            <el-tag :type="statusType(row.index_status)">{{ row.index_status }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="created_at" label="创建时间" min-width="160" />
        <el-table-column label="操作" width="200">
          <template #default="{ row }">
            <el-button link type="primary" @click="preview(row)">预览</el-button>
            <el-button v-if="isMarkdown(row)" link type="primary" @click="openEdit(row)"
              >编辑</el-button
            >
            <el-button link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <el-dialog
        v-model="previewVisible"
        :title="`预览：${previewFileName}`"
        width="80%"
        top="5vh"
        destroy-on-close
        @closed="onPreviewClosed"
      >
        <div v-loading="previewLoading" class="preview-body">
          <div v-if="!previewLoading" class="preview-toolbar">
            <el-button type="primary" size="small" @click="downloadPreview"
              >下载文件</el-button
            >
            <span v-if="previewType === 'unsupported'" class="preview-hint"
              >该文件类型暂不支持在线预览，请下载后查看</span
            >
          </div>
          <iframe
            v-if="previewType === 'pdf' && previewUrl"
            :src="previewUrl"
            class="preview-iframe"
          ></iframe>
          <div
            v-else-if="previewType === 'markdown'"
            class="preview-md"
            v-html="previewHtml"
          ></div>
        </div>
      </el-dialog>

      <el-dialog
        v-model="editorVisible"
        :title="editorMode === 'create' ? '新建文档' : `编辑：${editorFileName}`"
        width="80%"
        top="5vh"
        destroy-on-close
      >
        <div v-loading="editorLoading" class="editor-body">
          <el-input
            v-model="editorFileName"
            placeholder="文档名（如：RocketMQ 常见问题）"
            class="editor-name"
          >
            <template #prepend>文件名</template>
          </el-input>
          <el-input
            v-model="editorContent"
            type="textarea"
            :rows="20"
            placeholder="支持 Markdown 语法"
            class="editor-textarea"
          />
        </div>
        <template #footer>
          <el-button @click="editorVisible = false">取消</el-button>
          <el-button type="primary" :loading="editorSaving" @click="saveEditor">
            {{ editorMode === "create" ? "创建并索引" : "保存并重建索引" }}
          </el-button>
        </template>
      </el-dialog>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { onMounted, ref } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { Document, EditPen, FolderOpened } from "@element-plus/icons-vue";
import type { UploadFile } from "element-plus";
import MarkdownIt from "markdown-it";
import AppLayout from "../../layouts/AppLayout.vue";
import {
  createDocument,
  deleteDocument,
  fetchDocumentContent,
  listDocuments,
  updateDocument,
  uploadDocument,
  type DocumentItem,
} from "../../api/knowledge";

// markdown-it 默认关闭原始 HTML 渲染并转义标签，v-html 安全
const md = new MarkdownIt();

const selectedFile = ref<File | null>(null);
const fileList = ref<UploadFile[]>([]);
const uploading = ref(false);
const lastUploaded = ref<DocumentItem | null>(null);
const documents = ref<DocumentItem[]>([]);
const loadingList = ref(false);

function onChange(file: UploadFile): void {
  selectedFile.value = file.raw || null;
}

function statusType(status: string): "success" | "warning" | "danger" | "info" {
  if (status === "indexed") return "success";
  if (status === "pending") return "warning";
  if (status === "failed") return "danger";
  return "info";
}

async function submit(): Promise<void> {
  if (!selectedFile.value) return;
  uploading.value = true;
  try {
    lastUploaded.value = await uploadDocument(selectedFile.value);
    selectedFile.value = null;
    fileList.value = [];
    await loadDocuments();
  } finally {
    uploading.value = false;
  }
}

// 删除文档：确认后调接口，后端会同步清理向量与存储文件，再刷新列表
async function remove(row: DocumentItem): Promise<void> {
  try {
    await ElMessageBox.confirm(
      `确定删除文档「${row.file_name}」？将同时清理已索引的向量数据，删除后不可恢复。`,
      "删除文档",
      {
        confirmButtonText: "删除",
        cancelButtonText: "取消",
        type: "warning",
      },
    );
  } catch {
    return; // 用户取消
  }
  await deleteDocument(row.id);
  await loadDocuments();
}

async function loadDocuments(): Promise<void> {
  loadingList.value = true;
  try {
    documents.value = await listDocuments();
  } finally {
    loadingList.value = false;
  }
}

// 预览状态
const previewVisible = ref(false);
const previewLoading = ref(false);
const previewType = ref<"pdf" | "markdown" | "unsupported">("unsupported");
const previewUrl = ref<string | null>(null);
const previewHtml = ref("");
const previewFileName = ref("");
let previewBlob: Blob | null = null;

function detectPreviewType(
  contentType: string,
  fileName: string,
): "pdf" | "markdown" | "unsupported" {
  const ct = contentType.toLowerCase();
  const name = fileName.toLowerCase();
  if (ct.includes("pdf") || name.endsWith(".pdf")) return "pdf";
  if (
    ct.includes("markdown") ||
    name.endsWith(".md") ||
    name.endsWith(".markdown")
  )
    return "markdown";
  return "unsupported";
}

// 是否为 Markdown 文档（可在线编辑；历史文件型 Markdown 编辑后自动转为内容入库）
function isMarkdown(row: DocumentItem): boolean {
  return detectPreviewType(row.content_type, row.file_name) === "markdown";
}

// 在线新建/编辑状态
const editorVisible = ref(false);
const editorLoading = ref(false);
const editorSaving = ref(false);
const editorMode = ref<"create" | "edit">("create");
const editorFileName = ref("");
const editorContent = ref("");
let editingDocId: string | null = null;

function openCreate(): void {
  editorMode.value = "create";
  editingDocId = null;
  editorFileName.value = "";
  editorContent.value = "";
  editorVisible.value = true;
}

// 编辑文档：先拉取当前内容，保存时 PUT 更新并重建索引
async function openEdit(row: DocumentItem): Promise<void> {
  editorMode.value = "edit";
  editingDocId = row.id;
  editorFileName.value = row.file_name;
  editorContent.value = "";
  editorVisible.value = true;
  editorLoading.value = true;
  try {
    const blob = await fetchDocumentContent(row.id);
    editorContent.value = await blob.text();
  } catch {
    // 拉取失败：http 拦截器已提示错误，关闭弹窗
    editorVisible.value = false;
  } finally {
    editorLoading.value = false;
  }
}

async function saveEditor(): Promise<void> {
  if (!editorFileName.value.trim()) {
    ElMessage.warning("请填写文档名");
    return;
  }
  editorSaving.value = true;
  try {
    if (editorMode.value === "create") {
      const doc = await createDocument({
        file_name: editorFileName.value.trim(),
        content: editorContent.value,
      });
      lastUploaded.value = doc;
    } else if (editingDocId) {
      const doc = await updateDocument(editingDocId, {
        file_name: editorFileName.value.trim(),
        content: editorContent.value,
      });
      lastUploaded.value = doc;
    }
    editorVisible.value = false;
    await loadDocuments();
  } finally {
    editorSaving.value = false;
  }
}

// 预览文档：按类型直显 PDF/Markdown，其余提示不支持并引导下载
async function preview(row: DocumentItem): Promise<void> {
  previewVisible.value = true;
  previewLoading.value = true;
  previewType.value = "unsupported";
  previewUrl.value = null;
  previewHtml.value = "";
  previewFileName.value = row.file_name;
  try {
    const blob = await fetchDocumentContent(row.id);
    previewBlob = blob;
    previewType.value = detectPreviewType(row.content_type, row.file_name);
    if (previewType.value === "pdf") {
      previewUrl.value = URL.createObjectURL(blob);
    } else if (previewType.value === "markdown") {
      previewHtml.value = md.render(await blob.text());
    }
  } catch {
    // 拉取失败：http 拦截器已提示错误，关闭弹窗
    previewVisible.value = false;
  } finally {
    previewLoading.value = false;
  }
}

// 下载当前预览文件：临时 blob 链接触发下载后立即释放
function downloadPreview(): void {
  if (!previewBlob) return;
  const url = URL.createObjectURL(previewBlob);
  const a = document.createElement("a");
  a.href = url;
  a.download = previewFileName.value;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

// 关闭预览：释放 blob URL 并清理引用
function onPreviewClosed(): void {
  if (previewUrl.value) {
    URL.revokeObjectURL(previewUrl.value);
    previewUrl.value = null;
  }
  previewBlob = null;
  previewHtml.value = "";
}

onMounted(loadDocuments);
</script>

<style scoped>
.page {
  padding: 24px 28px;
  overflow-y: auto;
  height: 100%;
}
.page-head {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 18px;
}
.page-head-icon {
  font-size: 22px;
  padding: 10px;
  border-radius: 8px;
  background: var(--mw-signal-soft);
  color: var(--mw-signal-strong);
}
.page-head h2 {
  margin: 0;
  font-size: 18px;
}
.page-head-sub {
  margin: 2px 0 0;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}
.upload-row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}
.upload-actions {
  display: flex;
  gap: 8px;
}
.upload-tip {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.editor-body {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.editor-textarea :deep(textarea) {
  font-family: var(--el-font-family-mono, Menlo, Consolas, monospace);
  font-size: 13px;
  line-height: 1.7;
}
.block {
  margin-bottom: 16px;
}
.preview-body {
  min-height: 60vh;
}
.preview-toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 12px;
}
.preview-hint {
  color: var(--el-text-color-secondary);
  font-size: 13px;
}
.preview-iframe {
  width: 100%;
  height: 70vh;
  border: 1px solid var(--el-border-color-light);
  border-radius: 4px;
}
.preview-md {
  max-height: 70vh;
  overflow: auto;
  padding: 16px;
  border: 1px solid var(--el-border-color-light);
  border-radius: 4px;
  line-height: 1.7;
}
</style>
