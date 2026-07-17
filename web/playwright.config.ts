import { defineConfig, devices } from "@playwright/test";

const PORT = Number(process.env.DOWITCHER_E2E_PORT ?? 8099);

// localhost, not 127.0.0.1: it must match DOWITCHER_ORIGIN exactly (the WS
// allowlist), and a service worker only registers in a secure context, which
// localhost is and a bare IP is not.
const baseURL = `http://localhost:${PORT}`;

export default defineConfig({
  testDir: "./e2e",
  // These share one server and one library, and the offline specs reload the
  // page out from under a service worker. Run them one at a time rather than
  // reason about which parallel pair can see each other's downloads.
  workers: 1,
  fullyParallel: false,
  // Each test downloads a comic page by page over HTTP before it can begin.
  timeout: 60_000,
  reporter: process.env.CI ? "list" : "html",
  // The offline tests are the ones a flake would hide. A retry locally would
  // paper over a real race in the service worker's activation.
  retries: 0,
  use: {
    baseURL,
    serviceWorkers: "allow",
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: {
    command: "./e2e/serve.sh",
    url: baseURL,
    // Always a fresh server: it holds the SQLite DB and the library that
    // serve.sh just wiped, so an already-running one is a different app.
    reuseExistingServer: false,
    stdout: "pipe",
    stderr: "pipe",
    // Cold `go build` of the server plus the fixture, on a clean module cache.
    timeout: 120_000,
    env: { DOWITCHER_E2E_PORT: String(PORT) },
  },
});
