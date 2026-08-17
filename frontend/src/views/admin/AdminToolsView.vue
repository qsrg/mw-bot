<template>
  <AppLayout>
    <div class="page">
      <div class="page-head">
        <el-icon class="page-head-icon"><SetUp /></el-icon>
        <div>
          <h2>工具管理</h2>
          <p class="page-head-sub">注册 MCP Server，控制工具的启用与调用审批</p>
        </div>
      </div>
      <el-card class="block">
        <template #header>注册 MCP Server</template>
        <el-form inline>
          <el-form-item label="名称">
            <el-input v-model="serverName" placeholder="rocketmq" />
          </el-form-item>
          <el-form-item label="地址">
            <el-input v-model="serverUrl" placeholder="http://localhost:10914" />
          </el-form-item>
          <el-button type="primary" :loading="registering" @click="register">注册并刷新工具</el-button>
        </el-form>
      </el-card>
      <el-card class="block">
        <template #header>已注册 Server</template>
        <el-table :data="servers" v-loading="loadingServers" border>
          <el-table-column prop="name" label="名称" min-width="140" />
          <el-table-column prop="base_url" label="地址" min-width="220" />
          <el-table-column label="启用" width="90">
            <template #default="{ row }">
              <el-tag :type="row.enabled ? 'success' : 'info'">
                {{ row.enabled ? "是" : "否" }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="tool_count" label="工具数" width="90" />
          <el-table-column label="操作" width="240">
            <template #default="{ row }">
              <el-button size="small" @click="refreshServer(row)">刷新</el-button>
              <el-button size="small" @click="openEditServer(row)">编辑</el-button>
              <el-button size="small" type="danger" @click="removeServer(row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-card>
      <el-table :data="tools" v-loading="loadingTools" border class="block">
        <el-table-column prop="server_name" label="所属 Server" min-width="140" />
        <el-table-column prop="tool_name" label="工具名" min-width="200" />
        <el-table-column prop="description" label="描述" min-width="200" />
        <el-table-column label="启用" width="90">
          <template #default="{ row }">
            <el-switch :model-value="row.enabled" @change="(v: boolean) => toggleEnabled(row, v)" />
          </template>
        </el-table-column>
        <el-table-column label="需确认" width="90">
          <template #default="{ row }">
            <el-switch :model-value="row.requires_approval" @change="(v: boolean) => toggleApproval(row, v)" />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="120">
          <template #default="{ row }">
            <el-button size="small" @click="openInvoke(row)">调用</el-button>
          </template>
        </el-table-column>
      </el-table>
      <el-dialog v-model="serverDialogVisible" title="编辑 Server" width="520px">
        <el-form label-width="80px">
          <el-form-item label="名称">
            <el-input v-model="editName" />
          </el-form-item>
          <el-form-item label="地址">
            <el-input v-model="editUrl" />
          </el-form-item>
          <el-form-item label="启用">
            <el-switch v-model="editEnabled" />
          </el-form-item>
        </el-form>
        <template #footer>
          <el-button @click="serverDialogVisible = false">取消</el-button>
          <el-button type="primary" @click="saveServer">保存</el-button>
        </template>
      </el-dialog>
      <el-dialog v-model="invokeVisible" title="调用工具" width="520px">
        <el-form label-width="100px">
          <el-form-item label="参数 JSON">
            <el-input v-model="argsText" type="textarea" :rows="6" />
          </el-form-item>
          <el-form-item v-if="currentTool?.requires_approval" label="二次确认">
            <el-switch v-model="confirmed" />
          </el-form-item>
        </el-form>
        <pre v-if="invokeResult">{{ JSON.stringify(invokeResult, null, 2) }}</pre>
        <template #footer>
          <el-button type="primary" :loading="invoking" @click="doInvoke">调用</el-button>
        </template>
      </el-dialog>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { onMounted, ref } from "vue";
import { ElMessageBox } from "element-plus";
import { SetUp } from "@element-plus/icons-vue";
import AppLayout from "../../layouts/AppLayout.vue";
import {
  deleteServer,
  invokeTool,
  listServers,
  listTools,
  refreshTools,
  registerServer,
  updateServer,
  updateToolPolicy,
  type McpServer,
  type McpTool,
} from "../../api/mcp";

const serverName = ref("rocketmq");
const serverUrl = ref("http://localhost:10914");
const registering = ref(false);
const servers = ref<McpServer[]>([]);
const loadingServers = ref(false);
const tools = ref<McpTool[]>([]);
const loadingTools = ref(false);
const invokeVisible = ref(false);
const currentTool = ref<McpTool | null>(null);
const argsText = ref("{}");
const confirmed = ref(false);
const invokeResult = ref<unknown>(null);
const invoking = ref(false);
const serverDialogVisible = ref(false);
const editingServer = ref<McpServer | null>(null);
const editName = ref("");
const editUrl = ref("");
const editEnabled = ref(true);

async function register(): Promise<void> {
  registering.value = true;
  try {
    const server = await registerServer(serverName.value, serverUrl.value);
    await refreshTools(server.id);
    await loadServers();
    await loadTools();
  } finally {
    registering.value = false;
  }
}

async function loadServers(): Promise<void> {
  loadingServers.value = true;
  try {
    servers.value = await listServers();
  } finally {
    loadingServers.value = false;
  }
}

async function loadTools(): Promise<void> {
  loadingTools.value = true;
  try {
    tools.value = await listTools();
  } finally {
    loadingTools.value = false;
  }
}

async function refreshServer(server: McpServer): Promise<void> {
  await refreshTools(server.id);
  await loadServers();
  await loadTools();
}

function openEditServer(server: McpServer): void {
  editingServer.value = server;
  editName.value = server.name;
  editUrl.value = server.base_url;
  editEnabled.value = server.enabled;
  serverDialogVisible.value = true;
}

async function saveServer(): Promise<void> {
  if (!editingServer.value) return;
  await updateServer(editingServer.value.id, {
    name: editName.value,
    base_url: editUrl.value,
    enabled: editEnabled.value,
  });
  serverDialogVisible.value = false;
  await loadServers();
  await loadTools();
}

async function removeServer(server: McpServer): Promise<void> {
  try {
    await ElMessageBox.confirm(
      `确定删除 Server「${server.name}」？将同时删除其名下全部工具，不可恢复。`,
      "删除 Server",
      {
        confirmButtonText: "删除",
        cancelButtonText: "取消",
        type: "warning",
      },
    );
  } catch {
    return; // 用户取消
  }
  await deleteServer(server.id);
  await loadServers();
  await loadTools();
}

async function toggleEnabled(tool: McpTool, value: boolean): Promise<void> {
  await updateToolPolicy(tool.id, { enabled: value });
  await loadTools();
}

async function toggleApproval(tool: McpTool, value: boolean): Promise<void> {
  await updateToolPolicy(tool.id, { requires_approval: value });
  await loadTools();
}

function openInvoke(tool: McpTool): void {
  currentTool.value = tool;
  argsText.value = "{}";
  confirmed.value = false;
  invokeResult.value = null;
  invokeVisible.value = true;
}

async function doInvoke(): Promise<void> {
  if (!currentTool.value) return;
  invoking.value = true;
  try {
    invokeResult.value = await invokeTool(
      currentTool.value.id,
      JSON.parse(argsText.value),
      confirmed.value,
    );
  } catch {
    invokeResult.value = { error: "参数格式错误或调用失败" };
  } finally {
    invoking.value = false;
  }
}

onMounted(() => {
  loadServers();
  loadTools();
});
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
.block {
  margin-bottom: 16px;
}
</style>
