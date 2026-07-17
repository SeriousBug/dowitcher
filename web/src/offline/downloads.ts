import { PAGE_CACHE, STORE_COMICS, STORE_DOWNLOADS, STORE_PROGRESS_QUEUE, coverURL, pageURL } from "./cacheNames";
import { idbClear, idbDelete, idbGetAll, idbPut, idbTry } from "./db";
import { cacheComicDetail, forgetComicDetail } from "./metaCache";
import { requestPersistentStorage } from "./persist";
import { http } from "../api/http";
import { toaster } from "../lib/toaster";
import { comicLabel } from "../lib/format";
import type { ComicDetail } from "../api/generated";

/**
 * Per-comic downloads, driven from the page.
 *
 * The Cache API is available in window scope, so the download loop lives here
 * and not in the service worker: the worker only ever reads PAGE_CACHE back.
 * That keeps the two halves independent — the contract between them is just
 * "the bytes are in PAGE_CACHE under pageURL()" — and it means downloading
 * works before the worker has ever activated.
 *
 * The worker deliberately never writes to PAGE_CACHE. Nothing else may either:
 * the moment incidental reads start populating it, STORE_DOWNLOADS stops being
 * a truthful account of what is on disk and the downloads UI starts lying.
 */

/** One row of the downloads manifest. The source of truth for the UI. */
export interface DownloadRecord {
  comicId: string;
  title: string;
  pageCount: number;
  pagesDownloaded: number;
  complete: boolean;
  /** Bytes actually written, accumulated as pages land. */
  bytes: number;
  downloadedAt: number;
}

/** A manifest row plus the parts that only matter while the tab is open. */
export interface DownloadState extends DownloadRecord {
  active: boolean;
  error?: string;
}

const records = new Map<string, DownloadState>();
const listeners = new Set<() => void>();
const controllers = new Map<string, AbortController>();

// useSyncExternalStore compares snapshots by identity, so it must be a stable
// object between emits and a fresh one after.
let snapshot: ReadonlyMap<string, DownloadState> = new Map();

function emit() {
  snapshot = new Map(records);
  for (const l of listeners) l();
}

export function subscribeDownloads(fn: () => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

export function downloadsSnapshot(): ReadonlyMap<string, DownloadState> {
  return snapshot;
}

/** Reads the manifest off disk into memory. Call once on app start. */
export async function hydrateDownloads(): Promise<void> {
  const rows = await idbTry(idbGetAll<DownloadRecord>(STORE_DOWNLOADS), []);
  for (const row of rows) {
    // A row left active by a torn-down tab is not active now. It stays
    // incomplete, which is exactly what the resume path reads.
    records.set(row.comicId, { ...row, active: false });
  }
  emit();
}

function persist(state: DownloadState): Promise<void> {
  const { active: _active, error: _error, ...row } = state;
  return idbTry(idbPut(STORE_DOWNLOADS, row), undefined);
}

function update(comicID: string, patch: Partial<DownloadState>) {
  const prev = records.get(comicID);
  if (!prev) return;
  records.set(comicID, { ...prev, ...patch });
  emit();
}

function isQuotaError(err: unknown): boolean {
  return err instanceof DOMException && err.name === "QuotaExceededError";
}

/**
 * Fetches one URL and writes the bytes into PAGE_CACHE, returning their size.
 *
 * The body is read to a blob rather than handed straight to cache.put because
 * the size is otherwise unknowable: Content-Length is absent on a chunked
 * response, and reading the cached entry back afterwards would double the work.
 */
async function cacheOne(cache: Cache, url: string, signal: AbortSignal): Promise<number> {
  const hit = await cache.match(url);
  // Resume: a page already on disk is a page not worth re-fetching. Its bytes
  // are already counted in the persisted record.
  if (hit) return 0;

  const res = await fetch(url, { credentials: "include", signal });
  if (!res.ok) throw new Error(`Page request failed with ${res.status}`);
  const blob = await res.blob();
  // Rebuild the response around the drained body; the original is spent.
  await cache.put(url, new Response(blob, { status: 200, headers: res.headers }));
  return blob.size;
}

/**
 * Downloads every page of a comic into PAGE_CACHE, plus its cover.
 *
 * Resumable and cancellable, because a 300-page comic on a phone will be
 * interrupted: progress is persisted per page, already-cached pages are
 * skipped, and calling this again on an incomplete comic picks up where it
 * stopped.
 */
export async function downloadComic(comicID: string): Promise<void> {
  if (controllers.has(comicID)) return;
  if (!("caches" in globalThis)) {
    toaster.create({
      type: "error",
      title: "Downloads aren't available here",
      description: "This browser only allows offline storage over HTTPS or on localhost.",
    });
    return;
  }

  const controller = new AbortController();
  controllers.set(comicID, controller);

  try {
    // Without this, the origin is evictable: Chrome drops least-recently-used
    // origins under disk pressure, and Safari clears storage for origins left
    // unvisited for seven days. Asking here rather than at app start means the
    // prompt (where there is one) lands on a deliberate act the user just made.
    await requestPersistentStorage();

    const detail = await http.get<ComicDetail>(`/api/comics/${comicID}`, { signal: controller.signal });
    // The reader needs the page list to render the bytes we are about to store.
    await cacheComicDetail(detail);

    const title = comicLabel(detail.comic);
    const pageCount = detail.pages.length;
    const existing = records.get(comicID);
    const state: DownloadState = {
      comicId: comicID,
      title,
      pageCount,
      pagesDownloaded: existing?.pagesDownloaded ?? 0,
      complete: false,
      bytes: existing?.bytes ?? 0,
      downloadedAt: existing?.downloadedAt ?? Math.floor(Date.now() / 1000),
      active: true,
      error: undefined,
    };
    records.set(comicID, state);
    emit();
    await persist(state);

    const cache = await caches.open(PAGE_CACHE);

    let bytes = state.bytes;
    let done = 0;
    for (let i = 0; i < pageCount; i++) {
      bytes += await cacheOne(cache, pageURL(comicID, i), controller.signal);
      done = i + 1;
      update(comicID, { pagesDownloaded: done, bytes });
      // Persisted per page so an interrupted download resumes with an accurate
      // count instead of re-deriving it from the cache one match at a time.
      await persist(records.get(comicID)!);
    }

    // The cover last: the pages are the point, and a comic with every page and
    // no thumbnail is still readable.
    bytes += await cacheOne(cache, coverURL(comicID), controller.signal);

    update(comicID, {
      pagesDownloaded: done,
      bytes,
      complete: true,
      active: false,
      downloadedAt: Math.floor(Date.now() / 1000),
    });
    await persist(records.get(comicID)!);
    toaster.create({ type: "success", title: `${title} is ready to read offline` });
  } catch (err) {
    if (controller.signal.aborted) {
      // Cancelling is a choice, not a failure. What landed stays on disk and
      // the row stays incomplete, so the next attempt resumes.
      update(comicID, { active: false });
      return;
    }
    if (isQuotaError(err)) {
      update(comicID, { active: false, error: "Ran out of storage space." });
      toaster.create({
        type: "error",
        title: "Out of storage space",
        description:
          "There wasn't room for the whole comic. Delete a download you've finished and try again — what got saved so far is kept.",
      });
      return;
    }
    const message = err instanceof Error ? err.message : "The download stopped unexpectedly.";
    update(comicID, { active: false, error: message });
    toaster.create({
      type: "error",
      title: "That download stopped",
      description: navigator.onLine
        ? message
        : "You went offline part-way through. It'll pick up where it left off.",
    });
  } finally {
    controllers.delete(comicID);
  }
}

export function cancelDownload(comicID: string): void {
  controllers.get(comicID)?.abort();
}

/** Purges a comic's bytes from PAGE_CACHE and its row from the manifest. */
export async function deleteDownload(comicID: string): Promise<void> {
  cancelDownload(comicID);
  const state = records.get(comicID);
  if ("caches" in globalThis) {
    const cache = await caches.open(PAGE_CACHE);
    // Walk the whole recorded page count, not pagesDownloaded: a resumed
    // download can have holes, and deleting a key that isn't there is free.
    const count = state?.pageCount ?? 0;
    await Promise.all([
      ...Array.from({ length: count }, (_, i) => cache.delete(pageURL(comicID, i))),
      cache.delete(coverURL(comicID)),
    ]);
  }
  records.delete(comicID);
  emit();
  await idbTry(idbDelete(STORE_DOWNLOADS, comicID), undefined);
  await forgetComicDetail(comicID);
}

/**
 * Everything this user's session put on disk.
 *
 * Offline, the client is necessarily the authority on what it may show — the
 * bytes are local and the server is unreachable to ask. Downloading is a
 * deliberate act, so that trade is accepted, but it only holds while the
 * session does: signing out must leave nothing readable behind.
 *
 * SHELL_CACHE is left alone on purpose. It holds the app bundle, which is the
 * same public JavaScript any logged-out visitor is served.
 */
export async function clearOfflineData(): Promise<void> {
  for (const c of controllers.values()) c.abort();
  controllers.clear();
  records.clear();
  emit();
  if ("caches" in globalThis) await caches.delete(PAGE_CACHE).catch(() => false);
  await idbTry(idbClear(STORE_DOWNLOADS), undefined);
  await idbTry(idbClear(STORE_COMICS), undefined);
  await idbTry(idbClear(STORE_PROGRESS_QUEUE), undefined);
}

/**
 * Bytes this origin is using, or null where the browser won't say.
 *
 * Only `usage` is read. `quota` is deliberately ignored: since Chrome 133 it
 * reports a synthetic `usage + 10 GiB` on desktop and Android as an
 * anti-fingerprinting measure, so "space remaining" computed from it is
 * fiction — it says 10 GiB free on a full disk. The real limit (60% of disk
 * per origin) is unchanged and still enforced; it just isn't reportable. A
 * download that runs out of room finds out via QuotaExceededError, which is
 * why that path is handled explicitly above rather than pre-empted here.
 */
export async function storageUsage(): Promise<number | null> {
  if (!navigator.storage?.estimate) return null;
  try {
    const { usage } = await navigator.storage.estimate();
    return usage ?? null;
  } catch {
    return null;
  }
}
