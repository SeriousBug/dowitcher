import { useRef, useState } from "react";
import {
  CheckCircle2,
  ChevronDown,
  Copy,
  FolderUp,
  Loader2,
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
import { http, HttpError } from "../api/http";
import { toaster } from "../lib/toaster";
import { formatBytes } from "../lib/format";
import { filesFromDrop, isImage, pathOf, uploadWithProgress } from "../lib/upload";
import type { Collection, DupeGroup, ImportJob, ImportOptions } from "../api/generated";

const STAGE_LABEL: Record<string, string> = {
  uploading: "Uploading",
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
  const { jobs } = useLiveData();
  const queryClient = useQueryClient();
  const inputRef = useRef<HTMLInputElement>(null);

  const [files, setFiles] = useState<File[]>([]);
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

  const totalBytes = files.reduce((n, f) => n + f.size, 0);

  function take(picked: File[]) {
    const images = picked.filter(isImage);
    setFiles(images);
    // The folder's own name is the obvious title, and typing it again is busywork.
    if (images.length > 0 && !options.name) {
      const folder = pathOf(images[0]).split("/")[0];
      if (folder && folder !== images[0].name) setOptions((o) => ({ ...o, name: folder }));
    }
    if (picked.length > 0 && images.length === 0) {
      toaster.create({
        type: "error",
        title: "No images in there",
        description: "Longbox packs images into a CBZ, and that folder didn't have any.",
      });
    }
  }

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
        description: "Longbox is packing it now — watch it below.",
      });
      setFiles([]);
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

  const active = jobs.filter((j) => j.stage !== "done" && j.stage !== "failed");
  const finished = jobs.filter((j) => j.stage === "done" || j.stage === "failed");

  return (
    <div className={vstack({ gap: "7", alignItems: "stretch" })}>
      <PageHeader
        eyebrow="Intake"
        title="Import"
        subtitle="Point Longbox at a folder of images. It drops the duplicates, puts the pages in order, and packs a CBZ."
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
          <h2 className={css({ fontSize: "lg", fontWeight: "bold" })}>Drop a folder of images</h2>
          <p className={css({ color: "textMuted", fontSize: "sm", lineHeight: "1.6" })}>
            Pages get sorted by filename. Anything that turns out to be the same
            image twice only makes it in once.
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

        <Button variant="primary" icon={<Upload size={16} />} onClick={() => inputRef.current?.click()}>
          Choose a folder
        </Button>

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
              onClick={() => setFiles([])}
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

            {options.encode ? (
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

          <div className={hstack({ gap: "3", justify: "flex-end", flexWrap: "wrap" })}>
            {sent && (
              <span className={css({ fontSize: "xs", color: "textMuted", fontFamily: "mono" })}>
                {formatBytes(sent.loaded)} of {formatBytes(sent.total)}
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
              busy={start.isPending}
              disabled={files.length === 0}
              onClick={() => start.mutate()}
            >
              {files.length === 0
                ? "Choose a folder first"
                : `Import ${files.length} images`}
            </Button>
          </div>

          {sent && (
            <span className={css({ h: "3px", borderRadius: "full", bg: "ink.750", overflow: "hidden" })}>
              <span
                className={css({ display: "block", h: "full", bg: "accent", transition: "width 0.2s ease" })}
                style={{ width: `${sent.total > 0 ? (sent.loaded / sent.total) * 100 : 0}%` }}
              />
            </span>
          )}
        </div>
      </section>

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
          <SectionTitle>Recently finished</SectionTitle>
          {finished.map((job) => (
            <JobRow key={job.id} job={job} />
          ))}
        </section>
      )}
    </div>
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
              className={css({ color: "accent", flexShrink: 0, animation: "spin 0.9s linear infinite" })}
            />
          )}
          {/* A job exists before the server has read the options part, so one
              that dies during the upload never gets a name at all. */}
          <span className={css({ fontWeight: "semibold", truncate: true })}>
            {job.name || "Untitled import"}
          </span>
        </div>

        <div className={hstack({ gap: "3", flexShrink: 0 })}>
          <span className={css({ fontSize: "xs", color: "textMuted" })}>
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
        <span className={css({ h: "3px", borderRadius: "full", bg: "ink.750", overflow: "hidden" })}>
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
    <div className={vstack({ gap: "1.5", alignItems: "stretch" })}>
      <span className={css({ fontSize: "sm", fontWeight: "semibold", color: "text" })}>{label}</span>
      {children}
      <span className={css({ fontSize: "xs", color: "textMuted", lineHeight: "1.5" })}>{hint}</span>
    </div>
  );
}
