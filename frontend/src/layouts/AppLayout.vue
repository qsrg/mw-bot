<template>
  <el-container class="app-layout">
    <el-aside width="220px" class="app-aside">
      <div class="brand">
        <span class="mw-dot brand-dot"></span>
        <span class="mw-wordmark brand-name">mw-bot</span>
      </div>
      <div class="brand-sub">企业内部智能问答</div>
      <el-menu :default-active="activeMenu" router class="aside-menu">
        <el-menu-item index="/chat">
          <el-icon><ChatDotRound /></el-icon>
          <span>智能问答</span>
        </el-menu-item>
      </el-menu>
      <div v-if="auth.isAdmin" class="menu-group">管理</div>
      <el-menu v-if="auth.isAdmin" :default-active="activeMenu" router class="aside-menu">
        <el-menu-item index="/admin/documents">
          <el-icon><Document /></el-icon>
          <span>文档管理</span>
        </el-menu-item>
        <el-menu-item index="/admin/tools">
          <el-icon><SetUp /></el-icon>
          <span>工具管理</span>
        </el-menu-item>
        <el-menu-item index="/admin/memories">
          <el-icon><Collection /></el-icon>
          <span>记忆管理</span>
        </el-menu-item>
      </el-menu>
      <div class="aside-foot mw-wordmark">mw·bot / console</div>
    </el-aside>
    <el-container class="app-body">
      <el-header class="app-header">
        <div class="app-user">
          <span class="avatar" aria-hidden="true">{{ avatarChar }}</span>
          <span class="user-name">{{ auth.username }}</span>
          <el-tag size="small" :type="auth.isAdmin ? 'danger' : 'info'" effect="plain">
            {{ auth.isAdmin ? "管理员" : "用户" }}
          </el-tag>
        </div>
        <el-button link @click="onLogout">
          <el-icon class="logout-icon"><SwitchButton /></el-icon>
          退出登录
        </el-button>
      </el-header>
      <el-main class="app-main">
        <slot />
      </el-main>
    </el-container>
  </el-container>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { useRoute, useRouter } from "vue-router";
import {
  ChatDotRound,
  Collection,
  Document,
  SetUp,
  SwitchButton,
} from "@element-plus/icons-vue";
import { useAuthStore } from "../stores/auth";

const route = useRoute();
const router = useRouter();
const auth = useAuthStore();

// 当前激活的菜单项
const activeMenu = computed(() => route.path);

// 头像取用户名首字符
const avatarChar = computed(() => (auth.username || "?").charAt(0).toUpperCase());

function onLogout(): void {
  auth.logout();
  router.push({ name: "login" });
}
</script>

<style scoped>
.app-layout {
  height: 100vh;
  /* 兜底：防止内部任何元素被内容撑破 100vh 触发 body 整页滚动 */
  overflow: hidden;
}
/* 深色石墨蓝侧栏，纵向 flex 让品牌、菜单、底部标注各归其位 */
.app-aside {
  display: flex;
  flex-direction: column;
  background: linear-gradient(180deg, var(--mw-ink) 0%, var(--mw-ink-deep) 100%);
  color: var(--mw-ink-text);
}
.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 20px 20px 0;
}
.brand-dot {
  flex-shrink: 0;
}
.brand-name {
  font-size: 20px;
  font-weight: 700;
  color: #f2f5f8;
}
.brand-sub {
  padding: 4px 20px 16px;
  font-size: 12px;
  color: var(--mw-ink-dim);
  border-bottom: 1px solid var(--mw-ink-line);
}
.menu-group {
  padding: 14px 20px 6px;
  font-size: 11px;
  letter-spacing: 0.14em;
  color: var(--mw-ink-dim);
}
/* 深色面板上的菜单：去掉默认边线，激活项以左侧信号条 + 微亮底色标识 */
.aside-menu {
  --el-menu-bg-color: transparent;
  --el-menu-text-color: var(--mw-ink-text);
  --el-menu-hover-bg-color: rgba(255, 255, 255, 0.06);
  --el-menu-active-color: #ffffff;
  border-right: none;
  padding: 4px 8px;
}
.aside-menu:first-of-type {
  padding-top: 10px;
}
.aside-menu :deep(.el-menu-item) {
  border-radius: 6px;
  margin: 2px 0;
}
.aside-menu :deep(.el-menu-item.is-active) {
  background: rgba(14, 128, 116, 0.28);
  box-shadow: inset 3px 0 0 var(--mw-signal);
}
.aside-foot {
  margin-top: auto;
  padding: 14px 20px;
  font-size: 11px;
  color: var(--mw-ink-dim);
  border-top: 1px solid var(--mw-ink-line);
}
.app-body {
  /* 关键：el-container 默认只有 min-width:0，没有 min-height:0。
     在 .app-layout 的 column flex 中作为 flex item，min-height:auto 会被
     内部内容（如聊天记录）撑开，撑破 100vh 触发整页滚动。
     设为 0 让它被父级 flex:1 约束，内部溢出交给 .app-main 处理。 */
  min-height: 0;
  background: var(--mw-paper);
}
.app-header {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 20px;
  background: #ffffff;
  border-bottom: 1px solid var(--el-border-color-light);
}
.app-user {
  display: flex;
  align-items: center;
  gap: 10px;
}
.avatar {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  border-radius: 50%;
  background: var(--mw-signal-soft);
  color: var(--mw-signal-strong);
  font-size: 14px;
  font-weight: 600;
}
.user-name {
  font-weight: 500;
}
.logout-icon {
  margin-right: 4px;
}
.app-main {
  padding: 0;
  overflow: hidden;
}
</style>
