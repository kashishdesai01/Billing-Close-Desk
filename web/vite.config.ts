import { defineConfig } from "vite";
export default defineConfig({
  build: { outDir: "../cmd/desk/static", emptyOutDir: true },
  server: {
    proxy: {
      "/api": "http://127.0.0.1:8090",
      "/auth": "http://127.0.0.1:8090",
    },
  },
});
