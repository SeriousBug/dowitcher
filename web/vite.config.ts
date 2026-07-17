import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

const backend = "http://localhost:8080";

export default defineConfig({
  // Absolute base so hashed assets load from /assets/* on deep links like
  // /comic/abc123 (a relative base would resolve to /comic/assets/* and 404).
  base: "/",
  plugins: [react()],
  resolve: {
    alias: {
      "styled-system": fileURLToPath(new URL("./styled-system", import.meta.url)),
    },
  },
  server: {
    proxy: {
      "/api": { target: backend, changeOrigin: true },
      "/auth": { target: backend, changeOrigin: true },
      "/ws": { target: backend, changeOrigin: true, ws: true },
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
