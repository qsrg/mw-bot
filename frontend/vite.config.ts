import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import { fileURLToPath, URL } from "node:url";

// Vite 配置：启用 Vue 插件与路径别名，开发代理转发到后端
export default defineConfig({
  plugins: [vue()],
  resolve: {
    // .ts/.tsx 优先于 .js：避免 src 下 tsc 残留 .js 产物遮蔽 .ts 源码
    extensions: [".ts", ".tsx", ".mjs", ".js", ".jsx", ".json"],
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  server: {
    host: "0.0.0.0",
    port: 5173,
    proxy: {
      "/api": {
        target: "http://localhost:8080",
        changeOrigin: true,
      },
    },
  },
});
