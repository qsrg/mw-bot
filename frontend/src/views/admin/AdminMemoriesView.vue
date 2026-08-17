<template>
  <AppLayout>
    <div class="page">
      <div class="page-head">
        <el-icon class="page-head-icon"><Collection /></el-icon>
        <div>
          <h2>记忆管理</h2>
          <p class="page-head-sub">查看与清理系统自动记录的用户偏好</p>
        </div>
      </div>
      <el-alert
        type="info"
        :closable="false"
        title="系统仅自动记录用户显式表达的偏好（默认环境、常用集群、回答风格等），不记录身份声明或事实断言。"
        class="block"
      />
      <el-divider />
      <el-table :data="memories" v-loading="loading" border>
        <el-table-column prop="memory_type" label="类型" width="180">
          <template #default="{ row }">
            <el-tag size="small">{{ typeLabel(row.memory_type) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="content" label="内容" min-width="320" />
        <el-table-column label="启用" width="100">
          <template #default="{ row }">
            <el-switch
              :model-value="row.enabled"
              @change="(val: boolean) => toggle(row, val)"
            />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="100">
          <template #default="{ row }">
            <el-button link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
      <el-empty v-if="!loading && memories.length === 0" description="暂无记忆" />
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { onMounted, ref } from "vue";
import { ElMessageBox } from "element-plus";
import { Collection } from "@element-plus/icons-vue";
import AppLayout from "../../layouts/AppLayout.vue";
import { deleteMemory, listMemories, toggleMemory, type MemoryItem } from "../../api/chat";

const memories = ref<MemoryItem[]>([]);
const loading = ref(false);

// 记忆类型中文标签，未知类型原样展示
function typeLabel(memoryType: string): string {
  const labels: Record<string, string> = {
    default_environment: "默认环境",
    common_cluster: "常用集群",
    default_knowledge_base: "默认知识库",
    common_component: "常用组件",
    frequent_domain: "高频业务域",
    answer_style: "回答风格",
    preference: "偏好",
  };
  return labels[memoryType] ?? memoryType;
}

async function toggle(row: MemoryItem, enabled: boolean): Promise<void> {
  await toggleMemory(row.id, enabled);
  row.enabled = enabled;
}

async function remove(row: MemoryItem): Promise<void> {
  try {
    await ElMessageBox.confirm("确定删除该记忆？", "删除记忆", {
      confirmButtonText: "删除",
      cancelButtonText: "取消",
      type: "warning",
    });
  } catch {
    return;
  }
  await deleteMemory(row.id);
  memories.value = memories.value.filter((m) => m.id !== row.id);
}

async function load(): Promise<void> {
  loading.value = true;
  try {
    memories.value = await listMemories();
  } finally {
    loading.value = false;
  }
}

onMounted(load);
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
