import { readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath, URL } from "node:url";
import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";

const backend = "http://localhost:8080";

export default defineConfig({
  // Absolute base so hashed assets load from /assets/* on deep links like
  // /comic/abc123 (a relative base would resolve to /comic/assets/* and 404).
  base: "/",
  plugins: [react(), keepGitkeep(), assertCacheNames()],
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

/**
 * Fails the build when public/sw.js and src/offline/cacheNames.ts disagree.
 *
 * The worker is served verbatim out of public/ so that no build step stands
 * between it and the browser, which also means it cannot import the names it
 * shares with the page. Drift there is silent and nasty — the page fills one
 * cache and the worker reads another, so downloads simply never appear offline.
 * Cheaper to catch it here than in a bug report from someone on a train.
 */
function assertCacheNames(): Plugin {
  return {
    name: "longbox-assert-cache-names",
    buildStart() {
      const read = (path: string) => readFileSync(fileURLToPath(new URL(path, import.meta.url)), "utf8");
      const contract = read("./src/offline/cacheNames.ts");
      const worker = read("./public/sw.js");

      for (const name of ["SHELL_CACHE", "PAGE_CACHE"]) {
        const pattern = new RegExp(`const ${name} = "([^"]+)"`);
        const expected = contract.match(new RegExp(`export ${pattern.source}`))?.[1];
        const actual = worker.match(pattern)?.[1];
        if (!expected || !actual) {
          throw new Error(`assert-cache-names: could not read ${name} from both files`);
        }
        if (expected !== actual) {
          throw new Error(
            `assert-cache-names: ${name} is "${actual}" in public/sw.js but "${expected}" in ` +
              `src/offline/cacheNames.ts — the worker and the page must agree.`,
          );
        }
      }
    },
  };
}
