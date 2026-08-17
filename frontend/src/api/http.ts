import axios, { type AxiosInstance } from "axios";
import { ElMessage } from "element-plus";

const BASE_URL = import.meta.env.VITE_API_BASE_URL || "/api";
// 临近过期阈值：剩余有效时间不足 15 分钟则自动刷新
const REFRESH_THRESHOLD_SEC = 15 * 60;

// 统一 axios 实例：注入 token、临近过期自动续期、401 跳登录与错误提示
export const http: AxiosInstance = axios.create({
  baseURL: BASE_URL,
  timeout: 30000,
});

// 解析 JWT exp（秒级时间戳）；无法解析返回 null
function getTokenExp(token: string): number | null {
  try {
    const payload = JSON.parse(atob(token.split(".")[1]));
    return typeof payload.exp === "number" ? payload.exp : null;
  } catch {
    return null;
  }
}

// 清理登录态并跳转登录页
function clearAuthAndRedirect(): void {
  localStorage.removeItem("access_token");
  localStorage.removeItem("username");
  localStorage.removeItem("role");
  if (window.location.pathname !== "/login") {
    window.location.href = "/login";
  }
}

// 单例刷新 Promise：并发请求只触发一次刷新
let refreshPromise: Promise<string> | null = null;

// 用当前 token 调 /auth/refresh 换发新 token；走裸 axios 避免拦截器递归
async function refreshAccessToken(currentToken: string): Promise<string> {
  const resp = await axios.post(
    `${BASE_URL}/auth/refresh`,
    {},
    { headers: { Authorization: `Bearer ${currentToken}` } },
  );
  const newToken = resp.data?.access_token;
  if (!newToken) {
    throw new Error("刷新 token 失败");
  }
  localStorage.setItem("access_token", newToken);
  return newToken;
}

// 请求前确保 token 未临近过期；临近过期则先刷新
async function ensureFreshToken(): Promise<string | null> {
  const token = localStorage.getItem("access_token");
  if (!token) {
    return null;
  }
  const exp = getTokenExp(token);
  // 无法解析 exp 时不刷新，交由服务端校验
  if (!exp) {
    return token;
  }
  const remaining = exp - Date.now() / 1000;
  if (remaining > REFRESH_THRESHOLD_SEC) {
    return token;
  }
  // 临近过期：单例刷新，避免并发重复
  if (!refreshPromise) {
    refreshPromise = refreshAccessToken(token)
      .catch((err) => {
        clearAuthAndRedirect();
        throw err;
      })
      .finally(() => {
        refreshPromise = null;
      });
  }
  return refreshPromise;
}

// 请求拦截：附带 JWT，必要时先续期
http.interceptors.request.use(async (config) => {
  const token = await ensureFreshToken();
  if (token) {
    config.headers.Authorization = `Bearer ${token}`;
  }
  return config;
});

// 响应拦截：401 清理登录态并跳转，统一错误提示
http.interceptors.response.use(
  (response) => response,
  (error) => {
    const status = error.response?.status;
    if (status === 401) {
      clearAuthAndRedirect();
    }
    const detail = error.response?.data?.detail;
    const message =
      typeof detail === "string" ? detail : detail?.message || "请求失败，请稍后重试";
    ElMessage.error(message);
    return Promise.reject(error);
  },
);
