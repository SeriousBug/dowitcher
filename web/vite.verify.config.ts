// TEMPORARY verification-only config; delete when done. The committed
// vite.config.ts points at :8080, where a sibling agent's mock is listening.
import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  base: "/",
  plugins: [react()],
  resolve: {
    alias: { "styled-system": fileURLToPath(new URL("./styled-system", import.meta.url)) },
  },
  server: {
    port: 5199,
    strictPort: true,
    proxy: {
      "/api": { target: "http://localhost:8081", changeOrigin: true },
      "/auth": { target: "http://localhost:8081", changeOrigin: true },
      "/ws": { target: "http://localhost:8081", changeOrigin: true, ws: true },
    },
  },
});
