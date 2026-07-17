import { writeFileSync } from "node:fs";
import { fileURLToPath, URL } from "node:url";
import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";

const backend = "http://localhost:8080";

export default defineConfig({
  // Absolute base so hashed assets load from /assets/* on deep links like
  // /comic/abc123 (a relative base would resolve to /comic/assets/* and 404).
  base: "/",
  plugins: [react(), keepGitkeep()],
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

/**
 * Puts dist/.gitkeep back after emptyOutDir wipes it.
 *
 * The file is tracked so that `//go:embed all:dist` still compiles on a fresh
 * clone, where no build has run and the directory would otherwise not exist.
 * Building then deletes it and leaves the tree dirty, which reads as an
 * accidental deletion every single time. Restoring it here keeps both halves
 * true rather than asking everyone to remember.
 */
function keepGitkeep(): Plugin {
  return {
    name: "longbox-keep-gitkeep",
    closeBundle() {
      writeFileSync(fileURLToPath(new URL("./dist/.gitkeep", import.meta.url)), "");
    },
  };
}
