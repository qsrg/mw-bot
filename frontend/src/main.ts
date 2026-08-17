import { createApp } from "vue";
import { createPinia } from "pinia";
import ElementPlus from "element-plus";
import "element-plus/dist/index.css";
import "./styles/theme.css";
import App from "./App.vue";
import { router } from "./router";

// 应用入口：注册 Pinia、路由与 Element Plus
createApp(App).use(createPinia()).use(router).use(ElementPlus).mount("#app");
