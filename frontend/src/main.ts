import { createApp } from "vue";
import App from "./App.vue";
import "./style.css";

const app = createApp(App);
app.mount("#app");

// 调试入口：排查时可以在控制台里直接拿到根组件实例。
window.__app = { app };
