// Cache and IndexedDB names shared by the service worker and the page.
//
// Both halves address the same storage: the download manager fills PAGE_CACHE
// from the page (the Cache API is available in window scope, so downloading
// does not need to round-trip through the worker), and the worker reads it back
// to answer page requests offline. They agree here so neither can drift onto a
// name the other never reads.
//
// The version suffix is the migration mechanism: bumping it orphans the old
// cache, which the worker deletes on activate. Bump SHELL_CACHE freely — it
// only costs a re-download of the bundle. Bumping PAGE_CACHE throws away
// comics the user deliberately downloaded, so it needs a real reason.
export const SHELL_CACHE = "dowitcher-shell-v1";
export const PAGE_CACHE = "dowitcher-pages-v1";

export const DB_NAME = "dowitcher-offline";
export const DB_VERSION = 1;

// Object stores in DB_NAME.
//
// STORE_DOWNLOADS is the manifest of what PAGE_CACHE is supposed to hold: the
// Cache API can say whether one URL is present but not whether a comic is
// wholly downloaded, so the manifest tracks that separately. It is the source
// of truth for the downloads UI.
export const STORE_DOWNLOADS = "downloads";
// STORE_PROGRESS_QUEUE holds progress writes made while offline, replayed on
// reconnect.
export const STORE_PROGRESS_QUEUE = "progressQueue";
// STORE_COMICS caches comic metadata and page lists so the library and the
// reader can render without the network.
export const STORE_COMICS = "comics";

// pageURL is the canonical cache key for one page's bytes. It matches the
// server route so a cache hit and a network fetch are the same request.
export function pageURL(comicID: string, n: number): string {
  return `/api/comics/${comicID}/pages/${n}`;
}

export function coverURL(comicID: string): string {
  return `/api/comics/${comicID}/cover`;
}
