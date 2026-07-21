import { useRef, useState } from "react";
import {
  ArrowDown,
  ArrowUp,
  CheckCircle2,
  ChevronDown,
  Copy,
  FileArchive,
  FolderUp,
  ListOrdered,
  Loader2,
  Pause,
  Play,
  Trash2,
  TriangleAlert,
  Upload,
  X,
} from "lucide-react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { css } from "styled-system/css";
import { grid, hstack, vstack } from "styled-system/patterns";
import { Button } from "../components/Button";
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";
import { useLiveData } from "../live/LiveData";
import { useAuth } from "../auth/AuthProvider";
import { http, HttpError } from "../api/http";
import { toaster } from "../lib/toaster";
import { formatBytes } from "../lib/format";
import { filesFromDrop, isCBZ, isImage, isPDF, pathOf, uploadWithProgress } from "../lib/upload";
import { useComicUploads } from "../lib/useComicUploads";
import type { Collection, DupeGroup, ImportJob, ImportOptions } from "../api/generated";

const STAGE_LABEL: Record<string, string> = {
  uploading: "Uploading",
  queued: "Queued",
  extracting: "Extracting PDF pages",
  reading: "Fingerprinting pages",
  grouping: "Finding duplicates",
  encoding: "Re-encoding pages",
  packaging: "Packing the CBZ",
  done: "Done",
  failed: "Failed",
};

const ENCODINGS = [
  { value: "", label: "Keep as-is" },
  { value: "avif", label: "AVIF" },
  { value: "webp", label: "WebP" },
  { value: "jpeg", label: "JPEG" },
];

/**
 * The threshold is a mean-absolute-error ceiling on a 0-255 scale, which is a
 * true thing to say and a useless thing to ask someone to choose. These are the
 * three answers people actually have, in their words; the number lives behind
 * "set it by hand" for the one person in a hundred who has a reason.
 */
const SENSITIVITY = [
  {
    id: "exact",
    label: "Only identical pages",
    hint: "Drops a page only when it's the same file byte for byte. Nothing else is touched.",
    exact: true,
    threshold: 3,
  },
  {
    id: "normal",
    label: "Recommended",
    hint: "Also drops pages that are the same scan twice — a re-save, a different crop of nothing. This is what almost everyone wants.",
    exact: false,
    threshold: 3,
  },
  {
    id: "aggressive",
    label: "Catch more near-copies",
    hint: "Casts a wider net. Worth trying on a messy folder, but it can drop a page that merely looks like its neighbour.",
    exact: false,
    threshold: 6,
  },
] as const;

type SensitivityId = (typeof SENSITIVITY)[number]["id"];

export function ImportPage() {
  const { jobs, paused } = useLiveData();
  const { user } = useAuth();
  const isAdmin = user?.isAdmin ?? false;
  const queryClient = useQueryClient();
  const inputRef = useRef<HTMLInputElement>(null);
  const cbzInputRef = useRef<HTMLInputElement>(null);

  const [files, setFiles] = useState<File[]>([]);
  // Ready-made books (CBZ or PDF) and a folder of images are the two things this
  // page takes, and they are exclusive: a folder of images goes through the
  // dedupe pipeline, a ready-made book does not.
  const [ready, setReady] = useState<File[]>([]);
  const [dragging, setDragging] = useState(false);
  const [sensitivity, setSensitivity] = useState<SensitivityId>("normal");
  const [manual, setManual] = useState(false);
  const [sent, setSent] = useState<{ loaded: number; total: number } | null>(null);
  const abortRef = useRef<(() => void) | null>(null);

  const [options, setOptions] = useState<ImportOptions>({
    name: "",
    threshold: 3,
    exact: false,
    encode: "",
    quality: 80,
  });

  const collectionsQuery = useQuery({
    queryKey: ["collections"],
    queryFn: () => http.get<Collection[]>("/api/collections"),
  });

  const comicUploads = useComicUploads({ collectionId: options.collectionId });

  const hasReady = ready.length > 0;
  const totalBytes = hasReady
    ? ready.reduce((n, f) => n + f.size, 0)
    : files.reduce((n, f) => n + f.size, 0);

  function take(picked: File[]) {
    const books = picked.filter((f) => isCBZ(f) || isPDF(f));
    const images = picked.filter(isImage);

    // Ready-made books win only when they arrived on their own. Mixed with images
    // it is an ambiguous drop, and packing the images is the thing this page is
    // for. This page takes one book at a time; a batch belongs on the Library
    // page, which uploads them one after another.
    if (books.length > 0 && images.length === 0) {
      if (books.length > 1) {
        toaster.create({
          type: "error",
          title: "One book at a time here",
          description:
            "To add several CBZs or PDFs at once, drag them onto the Library page instead.",
        });
        return;
      }
      setReady(books);
      setFiles([]);
      return;
    }

    setReady([]);
    setFiles(images);
    // The folder's own name is the obvious title, and typing it again is busywork.
    if (images.length > 0 && !options.name) {
      const folder = pathOf(images[0]).split("/")[0];
      if (folder && folder !== images[0].name) setOptions((o) => ({ ...o, name: folder }));
    }
    if (picked.length > 0 && images.length === 0) {
      toaster.create({
        type: "error",
        title: "Nothing to import in there",
        description: "Dowitcher packs images into a CBZ. Drop a folder of images, a CBZ, or a PDF.",
      });
    }
  }

  function clearPick() {
    setFiles([]);
    setReady([]);
  }

  // The folder-of-images path: one import through the dedupe pipeline, with the
  // sensitivity and re-encode options. Ready-made books skip all of this and go
  // through useComicUploads instead.
  const start = useMutation({
    mutationFn: async () => {
      const chosen = SENSITIVITY.find((s) => s.id === sensitivity)!;
      const body: ImportOptions = {
        ...options,
        name: options.name.trim() || "Untitled",
        exact: manual ? options.exact : chosen.exact,
        threshold: manual ? options.threshold : chosen.threshold,
        encode: options.encode || undefined,
        quality: options.encode ? options.quality : undefined,
      };
      const form = new FormData();
      form.append(
        "options",
        new Blob([JSON.stringify(body)], { type: "application/json" }),
        "options.json",
      );
      for (const file of files) form.append("files", file, pathOf(file));

      setSent({ loaded: 0, total: totalBytes });
      const handle = uploadWithProgress("/api/imports", form, (loaded, total) =>
        setSent({ loaded, total }),
      );
      abortRef.current = handle.abort;
      return handle.promise;
    },
    onSuccess: () => {
      // The job itself now reports over the stream; the page has nothing left to
      // ask for.
      toaster.create({
        type: "success",
        title: "Upload finished",
        description: "Dowitcher is packing it now — watch it below.",
      });
      clearPick();
      setOptions((o) => ({ ...o, name: "" }));
      queryClient.invalidateQueries({ queryKey: ["comics"] });
    },
    onError: (err) => {
      if (err instanceof DOMException && err.name === "AbortError") {
        toaster.create({ type: "info", title: "Upload cancelled" });
        return;
      }
      toaster.create({
        type: "error",
        title: "That import didn't start",
        description:
          err instanceof HttpError || err instanceof Error
            ? err.message
            : "Something went wrong. Please try again.",
      });
    },
    onSettled: () => {
      setSent(null);
      abortRef.current = null;
    },
  });

  // useComicUploads runs its own queue, toasts, and query invalidation; the page
  // hands it the ready-made books and clears the picker once it takes them.
  async function uploadReady() {
    const books = ready;
    clearPick();
    setOptions((o) => ({ ...o, name: "" }));
    await comicUploads.start(books);
  }

  const uploading = start.isPending || comicUploads.busy;

  const queued = jobs
    .filter((j) => j.stage === "queued")
    .sort((a, b) => a.queueSeq - b.queueSeq);
  const active = jobs.filter(
    (j) => j.stage !== "done" && j.stage !== "failed" && j.stage !== "queued",
  );
  const finished = jobs.filter((j) => j.stage === "done" || j.stage === "failed");

  return (
    <div className={vstack({ gap: "7", alignItems: "stretch" })}>
      <PageHeader
        eyebrow="Intake"
        title="Import"
        subtitle="Point Dowitcher at a folder of images. It drops the duplicates, puts the pages in order, and packs a CBZ. Already have one? Upload it as it is."
      />

      <section
        onDragOver={(e) => {
          e.preventDefault();
          setDragging(true);
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={async (e) => {
          e.preventDefault();
          setDragging(false);
          const dropped = await filesFromDrop(e.dataTransfer.items);
          take(dropped.length > 0 ? dropped : [...e.dataTransfer.files]);
        }}
        className={vstack({
          gap: "4",
          alignItems: "center",
          py: "10",
          px: "6",
          borderRadius: "xl",
          borderWidth: "1px",
          borderStyle: "dashed",
          borderColor: dragging ? "accent" : "border",
          bg: dragging ? "accentQuiet" : "surface",
          textAlign: "center",
          transition: "border-color 0.15s ease, background 0.15s ease",
          _hover: { borderColor: "accent" },
        })}
      >
        <FolderUp size={30} className={css({ color: "ink.500" })} strokeWidth={1.5} />
        <div className={vstack({ gap: "1.5", maxW: "md" })}>
          <h2 className={css({ fontSize: "lg", fontWeight: "bold" })}>
            Drop a folder of images, a CBZ, or a PDF
          </h2>
          <p className={css({ color: "textMuted", fontSize: "sm", lineHeight: "1.6" })}>
            Pages get sorted by filename. Anything that turns out to be the same
            image twice only makes it in once. A CBZ is already a book, so it
            goes straight to the shelf untouched; a PDF has its pages pulled out
            and packed into one.
          </p>
        </div>

        <input
          ref={inputRef}
          type="file"
          multiple
          // webkitdirectory is the only way to pick a folder rather than a
          // selection of files, and it is React-unknown, hence the cast.
          {...({ webkitdirectory: "", directory: "" } as Record<string, string>)}
          onChange={(e) => take([...(e.target.files ?? [])])}
          className={css({ srOnly: true })}
        />
        <input
          ref={cbzInputRef}
          type="file"
          accept=".cbz,.zip,.pdf"
          onChange={(e) => take([...(e.target.files ?? [])])}
          className={css({ srOnly: true })}
        />

        <div className={hstack({ gap: "2.5", flexWrap: "wrap", justify: "center" })}>
          <Button
            variant="primary"
            icon={<Upload size={16} />}
            onClick={() => inputRef.current?.click()}
          >
            Choose a folder
          </Button>
          <Button
            variant="ghost"
            icon={<FileArchive size={16} />}
            onClick={() => cbzInputRef.current?.click()}
          >
            Choose a CBZ or PDF
          </Button>
        </div>

        {hasReady && (
          <div
            className={vstack({
              gap: "2",
              w: "full",
              maxW: "md",
              p: "3.5",
              borderRadius: "md",
              bg: "bg",
              borderWidth: "1px",
              borderColor: "border",
            })}
          >
            <span className={css({ fontSize: "sm", fontWeight: "semibold", color: "text" })}>
              {ready.length === 1 ? "1 book" : `${ready.length} books`} · {formatBytes(totalBytes)}
            </span>
            <span
              className={css({ fontSize: "xs", color: "textMuted", truncate: true, maxW: "full" })}
            >
              {ready.length === 1
                ? ready[0].name
                : `${ready[0].name} … ${ready[ready.length - 1].name}`}
            </span>
            <button
              onClick={clearPick}
              className={css({
                fontSize: "xs",
                fontWeight: "semibold",
                color: "textMuted",
                cursor: "pointer",
                _hover: { color: "danger" },
              })}
            >
              Pick something else
            </button>
          </div>
        )}

        {files.length > 0 && (
          <div
            className={vstack({
              gap: "2",
              w: "full",
              maxW: "md",
              p: "3.5",
              borderRadius: "md",
              bg: "bg",
              borderWidth: "1px",
              borderColor: "border",
            })}
          >
            <span className={css({ fontSize: "sm", fontWeight: "semibold", color: "text" })}>
              {files.length === 1 ? "1 image" : `${files.length} images`} ·{" "}
              {formatBytes(totalBytes)}
            </span>
            <span className={css({ fontSize: "xs", color: "textMuted", truncate: true, maxW: "full" })}>
              {pathOf(files[0])} … {pathOf(files[files.length - 1])}
            </span>
            <button
              onClick={clearPick}
              className={css({
                fontSize: "xs",
                fontWeight: "semibold",
                color: "textMuted",
                cursor: "pointer",
                _hover: { color: "danger" },
              })}
            >
              Pick something else
            </button>
          </div>
        )}
      </section>

      <section className={vstack({ gap: "4", alignItems: "stretch" })}>
        <SectionTitle>Options</SectionTitle>
        <div
          className={vstack({
            gap: "5",
            alignItems: "stretch",
            p: "5",
            borderRadius: "lg",
            bg: "surface",
            borderWidth: "1px",
            borderColor: "border",
          })}
        >
          <div className={grid({ columns: { base: 1, md: 2 }, gap: "4" })}>
            <Field label="Name" hint="What this ends up called on the shelf.">
              <input
                value={options.name}
                onChange={(e) => setOptions({ ...options, name: e.target.value })}
                placeholder="Untitled"
                className={FIELD}
              />
            </Field>

            <Field label="File it into" hint="Optional. You can move it later.">
              <select
                value={options.collectionId ?? ""}
                onChange={(e) =>
                  setOptions({ ...options, collectionId: e.target.value || undefined })
                }
                className={FIELD}
              >
                <option value="">No collection</option>
                {(collectionsQuery.data ?? []).map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
            </Field>

            {/* Everything below is the pipeline's, and a packed CBZ never goes
                through it. Showing these against a CBZ would promise a re-encode
                and a dedupe that are not going to happen. */}
            {!hasReady && (
              <Field label="Re-encode pages" hint="Smaller files, slower import. AVIF wins on size.">
                <select
                  value={options.encode ?? ""}
                  onChange={(e) => setOptions({ ...options, encode: e.target.value })}
                  className={FIELD}
                >
                  {ENCODINGS.map((e) => (
                    <option key={e.value} value={e.value}>
                      {e.label}
                    </option>
                  ))}
                </select>
              </Field>
            )}

            {!hasReady && options.encode ? (
              <Field label="Quality" hint="Higher keeps more detail and costs more space.">
                <div className={hstack({ gap: "3" })}>
                  <input
                    type="range"
                    min={40}
                    max={100}
                    step={1}
                    value={options.quality ?? 80}
                    onChange={(e) => setOptions({ ...options, quality: Number(e.target.value) })}
                    className={css({ flex: "1", accentColor: "accent", cursor: "pointer" })}
                  />
                  <span
                    className={css({ fontFamily: "mono", fontSize: "sm", color: "textMuted", w: "8" })}
                  >
                    {options.quality ?? 80}
                  </span>
                </div>
              </Field>
            ) : (
              <div />
            )}
          </div>

          {!hasReady && (
          <div className={vstack({ gap: "3", alignItems: "stretch" })}>
            <span className={css({ fontSize: "sm", fontWeight: "semibold", color: "text" })}>
              What counts as a duplicate
            </span>
            <div className={vstack({ gap: "2", alignItems: "stretch" })}>
              {SENSITIVITY.map((option) => (
                <label
                  key={option.id}
                  className={hstack({
                    gap: "3",
                    alignItems: "flex-start",
                    p: "3.5",
                    borderRadius: "md",
                    bg: "bg",
                    borderWidth: "1px",
                    borderColor: !manual && sensitivity === option.id ? "accent" : "border",
                    cursor: manual ? "not-allowed" : "pointer",
                    opacity: manual ? 0.5 : 1,
                  })}
                >
                  <input
                    type="radio"
                    name="sensitivity"
                    checked={sensitivity === option.id}
                    disabled={manual}
                    onChange={() => setSensitivity(option.id)}
                    className={css({
                      mt: "0.5",
                      w: "4",
                      h: "4",
                      accentColor: "accent",
                      cursor: "pointer",
                      flexShrink: 0,
                    })}
                  />
                  <span className={vstack({ gap: "0.5", alignItems: "flex-start" })}>
                    <span className={hstack({ gap: "2" })}>
                      <span
                        className={css({ fontSize: "sm", fontWeight: "semibold", color: "text" })}
                      >
                        {option.label}
                      </span>
                      {option.id === "normal" && (
                        <span
                          className={css({
                            px: "1.5",
                            py: "0.5",
                            borderRadius: "sm",
                            bg: "accentQuiet",
                            color: "magenta.300",
                            fontSize: "2xs",
                            fontWeight: "bold",
                          })}
                        >
                          DEFAULT
                        </span>
                      )}
                    </span>
                    <span className={css({ fontSize: "xs", color: "textMuted", lineHeight: "1.5" })}>
                      {option.hint}
                    </span>
                  </span>
                </label>
              ))}
            </div>

            <button
              onClick={() => setManual(!manual)}
              className={hstack({
                gap: "1.5",
                alignSelf: "flex-start",
                fontSize: "xs",
                fontWeight: "semibold",
                color: "textMuted",
                cursor: "pointer",
                _hover: { color: "text" },
              })}
            >
              <ChevronDown
                size={13}
                className={manual ? ROTATED : undefined}
              />
              Set it by hand
            </button>

            {manual && (
              <div
                className={vstack({
                  gap: "3",
                  alignItems: "stretch",
                  p: "3.5",
                  borderRadius: "md",
                  bg: "bg",
                  borderWidth: "1px",
                  borderColor: "border",
                })}
              >
                <p className={css({ fontSize: "xs", color: "textMuted", lineHeight: "1.6" })}>
                  Two pages are called the same image when their average
                  brightness difference, per pixel, stays under this number — on a
                  scale where 0 is identical and 255 is black against white. Real
                  duplicates come in around 2; genuinely different pages are ten
                  times that or more. <strong>3.0 is the tested default.</strong>
                </p>
                <label className={hstack({ gap: "2.5", cursor: "pointer" })}>
                  <input
                    type="checkbox"
                    checked={options.exact}
                    onChange={(e) => setOptions({ ...options, exact: e.target.checked })}
                    className={css({ w: "4", h: "4", accentColor: "accent", cursor: "pointer" })}
                  />
                  <span className={css({ fontSize: "sm", color: "text" })}>
                    Skip the comparison — only drop identical files
                  </span>
                </label>
                <div className={hstack({ gap: "3" })}>
                  <input
                    type="range"
                    min={0}
                    max={10}
                    step={0.5}
                    value={options.threshold}
                    disabled={options.exact}
                    onChange={(e) => setOptions({ ...options, threshold: Number(e.target.value) })}
                    className={css({
                      flex: "1",
                      accentColor: "accent",
                      cursor: "pointer",
                      _disabled: { opacity: 0.4 },
                    })}
                  />
                  <span
                    className={css({ fontFamily: "mono", fontSize: "sm", color: "textMuted", w: "8" })}
                  >
                    {options.threshold.toFixed(1)}
                  </span>
                </div>
              </div>
            )}
          </div>
          )}

          <div className={hstack({ gap: "3", justify: "flex-end", flexWrap: "wrap" })}>
            {sent && (
              <span
                aria-live="polite"
                className={css({ fontSize: "xs", color: "textMuted", fontFamily: "mono" })}
              >
                {formatBytes(sent.loaded)} of {formatBytes(sent.total)}
              </span>
            )}
            {comicUploads.progress && (
              <span
                aria-live="polite"
                className={css({ fontSize: "xs", color: "textMuted", fontFamily: "mono" })}
              >
                {comicUploads.progress.count > 1 &&
                  `${comicUploads.progress.index + 1}/${comicUploads.progress.count} · `}
                {formatBytes(comicUploads.progress.loaded)} of{" "}
                {formatBytes(comicUploads.progress.total)}
              </span>
            )}
            {start.isPending && (
              <Button variant="ghost" onClick={() => abortRef.current?.()}>
                Cancel
              </Button>
            )}
            <Button
              variant="primary"
              icon={<Upload size={16} />}
              busy={uploading}
              disabled={!hasReady && files.length === 0}
              onClick={() => (hasReady ? uploadReady() : start.mutate())}
            >
              {hasReady
                ? ready.length === 1
                  ? "Upload this book"
                  : `Upload ${ready.length} books`
                : files.length === 0
                  ? "Choose a folder first"
                  : `Import ${files.length} images`}
            </Button>
          </div>

          {sent && (
            <span
              className={css({ h: "3px", borderRadius: "full", bg: "ink.750", overflow: "hidden" })}
              role="progressbar"
              aria-valuenow={sent.total > 0 ? Math.round((sent.loaded / sent.total) * 100) : 0}
              aria-valuemin={0}
              aria-valuemax={100}
              aria-label="Upload progress"
            >
              <span
                className={css({ display: "block", h: "full", bg: "accent", transition: "width 0.2s ease" })}
                style={{ width: `${sent.total > 0 ? (sent.loaded / sent.total) * 100 : 0}%` }}
              />
            </span>
          )}

          {comicUploads.progress && (
            <span
              className={css({ h: "3px", borderRadius: "full", bg: "ink.750", overflow: "hidden" })}
              role="progressbar"
              aria-valuenow={
                comicUploads.progress.total > 0
                  ? Math.round((comicUploads.progress.loaded / comicUploads.progress.total) * 100)
                  : 0
              }
              aria-valuemin={0}
              aria-valuemax={100}
              aria-label="Upload progress"
            >
              <span
                className={css({ display: "block", h: "full", bg: "accent", transition: "width 0.2s ease" })}
                style={{
                  width: `${comicUploads.progress.total > 0 ? (comicUploads.progress.loaded / comicUploads.progress.total) * 100 : 0}%`,
                }}
              />
            </span>
          )}
        </div>
      </section>

      {(queued.length > 0 || paused) && (
        <QueueSection queued={queued} paused={paused} isAdmin={isAdmin} />
      )}

      <section className={vstack({ gap: "4", alignItems: "stretch" })}>
        <SectionTitle>In progress</SectionTitle>
        {active.length === 0 ? (
          <EmptyState icon={Upload} title="Nothing importing">
            Start an import above and you can watch it work here. It keeps going
            if you close this tab.
          </EmptyState>
        ) : (
          active.map((job) => <JobRow key={job.id} job={job} />)
        )}
      </section>

      {finished.length > 0 && (
        <section className={vstack({ gap: "4", alignItems: "stretch" })}>
          <div className={hstack({ gap: "3", justify: "space-between", alignItems: "center" })}>
            <SectionTitle>Recently finished</SectionTitle>
            <ClearFinishedButton />
          </div>
          {finished.map((job) => (
            <JobRow key={job.id} job={job} />
          ))}
        </section>
      )}
    </div>
  );
}

/**
 * The import queue: jobs waiting for a worker. Everyone sees the order and can
 * remove their own jobs; an admin can pause the whole queue and reorder or
 * remove any job — the server authorizes each action, this only hides controls
 * the caller cannot use.
 */
function QueueSection({
  queued,
  paused,
  isAdmin,
}: {
  queued: ImportJob[];
  paused: boolean;
  isAdmin: boolean;
}) {
  const pauseResume = useMutation({
    mutationFn: (next: boolean) =>
      http.post<{ ok: boolean }>(`/api/imports/queue/${next ? "pause" : "resume"}`),
    onError: (err) =>
      toaster.create({
        type: "error",
        title: "Couldn't change the queue",
        description: err instanceof HttpError ? err.message : "Please try again.",
      }),
  });

  const reorder = useMutation({
    mutationFn: (jobIds: string[]) =>
      http.put<{ ok: boolean }>("/api/imports/queue/order", { jobIds }),
    onError: (err) =>
      toaster.create({
        type: "error",
        title: "Couldn't reorder the queue",
        description: err instanceof HttpError ? err.message : "Please try again.",
      }),
  });

  // Reordering swaps two neighbours and sends the whole new order, which is the
  // idempotent shape the server takes.
  function move(index: number, delta: number) {
    const next = [...queued];
    const target = index + delta;
    if (target < 0 || target >= next.length) return;
    [next[index], next[target]] = [next[target], next[index]];
    reorder.mutate(next.map((j) => j.id));
  }

  return (
    <section className={vstack({ gap: "4", alignItems: "stretch" })}>
      <div className={hstack({ gap: "3", justify: "space-between", alignItems: "center" })}>
        <SectionTitle>Queue</SectionTitle>
        {isAdmin && (
          <Button
            variant="ghost"
            icon={paused ? <Play size={15} /> : <Pause size={15} />}
            busy={pauseResume.isPending}
            onClick={() => pauseResume.mutate(!paused)}
          >
            {paused ? "Resume queue" : "Pause queue"}
          </Button>
        )}
      </div>

      {paused && (
        <div
          className={hstack({
            gap: "2.5",
            p: "3.5",
            borderRadius: "md",
            bg: "accentQuiet",
            borderWidth: "1px",
            borderColor: "border",
            fontSize: "sm",
            color: "text",
          })}
        >
          <Pause size={16} className={css({ color: "accent", flexShrink: 0 })} />
          The queue is paused. Jobs wait here until it is resumed.
        </div>
      )}

      {queued.length === 0 ? (
        <EmptyState icon={ListOrdered} title="Nothing queued">
          Uploads wait here for a free worker before they start.
        </EmptyState>
      ) : (
        queued.map((job, i) => (
          <QueueRow
            key={job.id}
            job={job}
            isAdmin={isAdmin}
            first={i === 0}
            last={i === queued.length - 1}
            onMove={(delta) => move(i, delta)}
          />
        ))
      )}
    </section>
  );
}

function QueueRow({
  job,
  isAdmin,
  first,
  last,
  onMove,
}: {
  job: ImportJob;
  isAdmin: boolean;
  first: boolean;
  last: boolean;
  onMove: (delta: number) => void;
}) {
  const remove = useMutation({
    mutationFn: () => http.post<{ ok: boolean }>(`/api/imports/${job.id}/cancel`),
    onSuccess: () =>
      toaster.create({ type: "success", title: `Removed ${job.name || "import"} from the queue` }),
    onError: (err) =>
      toaster.create({
        type: "error",
        title: "Couldn't remove that",
        description: err instanceof HttpError ? err.message : "It may have already started.",
      }),
  });

  return (
    <div
      className={hstack({
        gap: "3",
        justify: "space-between",
        p: "4",
        borderRadius: "lg",
        bg: "surface",
        borderWidth: "1px",
        borderColor: "border",
      })}
    >
      <div className={hstack({ gap: "2.5", minW: "0" })}>
        <span className={css({ fontWeight: "semibold", truncate: true })}>
          {job.name || "Untitled import"}
        </span>
        {job.kind === "library-pdf" && (
          <span
            className={css({
              px: "1.5",
              py: "0.5",
              borderRadius: "sm",
              bg: "ink.750",
              color: "textMuted",
              fontSize: "2xs",
              fontWeight: "bold",
              flexShrink: 0,
            })}
          >
            LIBRARY PDF
          </span>
        )}
      </div>

      <div className={hstack({ gap: "1.5", flexShrink: 0 })}>
        {isAdmin && (
          <>
            <IconButton
              label="Move up in the queue"
              disabled={first}
              onClick={() => onMove(-1)}
            >
              <ArrowUp size={15} />
            </IconButton>
            <IconButton
              label="Move down in the queue"
              disabled={last}
              onClick={() => onMove(1)}
            >
              <ArrowDown size={15} />
            </IconButton>
          </>
        )}
        <IconButton
          label={`Remove ${job.name || "import"} from the queue`}
          danger
          disabled={remove.isPending}
          onClick={() => remove.mutate()}
        >
          <X size={15} />
        </IconButton>
      </div>
    </div>
  );
}

function ClearFinishedButton() {
  const clear = useMutation({
    mutationFn: () => http.del<{ ok: boolean }>("/api/imports/finished"),
    onSuccess: () => {
      // No local update needed: the server pushes a fresh job snapshot over the
      // WS after clearing, and LiveData replaces its list from it.
      toaster.create({ type: "success", title: "Cleared finished imports" });
    },
    onError: (err) =>
      toaster.create({
        type: "error",
        title: "Couldn't clear those",
        description: err instanceof HttpError ? err.message : "Please try again.",
      }),
  });
  return (
    <Button
      variant="ghost"
      icon={<Trash2 size={15} />}
      busy={clear.isPending}
      onClick={() => clear.mutate()}
    >
      Clear finished
    </Button>
  );
}

function IconButton({
  label,
  danger,
  disabled,
  onClick,
  children,
}: {
  label: string;
  danger?: boolean;
  disabled?: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      onClick={onClick}
      disabled={disabled}
      aria-label={label}
      title={label}
      className={css({
        color: "textMuted",
        cursor: "pointer",
        borderRadius: "sm",
        p: "1",
        _hover: { color: danger ? "danger" : "text", bg: "surfaceRaised" },
        _disabled: { opacity: 0.35, cursor: "not-allowed", _hover: { bg: "transparent" } },
      })}
    >
      {children}
    </button>
  );
}

function JobRow({ job }: { job: ImportJob }) {
  const failed = job.stage === "failed";
  const done = job.stage === "done";
  const pct = job.total > 0 ? Math.round((job.done / job.total) * 100) : 0;
  const [showDupes, setShowDupes] = useState(false);
  const dropped = job.exactDupes + job.nearDupes;

  const cancel = useMutation({
    mutationFn: () => http.post<{ ok: boolean }>(`/api/imports/${job.id}/cancel`),
    onSuccess: () => {
      // No invalidate: the job's own stage change arrives over the stream.
      toaster.create({ type: "success", title: `Stopped importing ${job.name}` });
    },
    onError: (err) => {
      toaster.create({
        type: "error",
        title: "Couldn't cancel that",
        description:
          err instanceof HttpError ? err.message : "It may have already finished.",
      });
    },
  });

  return (
    <div
      className={vstack({
        gap: "3",
        alignItems: "stretch",
        p: "4",
        borderRadius: "lg",
        bg: "surface",
        borderWidth: "1px",
        borderColor: failed ? "rust.700" : "border",
      })}
    >
      <div className={hstack({ gap: "3", justify: "space-between" })}>
        <div className={hstack({ gap: "2.5", minW: "0" })}>
          {failed ? (
            <TriangleAlert size={17} className={css({ color: "danger", flexShrink: 0 })} />
          ) : done ? (
            <CheckCircle2 size={17} className={css({ color: "ok", flexShrink: 0 })} />
          ) : (
            <Loader2
              size={17}
              className={css({
                color: "accent",
                flexShrink: 0,
                animation: "spin 0.9s linear infinite",
                _motionReduce: { animation: "none" },
              })}
            />
          )}
          {/* A job exists before the server has read the options part, so one
              that dies during the upload never gets a name at all. */}
          <span className={css({ fontWeight: "semibold", truncate: true })}>
            {job.name || "Untitled import"}
          </span>
        </div>

        <div className={hstack({ gap: "3", flexShrink: 0 })}>
          <span aria-live="polite" className={css({ fontSize: "xs", color: "textMuted" })}>
            {STAGE_LABEL[job.stage] ?? job.stage}
          </span>
          {!done && !failed && (
            <button
              onClick={() => cancel.mutate()}
              disabled={cancel.isPending}
              aria-label={`Cancel importing ${job.name}`}
              title="Cancel this import"
              className={css({
                color: "textMuted",
                cursor: "pointer",
                borderRadius: "sm",
                p: "0.5",
                _hover: { color: "danger", bg: "surfaceRaised" },
                _disabled: { opacity: 0.5, cursor: "not-allowed" },
              })}
            >
              <X size={15} />
            </button>
          )}
        </div>
      </div>

      {!done && !failed && (
        <span
          className={css({ h: "3px", borderRadius: "full", bg: "ink.750", overflow: "hidden" })}
          role="progressbar"
          aria-valuenow={pct}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-label={`${job.name || "Untitled import"} progress`}
        >
          <span
            className={css({ display: "block", h: "full", bg: "accent", transition: "width 0.3s ease" })}
            style={{ width: `${pct}%` }}
          />
        </span>
      )}

      <div className={hstack({ gap: "4", flexWrap: "wrap", fontSize: "xs", color: "textMuted" })}>
        {job.total > 0 && !done && (
          <span>
            {job.done} of {job.total}
          </span>
        )}
        {job.pageCount > 0 && (
          <span>{job.pageCount === 1 ? "1 page kept" : `${job.pageCount} pages kept`}</span>
        )}
        {job.exactDupes > 0 && (
          <span>
            {job.exactDupes === 1
              ? "1 exact duplicate dropped"
              : `${job.exactDupes} exact duplicates dropped`}
          </span>
        )}
        {job.nearDupes > 0 && (
          <span>
            {job.nearDupes === 1
              ? "1 near-duplicate dropped"
              : `${job.nearDupes} near-duplicates dropped`}
          </span>
        )}
        {job.message && (
          <span className={css({ color: failed ? "rust.300" : "textMuted" })}>{job.message}</span>
        )}
        {done && job.comicId && (
          <Link
            to="/comic/$id"
            params={{ id: job.comicId }}
            className={css({
              color: "accent",
              fontWeight: "semibold",
              textDecoration: "none",
              _hover: { textDecoration: "underline" },
            })}
          >
            Read it now
          </Link>
        )}
      </div>

      {done && dropped > 0 && (
        <div className={vstack({ gap: "3", alignItems: "stretch" })}>
          <button
            onClick={() => setShowDupes(!showDupes)}
            className={hstack({
              gap: "1.5",
              alignSelf: "flex-start",
              fontSize: "xs",
              fontWeight: "semibold",
              color: "textMuted",
              cursor: "pointer",
              _hover: { color: "text" },
            })}
          >
            <ChevronDown size={13} className={showDupes ? ROTATED : undefined} />
            {dropped === 1 ? "1 duplicate merged" : `${dropped} duplicates merged`}
          </button>
          {showDupes && <DupeReport jobId={job.id} />}
        </div>
      )}
    </div>
  );
}

/**
 * What the dedupe actually did, page by page. Fetched only when someone opens
 * it: the counts on the row answer the question for almost everyone, and this is
 * the receipt for the one time they don't believe it.
 */
function DupeReport({ jobId }: { jobId: string }) {
  const dupes = useQuery({
    queryKey: ["imports", jobId, "dupes"],
    queryFn: () => http.get<DupeGroup[]>(`/api/imports/${jobId}/dupes`),
  });

  if (dupes.isLoading) {
    return (
      <span className={css({ fontSize: "xs", color: "textMuted" })}>Fetching the report…</span>
    );
  }

  if (dupes.isError) {
    return (
      <span className={css({ fontSize: "xs", color: "rust.300" })}>
        {dupes.error instanceof HttpError
          ? dupes.error.message
          : "That report isn't available any more."}
      </span>
    );
  }

  const groups = dupes.data ?? [];
  if (groups.length === 0) {
    return <span className={css({ fontSize: "xs", color: "textMuted" })}>Nothing to show.</span>;
  }

  return (
    <div
      className={vstack({
        gap: "2",
        alignItems: "stretch",
        maxH: "72",
        overflowY: "auto",
        p: "3.5",
        borderRadius: "md",
        bg: "bg",
        borderWidth: "1px",
        borderColor: "border",
      })}
    >
      {groups.map((group) => (
        <div
          key={group.kept}
          className={vstack({
            gap: "1",
            alignItems: "stretch",
            pb: "2",
            borderBottomWidth: "1px",
            borderColor: "ink.850",
          })}
        >
          <span
            className={hstack({
              gap: "2",
              fontSize: "xs",
              fontFamily: "mono",
              color: "ok",
              wordBreak: "break-all",
            })}
          >
            <CheckCircle2 size={12} className={css({ flexShrink: 0 })} />
            {group.kept}
          </span>
          {group.dropped.map((name) => (
            <span
              key={name}
              className={hstack({
                gap: "2",
                pl: "5",
                fontSize: "xs",
                fontFamily: "mono",
                color: "ink.500",
                textDecoration: "line-through",
                wordBreak: "break-all",
              })}
            >
              <Copy size={11} className={css({ flexShrink: 0 })} />
              {name}
            </span>
          ))}
          {group.reason && (
            <span className={css({ pl: "5", fontSize: "2xs", color: "ink.600" })}>
              {group.reason}
            </span>
          )}
        </div>
      ))}
    </div>
  );
}

const ROTATED = css({ transform: "rotate(-90deg)", transition: "transform 0.15s ease" });

const FIELD = css({
  w: "full",
  px: "3",
  py: "2.5",
  borderRadius: "md",
  borderWidth: "1px",
  borderColor: "border",
  bg: "bg",
  color: "text",
  fontSize: "sm",
  _placeholder: { color: "ink.500" },
  _focus: { outline: "none", borderColor: "accent" },
});

function SectionTitle({ children }: { children: React.ReactNode }) {
  return (
    <h2
      className={css({
        fontSize: "2xs",
        fontWeight: "bold",
        letterSpacing: "0.14em",
        textTransform: "uppercase",
        color: "textMuted",
      })}
    >
      {children}
    </h2>
  );
}

function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint: string;
  children: React.ReactNode;
}) {
  return (
    <label className={vstack({ gap: "1.5", alignItems: "stretch" })}>
      <span className={css({ fontSize: "sm", fontWeight: "semibold", color: "text" })}>{label}</span>
      {children}
      <span className={css({ fontSize: "xs", color: "textMuted", lineHeight: "1.5" })}>{hint}</span>
    </label>
  );
}
