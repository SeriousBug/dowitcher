import { useCallback, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toaster } from "./toaster";
import { isCBZ, isImage, isPDF, pathOf, uploadWithProgress } from "./upload";
import type { Comic, ImportOptions } from "../api/generated";

/**
 * The one place that turns a pile of dropped or picked files into upload
 * requests, shared by the Import page and the drop overlays. It classifies each
 * file and routes it:
 *
 * - a CBZ/ZIP is already a book — it goes to POST /api/comics and is on the
 *   shelf by the time the request returns;
 * - a PDF is unpacked server-side into page images, so it goes to POST
 *   /api/imports as a job to watch;
 * - loose images are one book between them — the folder-of-images case — and go
 *   to POST /api/imports as a single import.
 *
 * The work runs sequentially. It has to: a PDF or an image import creates a job,
 * and the per-user cap is two, so firing a whole batch at once would 429 the
 * third. CBZs create no job, but serialising them too keeps the progress
 * readout to one file at a time and the code to one path.
 */

export interface UploadProgress {
  name: string;
  index: number;
  count: number;
  loaded: number;
  total: number;
}

interface UseComicUploadsOptions {
  collectionId?: string;
}

/** One unit of upload work: a single archive/PDF, or all the images together. */
interface WorkItem {
  kind: "cbz" | "pdf" | "images";
  name: string;
  files: File[];
}

function stripExt(name: string, re: RegExp): string {
  return name.replace(re, "");
}

function classify(files: File[]): WorkItem[] {
  const items: WorkItem[] = [];
  const images: File[] = [];
  for (const file of files) {
    if (isCBZ(file)) {
      items.push({ kind: "cbz", name: stripExt(file.name, /\.(cbz|zip)$/i), files: [file] });
    } else if (isPDF(file)) {
      items.push({ kind: "pdf", name: stripExt(file.name, /\.pdf$/i), files: [file] });
    } else if (isImage(file)) {
      images.push(file);
    }
  }
  if (images.length > 0) {
    // The folder the images came from is the obvious title; fall back to nothing
    // and let the server name it.
    const folder = pathOf(images[0]).split("/")[0];
    const name = folder && folder !== images[0].name ? folder : "";
    items.push({ kind: "images", name, files: images });
  }
  return items;
}

export function useComicUploads({ collectionId }: UseComicUploadsOptions = {}) {
  const queryClient = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState<UploadProgress | null>(null);
  // Guards against a second drop landing while a batch is still uploading, which
  // would interleave two sequences and blow past the import cap.
  const running = useRef(false);

  const start = useCallback(
    async (files: File[]) => {
      if (running.current) {
        toaster.create({
          type: "info",
          title: "Still uploading",
          description: "Wait for the current upload to finish before starting another.",
        });
        return;
      }
      const items = classify(files);
      if (items.length === 0) {
        toaster.create({
          type: "error",
          title: "Nothing to upload there",
          description: "Drop a CBZ, a PDF, or a folder of images.",
        });
        return;
      }

      running.current = true;
      setBusy(true);
      let filed = false;
      try {
        for (let i = 0; i < items.length; i++) {
          const item = items[i];
          const total = item.files.reduce((n, f) => n + f.size, 0);
          setProgress({ name: item.name || item.files[0].name, index: i, count: items.length, loaded: 0, total });

          const form = new FormData();
          if (item.kind === "cbz") {
            const body: Partial<ImportOptions> = { name: item.name, collectionId };
            form.append("options", new Blob([JSON.stringify(body)], { type: "application/json" }), "options.json");
            form.append("file", item.files[0], item.files[0].name);
          } else if (item.kind === "pdf") {
            const body: Partial<ImportOptions> = { name: item.name, collectionId };
            form.append("options", new Blob([JSON.stringify(body)], { type: "application/json" }), "options.json");
            form.append("files", item.files[0], item.files[0].name);
          } else {
            const body: ImportOptions = {
              name: item.name,
              threshold: 3,
              exact: false,
              collectionId,
            };
            form.append("options", new Blob([JSON.stringify(body)], { type: "application/json" }), "options.json");
            for (const file of item.files) form.append("files", file, pathOf(file));
          }

          const url = item.kind === "cbz" ? "/api/comics" : "/api/imports";
          try {
            const handle = uploadWithProgress(url, form, (loaded, t) =>
              setProgress({ name: item.name || item.files[0].name, index: i, count: items.length, loaded, total: t }),
            );
            const data = await handle.promise;
            filed = true;
            if (item.kind === "cbz") {
              const comic = data as Comic;
              toaster.create({
                type: "success",
                title: `Added ${comic.title}`,
                description: "It's on your shelf and ready to read.",
              });
            } else {
              toaster.create({
                type: "success",
                title: `Uploaded ${item.name || item.files[0].name}`,
                description: "Dowitcher is packing it now — watch it on the Import page.",
              });
            }
          } catch (err) {
            toaster.create({
              type: "error",
              title: `Couldn't upload ${item.name || item.files[0].name}`,
              description: err instanceof Error ? err.message : "Something went wrong. Please try again.",
            });
          }
        }
      } finally {
        running.current = false;
        setBusy(false);
        setProgress(null);
        if (filed) {
          // Prefix match invalidates every ["comics", …] query, the shelf and any
          // collection grid alike.
          queryClient.invalidateQueries({ queryKey: ["comics"] });
          if (collectionId) {
            queryClient.invalidateQueries({ queryKey: ["collection", collectionId] });
            queryClient.invalidateQueries({ queryKey: ["collections"] });
          }
        }
      }
    },
    [collectionId, queryClient],
  );

  return { start, busy, progress };
}
