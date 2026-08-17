/// <reference types="vite/client" />

// 允许在 TS 中导入 .vue 单文件组件
declare module "*.vue" {
  import type { DefineComponent } from "vue";
  const component: DefineComponent<Record<string, never>, Record<string, never>, unknown>;
  export default component;
}
