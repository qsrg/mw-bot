import { createRouter, createWebHistory, type RouteRecordRaw } from "vue-router";
import LoginView from "../views/LoginView.vue";
import { useAuthStore } from "../stores/auth";

// 路由表：/login 公开，其余需登录；/admin/* 需 admin 角色
const routes: RouteRecordRaw[] = [
  { path: "/", redirect: "/chat" },
  { path: "/login", name: "login", component: LoginView, meta: { public: true } },
  {
    path: "/chat",
    name: "chat",
    component: () => import("../views/chat/ChatView.vue"),
  },
  {
    path: "/admin/documents",
    name: "admin-documents",
    component: () => import("../views/admin/AdminDocumentsView.vue"),
    meta: { requiresAdmin: true },
  },
  {
    path: "/admin/tools",
    name: "admin-tools",
    component: () => import("../views/admin/AdminToolsView.vue"),
    meta: { requiresAdmin: true },
  },
  {
    path: "/admin/memories",
    name: "admin-memories",
    component: () => import("../views/admin/AdminMemoriesView.vue"),
    meta: { requiresAdmin: true },
  },
];

export const router = createRouter({
  history: createWebHistory(),
  routes,
});

// 全局前置守卫：未登录跳 /login，admin 路由校验角色
router.beforeEach((to) => {
  const auth = useAuthStore();
  if (to.meta.public) return true;
  if (!auth.isLoggedIn) {
    return { name: "login" };
  }
  if (to.meta.requiresAdmin && !auth.isAdmin) {
    return { name: "chat" };
  }
  return true;
});
