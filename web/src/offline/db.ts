import {
  DB_NAME,
  DB_VERSION,
  STORE_COMICS,
  STORE_DOWNLOADS,
  STORE_PROGRESS_QUEUE,
  STORE_SESSION,
} from "./cacheNames";

/**
 * The raw IndexedDB API, wrapped just far enough to be usable. No idb/dexie:
 * the schema is three stores with no indexes and no queries beyond "by key",
 * which is the one shape the raw API is already adequate for. A dependency
 * would buy nothing and ship in every bundle.
 *
 * Every store is keyed on its record's own id rather than an auto-increment
 * key. For STORE_PROGRESS_QUEUE that is what collapses the queue: a page turn
 * overwrites the comic's pending row instead of stacking a hundred of them, so
 * only the newest position per comic ever replays.
 */

let dbPromise: Promise<IDBDatabase> | null = null;

export function openDB(): Promise<IDBDatabase> {
  if (dbPromise) return dbPromise;
  dbPromise = new Promise<IDBDatabase>((resolve, reject) => {
    if (!("indexedDB" in globalThis)) {
      reject(new Error("This browser has no IndexedDB, so offline reading is unavailable."));
      return;
    }
    const req = indexedDB.open(DB_NAME, DB_VERSION);
    req.onupgradeneeded = () => {
      const db = req.result;
      // Guarded per store rather than switched on oldVersion: a browser that
      // dropped the database (Safari evicts unused origins after seven days)
      // reopens at the current version with nothing in it, and a version-gated
      // upgrade would skip the creates and leave every read failing.
      if (!db.objectStoreNames.contains(STORE_DOWNLOADS)) {
        db.createObjectStore(STORE_DOWNLOADS, { keyPath: "comicId" });
      }
      if (!db.objectStoreNames.contains(STORE_PROGRESS_QUEUE)) {
        db.createObjectStore(STORE_PROGRESS_QUEUE, { keyPath: "comicId" });
      }
      // Out-of-line keys: this store holds both library pages and comic
      // details, which share no key path. Callers namespace their own keys.
      if (!db.objectStoreNames.contains(STORE_COMICS)) {
        db.createObjectStore(STORE_COMICS);
      }
      if (!db.objectStoreNames.contains(STORE_SESSION)) {
        db.createObjectStore(STORE_SESSION);
      }
    };
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error ?? new Error("Couldn't open the offline database."));
    // Another tab holds an older version open. Nothing to do but let that tab
    // win; retrying here would block this one forever.
    req.onblocked = () => reject(new Error("Another Dowitcher tab is upgrading the offline database."));
  });
  // A failed open must not be memoised, or the first failure is permanent for
  // the life of the tab.
  dbPromise.catch(() => {
    dbPromise = null;
  });
  return dbPromise;
}

function run<T>(store: string, mode: IDBTransactionMode, fn: (s: IDBObjectStore) => IDBRequest): Promise<T> {
  return openDB().then(
    (db) =>
      new Promise<T>((resolve, reject) => {
        const tx = db.transaction(store, mode);
        const req = fn(tx.objectStore(store));
        req.onsuccess = () => resolve(req.result as T);
        // Both are wired: a request can fail on its own (a put over quota), and
        // a transaction can fail around a request that already reported success.
        req.onerror = () => reject(req.error ?? new Error("Offline database request failed."));
        tx.onabort = () => reject(tx.error ?? new Error("Offline database transaction aborted."));
      }),
  );
}

export function idbGet<T>(store: string, key: IDBValidKey): Promise<T | undefined> {
  return run<T | undefined>(store, "readonly", (s) => s.get(key));
}

export function idbGetAll<T>(store: string): Promise<T[]> {
  return run<T[]>(store, "readonly", (s) => s.getAll());
}

/** Key is required for STORE_COMICS and must be omitted for the keyPath stores. */
export function idbPut(store: string, value: unknown, key?: IDBValidKey): Promise<void> {
  return run<void>(store, "readwrite", (s) => (key === undefined ? s.put(value) : s.put(value, key)));
}

export function idbDelete(store: string, key: IDBValidKey): Promise<void> {
  return run<void>(store, "readwrite", (s) => s.delete(key));
}

export function idbClear(store: string): Promise<void> {
  return run<void>(store, "readwrite", (s) => s.clear());
}

/**
 * For the caches that are an optimisation rather than a promise. A browser in
 * private mode, or one that has evicted us, should cost a network fetch — not
 * a broken page.
 */
export function idbTry<T>(p: Promise<T>, fallback: T): Promise<T> {
  return p.catch(() => fallback);
}
