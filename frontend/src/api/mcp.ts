import { http } from "./http";

// 工具信息
export interface McpTool {
  id: string;
  server_id: string;
  server_name: string;
  tool_name: string;
  description: string;
  requires_approval: boolean;
  enabled: boolean;
  allowed_roles: string[];
  timeout_seconds: number;
}

// Server 信息
export interface McpServer {
  id: string;
  name: string;
  base_url: string;
  enabled: boolean;
  created_at: string;
  tool_count?: number;
}

// 工具调用响应
export interface ToolInvokeResult {
  status: string;
  result: Record<string, unknown> | null;
  message: string | null;
}

// 注册 Server
export async function registerServer(
  name: string,
  baseUrl: string,
): Promise<McpServer> {
  const response = await http.post<McpServer>("/mcp/servers", {
    name,
    base_url: baseUrl,
  });
  return response.data;
}

// 列出已注册 Server
export async function listServers(): Promise<McpServer[]> {
  const response = await http.get<McpServer[]>("/mcp/servers");
  return response.data;
}

// 更新 Server
export async function updateServer(
  serverId: string,
  payload: { name?: string; base_url?: string; enabled?: boolean },
): Promise<McpServer> {
  const response = await http.patch<McpServer>(`/mcp/servers/${serverId}`, payload);
  return response.data;
}

// 删除 Server（级联清理其名下工具）
export async function deleteServer(serverId: string): Promise<void> {
  await http.delete(`/mcp/servers/${serverId}`);
}

// 刷新 Server 工具
export async function refreshTools(serverId: string): Promise<McpTool[]> {
  const response = await http.post<McpTool[]>(`/mcp/servers/${serverId}/refresh`);
  return response.data;
}

// 工具列表
export async function listTools(): Promise<McpTool[]> {
  const response = await http.get<McpTool[]>("/mcp/tools");
  return response.data;
}

// 更新工具策略
export async function updateToolPolicy(
  toolId: string,
  payload: { enabled?: boolean; requires_approval?: boolean },
): Promise<McpTool> {
  const response = await http.patch<McpTool>(`/mcp/tools/${toolId}`, payload);
  return response.data;
}

// 调用工具
export async function invokeTool(
  toolId: string,
  args: Record<string, unknown>,
  confirmed = false,
): Promise<ToolInvokeResult> {
  const response = await http.post<ToolInvokeResult>(`/mcp/tools/${toolId}/invoke`, {
    arguments: args,
    confirmed,
  });
  return response.data;
}
