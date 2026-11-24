import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

// 构建产物固定为 frontend/dist，由仓库根目录的 web.go 用 go:embed 打进二进制。
// 控制台挂在 /admin 下，因此 base 是 /admin/：产物里的资源地址都带这个前缀，
// 与 api 层的静态路由一一对应。
export default defineConfig({
  base: "/admin/",
  plugins: [vue()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    target: "chrome120",
  },
  server: {
    port: 9246,
    strictPort: true,
    proxy: {
      "/api": { target: "http://127.0.0.1:8088", changeOrigin: true },
    },
  },
});
