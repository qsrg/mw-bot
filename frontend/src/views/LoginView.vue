<template>
  <main class="login-page">
    <section class="brand-panel">
      <div class="brand-head">
        <span class="mw-dot"></span>
        <span class="mw-wordmark brand-name">mw-bot</span>
      </div>
      <p class="brand-line">企业内部智能问答系统</p>
      <p class="brand-desc">面向中间件与运维团队的问答控制台，答案附带知识库来源引用。</p>
      <ul class="brand-list" aria-label="已支持的中间件">
        <li v-for="mw in middlewares" :key="mw">
          <span class="mw-dot list-dot"></span>
          <span class="mw-wordmark">{{ mw }}</span>
        </li>
      </ul>
      <div class="brand-foot mw-wordmark">mw·bot / console</div>
    </section>
    <section class="form-panel">
      <el-card class="login-panel" shadow="never">
        <h1 class="login-title">登录</h1>
        <p class="login-sub">使用企业账号访问智能问答</p>
        <el-form :model="form" label-position="top" @submit.prevent="submit">
          <el-form-item label="用户名">
            <el-input
              v-model="form.username"
              autocomplete="username"
              placeholder="请输入用户名"
              :prefix-icon="User"
            />
          </el-form-item>
          <el-form-item label="密码">
            <el-input
              v-model="form.password"
              type="password"
              autocomplete="current-password"
              show-password
              placeholder="请输入密码"
              :prefix-icon="Lock"
            />
          </el-form-item>
          <el-button type="primary" native-type="submit" :loading="loading" class="login-btn">
            登录
          </el-button>
        </el-form>
      </el-card>
    </section>
  </main>
</template>

<script setup lang="ts">
import { reactive, ref } from "vue";
import { useRouter } from "vue-router";
import { Lock, User } from "@element-plus/icons-vue";
import { useAuthStore } from "../stores/auth";

const router = useRouter();
const auth = useAuthStore();
const loading = ref(false);

const form = reactive({ username: "", password: "" });

// 品牌面板展示的中间件清单，与后端 MIDDLEWARES 默认值对齐
const middlewares = ["rocketmq", "kafka", "rabbitmq", "pulsar", "redis", "nacos"];

// 提交登录
async function submit(): Promise<void> {
  if (!form.username || !form.password) return;
  loading.value = true;
  try {
    await auth.login(form.username, form.password);
    await router.push("/chat");
  } catch {
    // 错误提示由 axios 拦截器统一处理
  } finally {
    loading.value = false;
  }
}
</script>

<style scoped>
.login-page {
  display: flex;
  height: 100vh;
}
/* 左侧品牌面板：与侧栏同源的石墨蓝，承载字标与中间件清单 */
.brand-panel {
  display: flex;
  flex-direction: column;
  flex: 1 1 52%;
  padding: 48px 56px;
  background: linear-gradient(160deg, var(--mw-ink) 0%, var(--mw-ink-deep) 100%);
  color: var(--mw-ink-text);
}
.brand-head {
  display: flex;
  align-items: center;
  gap: 12px;
}
.brand-name {
  font-size: 28px;
  font-weight: 700;
  color: #f2f5f8;
}
.brand-line {
  margin: 14px 0 0;
  font-size: 17px;
  font-weight: 600;
  color: #e8edf2;
}
.brand-desc {
  margin: 8px 0 0;
  max-width: 26em;
  font-size: 13px;
  line-height: 1.8;
  color: var(--mw-ink-dim);
}
.brand-list {
  list-style: none;
  margin: 36px 0 0;
  padding: 0;
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px 24px;
}
.brand-list li {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 13px;
  color: var(--mw-ink-text);
}
.list-dot {
  width: 6px;
  height: 6px;
  box-shadow: none;
  opacity: 0.9;
}
.brand-foot {
  margin-top: auto;
  font-size: 11px;
  color: var(--mw-ink-dim);
}
/* 右侧表单区 */
.form-panel {
  display: flex;
  align-items: center;
  justify-content: center;
  flex: 1 1 48%;
  padding: 24px;
  background: var(--mw-paper);
}
.login-panel {
  width: 360px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 10px;
}
.login-title {
  margin: 4px 0 0;
  font-size: 22px;
}
.login-sub {
  margin: 6px 0 20px;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}
.login-btn {
  width: 100%;
  margin-top: 4px;
}
/* 窄屏：品牌面板退化为顶部横条，只留字标一行 */
@media (max-width: 700px) {
  .login-page {
    flex-direction: column;
  }
  .brand-panel {
    flex: none;
    padding: 24px;
  }
  .brand-desc,
  .brand-list,
  .brand-foot {
    display: none;
  }
  .brand-line {
    margin-top: 10px;
  }
}
</style>
