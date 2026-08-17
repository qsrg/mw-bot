import { defineStore } from "pinia";
import { login as apiLogin, fetchMe } from "../api/auth";

// 认证状态：token、用户信息与登录/登出
export const useAuthStore = defineStore("auth", {
  state: () => ({
    token: localStorage.getItem("access_token") || "",
    username: localStorage.getItem("username") || "",
    role: localStorage.getItem("role") || "",
    permissions: [] as string[],
  }),
  getters: {
    isLoggedIn: (state) => Boolean(state.token),
    isAdmin: (state) => state.role === "admin",
  },
  actions: {
    // 登录并持久化 token 与用户信息
    async login(username: string, password: string): Promise<void> {
      const result = await apiLogin(username, password);
      this.token = result.access_token;
      this.username = result.username;
      this.role = result.role;
      localStorage.setItem("access_token", result.access_token);
      localStorage.setItem("username", result.username);
      localStorage.setItem("role", result.role);
    },
    // 拉取当前用户信息（含权限）
    async loadProfile(): Promise<void> {
      if (!this.token) return;
      try {
        const info = await fetchMe();
        this.username = info.username;
        this.role = info.role;
        this.permissions = info.permissions;
      } catch {
        // 加载失败时静默，由拦截器处理 401
      }
    },
    // 登出并清理本地态
    logout(): void {
      this.token = "";
      this.username = "";
      this.role = "";
      this.permissions = [];
      localStorage.removeItem("access_token");
      localStorage.removeItem("username");
      localStorage.removeItem("role");
    },
  },
});
