# 前端服务

Vue 3 + TypeScript + Vite + Element Plus + Pinia 的管理端与聊天界面。

## 启动（开发）

```bash
cd frontend
npm install
npm run dev          # http://localhost:5173，/api 自动代理到 8080
```

开发代理见 `vite.config.ts`：`/api` → `http://localhost:8080`。

## 构建

```bash
npm run build        # vue-tsc 类型检查 + vite build，产物在 dist/
```

生产镜像由 `Dockerfile` 多阶段构建：node 编译 → nginx 提供静态文件。`nginx.conf` 配置 SPA history 回退与 `/api` 反向代理（指向 compose 服务 `backend-api`）。

## 配置

| 变量 | 默认 | 说明 |
|---|---|---|
| `VITE_API_BASE_URL` | `/api` | 后端 API 基址，构建时注入 |

## 健康检查

前端为 nginx 静态站点，无独立 `/health` 端点；服务可用性以根路径返回 200 为准。
后端与 MCP Server 的健康检查见各自 README。
