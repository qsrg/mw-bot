import { http } from "./http";

// 文档响应
export interface DocumentItem {
  id: string;
  file_name: string;
  content_type: string;
  file_size: number;
  storage_backend: string; // db=内容入库（可在线编辑）/local/minio
  index_status: string;
  created_at: string;
}

// 在线新建文档请求
export interface CreateDocumentPayload {
  file_name: string;
  content: string;
  knowledge_base_id?: string;
}

// 在线更新文档请求
export interface UpdateDocumentPayload {
  file_name: string; // 空串表示保持原文件名
  content: string;
}

// 上传文档（仅支持 Markdown，内容直接入库）
export async function uploadDocument(
  file: File,
  knowledgeBaseId = "1",
): Promise<DocumentItem> {
  const form = new FormData();
  form.append("file", file);
  form.append("knowledge_base_id", knowledgeBaseId);
  const response = await http.post<DocumentItem>("/knowledge/documents", form, {
    headers: { "Content-Type": "multipart/form-data" },
  });
  return response.data;
}

// 在线新建 Markdown 文档（内容入库并自动索引）
export async function createDocument(
  payload: CreateDocumentPayload,
): Promise<DocumentItem> {
  const response = await http.post<DocumentItem>(
    "/knowledge/documents",
    payload,
  );
  return response.data;
}

// 在线更新 Markdown 文档内容并重建索引（向量与 BM25 随之刷新）
export async function updateDocument(
  id: string,
  payload: UpdateDocumentPayload,
): Promise<DocumentItem> {
  const response = await http.put<DocumentItem>(
    `/knowledge/documents/${encodeURIComponent(id)}`,
    payload,
  );
  return response.data;
}

// 文档列表
export async function listDocuments(
  knowledgeBaseId?: string,
): Promise<DocumentItem[]> {
  const response = await http.get<DocumentItem[]>("/knowledge/documents", {
    params: knowledgeBaseId ? { knowledge_base_id: knowledgeBaseId } : {},
  });
  return response.data;
}

// 删除文档（同时清理向量数据与索引）
export async function deleteDocument(id: string): Promise<void> {
  await http.delete(`/knowledge/documents/${encodeURIComponent(id)}`);
}

// 获取文档原始内容（带鉴权），返回 Blob 供在线预览或下载
export async function fetchDocumentContent(id: string): Promise<Blob> {
  const response = await http.get(
    `/knowledge/documents/${encodeURIComponent(id)}/content`,
    { responseType: "blob" },
  );
  return response.data;
}
