import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { css } from "styled-system/css";
import { InstallPrompt } from "./components/InstallPrompt";
import { OfflineIndicator } from "./components/OfflineIndicator";
import { toaster } from "./lib/toaster";
import { router } from "./router";
import "./index.css";

const queryClient = new QueryClient();

const rootEl = document.getElementById("root");
if (!rootEl) throw new Error("#root element not found");

createRoot(rootEl).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
      <InstallPrompt />
      <OfflineIndicator />
    </QueryClientProvider>
  </StrictMode>,
);

registerServiceWorker();

/**
 * Registers the worker that makes the app launchable offline.
 *
 * Everything here is best-effort by design. `navigator.serviceWorker` is simply
 * absent outside a secure context, and a self-hosted Dowitcher on a LAN over
 * plain http is a normal way to run it — that install stays exactly as it is
 * today, online-only, with InstallPrompt explaining why rather than the feature
 * vanishing without comment.
 */
function registerServiceWorker() {
  // Dev is deliberately left uncontrolled: a worker holding onto /assets/*
  // fights the dev server over what the current bundle is. Test the offline
  // path against a build (`pnpm build` + the Go binary, or `pnpm preview`).
  if (import.meta.env.DEV) return;
  if (!("serviceWorker" in navigator)) return;

  window.addEventListener("load", () => {
    void navigator.serviceWorker.register("/sw.js").then(
      (reg) => watchForUpdate(reg),
      (err) => console.warn("service worker registration failed, offline reading is off", err),
    );
  });
}

function watchForUpdate(reg: ServiceWorkerRegistration) {
  // A worker can already be waiting from a previous visit that never reloaded.
  if (reg.waiting && navigator.serviceWorker.controller) offerUpdate(reg.waiting);

  reg.addEventListener("updatefound", () => {
    const installing = reg.installing;
    if (!installing) return;

    installing.addEventListener("statechange", () => {
      // No controller means this is the first install, not an update: there is
      // no old version to replace and nothing worth interrupting anyone over.
      if (installing.state === "installed" && navigator.serviceWorker.controller) {
        offerUpdate(installing);
      }
    });
  });
}

let updateOffered = false;

/**
 * Asks before swapping versions instead of calling skipWaiting on sight.
 *
 * The waiting worker only takes over once every tab of the old one is gone, so
 * activating it early means reloading under whoever is mid-page. The new
 * version can wait; the reader cannot be un-interrupted.
 */
function offerUpdate(waiting: ServiceWorker) {
  if (updateOffered) return;
  updateOffered = true;

  toaster.create({
    type: "info",
    title: "A new version of Dowitcher is ready",
    // No duration: this outlives a comic, and the reader has no ToasterView, so
    // an expiring toast could tick away while nobody could have seen it.
    duration: Infinity,
    description: (
      <span>
        Reload when you reach a good stopping point.{" "}
        <button
          type="button"
          onClick={() => applyUpdate(waiting)}
          className={css({
            color: "accent",
            fontWeight: "semibold",
            cursor: "pointer",
            textDecoration: "underline",
            _hover: { color: "accentHover" },
          })}
        >
          Reload now
        </button>
      </span>
    ),
  });
}

let reloading = false;

function applyUpdate(waiting: ServiceWorker) {
  navigator.serviceWorker.addEventListener("controllerchange", () => {
    // Fires once per takeover, but a browser that repeats it must not put us in
    // a reload loop.
    if (reloading) return;
    reloading = true;
    window.location.reload();
  });
  waiting.postMessage({ type: "SKIP_WAITING" });
}
