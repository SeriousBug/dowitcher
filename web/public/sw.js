// Dowitcher service worker: makes the app shell launchable offline and serves
// downloaded comic pages back out of the cache the download manager filled.
//
// Plain JS in public/ rather than a bundled entry: Vite copies this file through
// untouched, so the same worker is served in dev and in the Go binary with no
// build step, no hashed filename, and no precache manifest to keep in sync. The
// cost is that it cannot import src/offline/cacheNames.ts, so the two cache
// names are repeated below — the `assertCacheNames` plugin in vite.config.ts
// fails the build if they ever drift from the contract.
const SHELL_CACHE = "dowitcher-shell-v1";
const PAGE_CACHE = "dowitcher-pages-v1";

const KEEP = [SHELL_CACHE, PAGE_CACHE];

// Every route is served by the same SPA document, so one cached copy answers a
// cold launch at /, /comic/abc123, or anywhere else.
const SHELL_URL = "/index.html";

const PAGE_OR_COVER = /^\/api\/comics\/[^/]+\/(pages\/\d+|cover)$/;

self.addEventListener("install", (event) => {
  // Seeding the shell here means a launch works offline even if the user never
  // navigates again after this worker installs. No skipWaiting: an update
  // swapped in under a reader mid-page is the thing we are avoiding.
  event.waitUntil(
    caches.open(SHELL_CACHE).then((cache) => cache.add(new Request(SHELL_URL, { cache: "reload" }))),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    (async () => {
      const names = await caches.keys();
      await Promise.all(names.filter((name) => !KEEP.includes(name)).map((name) => caches.delete(name)));
      // Take over tabs that loaded before this worker existed, so the first
      // launch after install is already offline-capable.
      await self.clients.claim();
    })(),
  );
});

self.addEventListener("message", (event) => {
  // The page asks for this only after the user agrees to reload.
  if (event.data?.type === "SKIP_WAITING") self.skipWaiting();
});

self.addEventListener("fetch", (event) => {
  const req = event.request;
  if (req.method !== "GET") return;

  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;

  if (req.mode === "navigate") {
    event.respondWith(shellNetworkFirst(req));
    return;
  }

  if (url.pathname.startsWith("/assets/")) {
    event.respondWith(assetCacheFirst(req));
    return;
  }

  if (PAGE_OR_COVER.test(url.pathname)) {
    event.respondWith(pageReadThrough(req));
    return;
  }

  // Everything else — /api/*, /auth/*, /ws — falls through to the network
  // untouched. A stale answer to "what is on my shelf" or a replayed auth
  // request is worse than no answer.
});

/**
 * Network-first, cached shell as the fallback.
 *
 * Never cache-first: the document names the hashed bundle, so pinning it to a
 * cached copy pins the user to whatever version they first loaded, and a
 * self-hosted server that just got updated could never hand them the new one.
 */
async function shellNetworkFirst(req) {
  try {
    const res = await fetch(req);
    // The mirror of the check in assetCacheFirst: only a real document is worth
    // keeping as the shell. A proxy's sign-in page or an error body saved here
    // would become what the app launches into offline.
    if (res.ok && isHTML(res)) {
      // Keyed by SHELL_URL, not by the request: every deep link returns the same
      // document, and storing one per visited URL would just be copies.
      const cache = await caches.open(SHELL_CACHE);
      await cache.put(SHELL_URL, res.clone());
    }
    return res;
  } catch {
    const cached = await caches.match(SHELL_URL, { cacheName: SHELL_CACHE });
    if (cached) return cached;
    return new Response("Dowitcher is offline and no cached copy is available.", {
      status: 503,
      headers: { "Content-Type": "text/plain" },
    });
  }
}

/** Cache-first: Vite's /assets/* names are content hashes, so they never change meaning. */
async function assetCacheFirst(req) {
  const cache = await caches.open(SHELL_CACHE);
  const cached = await cache.match(req);
  if (cached) return cached;

  const res = await fetch(req);
  // A 200 is not enough to trust. The server answers any unknown path with the
  // SPA shell, so an asset deleted by a server update comes back as index.html
  // with a 200 — and cache-first means that HTML would sit under a .js key
  // forever, breaking the app until someone cleared their storage by hand.
  // Nothing under /assets/ is ever a document, so an HTML answer here means the
  // asset is gone, not that we found it.
  if (res.ok && !isHTML(res)) await cache.put(req, res.clone());
  return res;
}

function isHTML(res) {
  return (res.headers.get("Content-Type") || "").includes("text/html");
}

/**
 * Read-through from PAGE_CACHE, network on a miss, and **never a write**.
 *
 * The download manager on the page owns what lives in PAGE_CACHE, and the
 * downloads manifest in IndexedDB is what the UI reports as "downloaded". If
 * this worker also wrote every page it proxied, both of those would become
 * lies: the cache would fill with comics nobody asked to keep, quota eviction
 * would start dropping deliberate downloads to make room for accidental ones,
 * and deleting a download would leave its pages behind. Reading is enough —
 * whatever the download manager put here answers offline, and anything else is
 * simply a network request.
 */
async function pageReadThrough(req) {
  const cache = await caches.open(PAGE_CACHE);
  // ignoreVary: the download manager stores a plain fetch of the same URL, and a
  // Vary header on the stored response should not stop that copy from matching.
  //
  // ignoreSearch: the reader retries a failed page as ?retry=1 to get past a bad
  // HTTP-cache entry, but the download manager stores the clean URL. Matching on
  // the full URL would miss every retry, so a page that failed once could never
  // recover offline — the one moment the retry exists for. A hit here is a
  // deliberate download, so there is no stale copy to bust anyway.
  const cached = await cache.match(req, { ignoreVary: true, ignoreSearch: true });
  if (cached) return cached;
  return fetch(req);
}
