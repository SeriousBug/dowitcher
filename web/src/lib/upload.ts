/**
 * A folder of scans is routinely multiple gigabytes, and an upload that shows
 * nothing for six minutes is an upload people kill and retry. fetch() cannot
 * report request-body progress in any browser that ships today — the streaming
 * request body that would allow it is Chromium-only and requires HTTP/2 — so
 * this one call goes around http.ts and uses XHR, which has had upload.onprogress
 * since forever. Everything else in the app should keep using http.ts.
 */
export interface UploadHandle {
  promise: Promise<unknown>;
  abort: () => void;
}

export function uploadWithProgress(
  url: string,
  body: FormData,
  onProgress: (sent: number, total: number) => void,
): UploadHandle {
  const xhr = new XMLHttpRequest();

  const promise = new Promise<unknown>((resolve, reject) => {
    xhr.open("POST", url);
    xhr.withCredentials = true;
    xhr.setRequestHeader("Accept", "application/json");
    // Content-Type is deliberately unset: the browser has to add the multipart
    // boundary itself, and setting the header by hand drops it.

    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable) onProgress(e.loaded, e.total);
    };

    xhr.onload = () => {
      let parsed: unknown;
      try {
        parsed = xhr.responseText ? JSON.parse(xhr.responseText) : undefined;
      } catch {
        parsed = undefined;
      }
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(parsed);
        return;
      }
      // The server writes user-safe text into .error; surface it verbatim, the
      // same as http.ts does.
      const message = (parsed as { error?: string } | undefined)?.error ?? xhr.statusText;
      reject(new Error(message || "Upload failed"));
    };

    xhr.onerror = () => reject(new Error("The connection dropped while uploading."));
    xhr.onabort = () => reject(new DOMException("Upload cancelled", "AbortError"));

    xhr.send(body);
  });

  return { promise, abort: () => xhr.abort() };
}

const IMAGE_RE = /\.(jpe?g|png|gif|webp|avif|bmp|tiff?)$/i;

export function isImage(file: File): boolean {
  return file.type.startsWith("image/") || IMAGE_RE.test(file.name);
}

/**
 * Walk a dropped folder. A drop hands over FileSystemEntry objects rather than
 * Files, and the directory reader returns at most 100 entries per call, so each
 * directory has to be drained in a loop until it answers with nothing.
 */
export async function filesFromDrop(items: DataTransferItemList): Promise<File[]> {
  const roots: FileSystemEntry[] = [];
  for (const item of items) {
    const entry = item.webkitGetAsEntry?.();
    if (entry) roots.push(entry);
  }
  // Plain files with no directory behind them: a multi-select rather than a
  // folder drop.
  if (roots.length === 0) return [];

  const out: File[] = [];
  await Promise.all(roots.map((entry) => walk(entry, out)));
  // Pages are ordered by filename, and a drop arrives in whatever order the
  // filesystem felt like.
  out.sort((a, b) => pathOf(a).localeCompare(pathOf(b), undefined, { numeric: true }));
  return out;
}

async function walk(entry: FileSystemEntry, out: File[]): Promise<void> {
  if (entry.isFile) {
    const file = await new Promise<File | null>((resolve) =>
      (entry as FileSystemFileEntry).file(resolve, () => resolve(null)),
    );
    if (file && isImage(file)) {
      // webkitRelativePath is read-only and empty on a dropped file, so the
      // entry's full path is stashed where the uploader can find it.
      Object.defineProperty(file, "dowitcherPath", { value: entry.fullPath.replace(/^\//, "") });
      out.push(file);
    }
    return;
  }
  const reader = (entry as FileSystemDirectoryEntry).createReader();
  for (;;) {
    const batch = await new Promise<FileSystemEntry[]>((resolve) =>
      reader.readEntries(resolve, () => resolve([])),
    );
    if (batch.length === 0) return;
    await Promise.all(batch.map((child) => walk(child, out)));
  }
}

/** The path the server should see: folder-relative where we know it, else the name. */
export function pathOf(file: File): string {
  const stashed = (file as File & { dowitcherPath?: string }).dowitcherPath;
  return stashed || file.webkitRelativePath || file.name;
}
