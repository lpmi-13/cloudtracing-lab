import { defineConfig } from "vite";

const backendURL = process.env.COACH_BACKEND_URL || "http://127.0.0.1:9000";
const host = process.env.COACH_UI_HOST || "127.0.0.1";
const port = Number(process.env.COACH_UI_PORT || "5173");

export default defineConfig({
  root: "cmd/coach/ui",
  server: {
    host,
    port,
    strictPort: true,
    proxy: {
      "/api": {
        target: backendURL,
        changeOrigin: true
      },
      "/favicon.ico": {
        target: backendURL,
        changeOrigin: true
      }
    }
  }
});
