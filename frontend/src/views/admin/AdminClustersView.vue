<template>
    <div class="page">
      <div class="page-head">
        <el-icon class="page-head-icon"><OfficeBuilding /></el-icon>
        <div>
          <h2>集群信息</h2>
          <p class="page-head-sub">维护各中间件的集群登记（机房与连接地址），供问答按中间件/机房/集群映射并直连真实集群</p>
        </div>
      </div>
      <el-card class="block">
        <div class="toolbar">
          <el-button type="primary" plain @click="openCreate">
            <el-icon><Plus /></el-icon>
            新增集群
          </el-button>
          <el-button circle :loading="loadingList" @click="loadClusters">
            <el-icon><Refresh /></el-icon>
          </el-button>
        </div>
        <el-table :data="clusters" v-loading="loadingList" border>
          <el-table-column prop="middleware" label="中间件" width="110" />
          <el-table-column prop="datacenter" label="机房" width="110" />
          <el-table-column prop="namesrv" label="连接地址" min-width="190" show-overflow-tooltip />
          <el-table-column prop="cluster_name" label="真实集群名（MCP 入参）" min-width="170" show-overflow-tooltip />
          <el-table-column prop="display_name" label="集群别名" min-width="140" show-overflow-tooltip />
          <el-table-column prop="description" label="说明" min-width="160" show-overflow-tooltip />
          <el-table-column label="启用" width="90">
            <template #default="{ row }">
              <el-switch :model-value="row.enabled" @change="(v: boolean) => toggleEnabled(row, v)" />
            </template>
          </el-table-column>
          <el-table-column prop="created_at" label="登记时间" min-width="160" />
          <el-table-column label="操作" width="140">
            <template #default="{ row }">
              <el-button size="small" @click="openEdit(row)">编辑</el-button>
              <el-button size="small" type="danger" @click="remove(row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
        <el-alert
          class="tip"
          type="info"
          :closable="false"
          title="namesrv 为权威连接地址：问答调用工具时按集群名解析后注入，MCP Server 据此直连真实集群；停用后不参与机房映射与反问。"
        />
      </el-card>

      <el-dialog
        v-model="dialogVisible"
        :title="editingId ? `编辑集群：${form.cluster_name}` : '新增集群登记'"
        width="520px"
        destroy-on-close
      >
        <el-form :model="form" label-width="110px">
          <el-form-item label="中间件" required>
            <el-select
              v-model="form.middleware"
              filterable
              allow-create
              placeholder="选择或输入中间件"
              class="mw-select"
            >
              <el-option v-for="m in middlewareOptions" :key="m" :label="m" :value="m" />
            </el-select>
            <div class="field-tip">该集群所属的中间件类型，选项来自系统 MIDDLEWARES 配置，可自行输入。</div>
          </el-form-item>
          <el-form-item label="机房" required>
            <el-input v-model="form.datacenter" placeholder="如 bj10" />
            <div class="field-tip">用户提问中的机房标识，问答按它映射到集群。</div>
          </el-form-item>
          <el-form-item label="连接地址">
            <el-input v-model="form.namesrv" placeholder="如 localhost:9876（RocketMQ 为 NameServer 地址）" />
            <div class="field-tip">该集群的真实连接地址，调用工具时由后端注入；同一地址下可登记多个集群。</div>
          </el-form-item>
          <el-form-item label="真实集群名" required>
            <el-input v-model="form.cluster_name" placeholder="如 Trans-Async-Cluster" />
            <div class="field-tip">
              broker 配置的 brokerClusterName，将作为 MCP 工具的 cluster 入参；
              不确定时可先通过 MCP Server 查询集群列表获取。
            </div>
          </el-form-item>
          <el-form-item label="集群别名">
            <el-input v-model="form.display_name" placeholder="如 交易异步集群（用户提问中使用的名字）" />
            <div class="field-tip">
              用户口中的业务名，可与真实集群名不同；留空则按真实集群名匹配。
              同一 NameServer 下有多个集群时建议填写以便区分。
            </div>
          </el-form-item>
          <el-form-item label="说明">
            <el-input v-model="form.description" type="textarea" :rows="2" placeholder="备注（可选）" />
          </el-form-item>
        </el-form>
        <template #footer>
          <el-button @click="dialogVisible = false">取消</el-button>
          <el-button type="primary" :loading="saving" @click="save">保存</el-button>
        </template>
      </el-dialog>
    </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { OfficeBuilding, Plus, Refresh } from "@element-plus/icons-vue";
import {
  createCluster,
  deleteCluster,
  listClusters,
  listMiddlewareOptions,
  updateCluster,
  type MiddlewareCluster,
} from "../../api/mcp";

const clusters = ref<MiddlewareCluster[]>([]);
const loadingList = ref(false);
const saving = ref(false);
const dialogVisible = ref(false);
const editingId = ref("");
const middlewareOptions = ref<string[]>([]);
const form = ref({
  middleware: "",
  cluster_name: "",
  display_name: "",
  datacenter: "",
  namesrv: "",
  description: "",
});

async function loadClusters(): Promise<void> {
  loadingList.value = true;
  try {
    clusters.value = await listClusters();
  } finally {
    loadingList.value = false;
  }
}

function openCreate(): void {
  editingId.value = "";
  form.value = {
    middleware: middlewareOptions.value[0] ?? "",
    cluster_name: "",
    display_name: "",
    datacenter: "",
    namesrv: "",
    description: "",
  };
  dialogVisible.value = true;
}

function openEdit(row: MiddlewareCluster): void {
  editingId.value = row.id;
  form.value = {
    middleware: row.middleware,
    cluster_name: row.cluster_name,
    display_name: row.display_name,
    datacenter: row.datacenter,
    namesrv: row.namesrv,
    description: row.description,
  };
  dialogVisible.value = true;
}

// 保存：新增或编辑，集群名/机房必填由前端预校验（后端兜底）
async function save(): Promise<void> {
  if (!form.value.middleware.trim() || !form.value.cluster_name.trim() || !form.value.datacenter.trim()) {
    ElMessage.warning("请填写中间件、真实集群名与机房");
    return;
  }
  saving.value = true;
  try {
    if (editingId.value) {
      await updateCluster(editingId.value, { ...form.value });
      ElMessage.success("集群登记已更新");
    } else {
      await createCluster({ ...form.value });
      ElMessage.success("集群登记已创建");
    }
    dialogVisible.value = false;
    await loadClusters();
  } finally {
    saving.value = false;
  }
}

async function toggleEnabled(row: MiddlewareCluster, enabled: boolean): Promise<void> {
  await updateCluster(row.id, { enabled });
  await loadClusters();
}

async function remove(row: MiddlewareCluster): Promise<void> {
  try {
    await ElMessageBox.confirm(
      `确定删除集群「${row.cluster_name}」的登记？删除后问答将无法按该集群查询。`,
      "删除集群登记",
      { confirmButtonText: "删除", cancelButtonText: "取消", type: "warning" },
    );
  } catch {
    return; // 用户取消
  }
  await deleteCluster(row.id);
  ElMessage.success("集群登记已删除");
  await loadClusters();
}

onMounted(async () => {
  await Promise.all([loadClusters(), loadMiddlewareOptions()]);
});

// 加载中间件选项（来自 MIDDLEWARES 配置），失败时留空由用户自填
async function loadMiddlewareOptions(): Promise<void> {
  try {
    middlewareOptions.value = await listMiddlewareOptions();
  } catch {
    middlewareOptions.value = [];
  }
}
</script>

<style scoped>
.page {
  padding: 24px;
}
.page-head {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 16px;
}
.page-head-icon {
  font-size: 28px;
  color: var(--el-color-primary);
}
.page-head-sub {
  margin: 2px 0 0;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}
.block {
  margin-bottom: 16px;
}
.toolbar {
  display: flex;
  gap: 8px;
  margin-bottom: 12px;
}
.tip {
  margin-top: 12px;
}
.mw-select {
  width: 100%;
}
.field-tip {
  width: 100%;
  margin-top: 4px;
  font-size: 12px;
  line-height: 1.5;
  color: var(--el-text-color-secondary);
}
</style>
