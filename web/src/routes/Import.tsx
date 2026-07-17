import { useState } from "react";
import { CheckCircle2, FolderUp, Loader2, TriangleAlert, Upload, X } from "lucide-react";
import { Link } from "@tanstack/react-router";
import { css } from "styled-system/css";
import { grid, hstack, vstack } from "styled-system/patterns";
import { Button } from "../components/Button";
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";
import { useLiveData } from "../live/LiveData";
import type { ImportJob, ImportOptions } from "../api/generated";

// TODO(Import): wire the picker to POST /api/imports (multipart: the files plus
// an ImportOptions JSON part) and the cancel button to
// POST /api/imports/{id}/cancel. Job progress needs no fetching — it already
// arrives over the WS and is read from useLiveData() below.

const STAGE_LABEL: Record<string, string> = {
  uploading: "Uploading",
  hashing: "Fingerprinting pages",
  thumbnailing: "Making thumbnails",
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

export function ImportPage() {
  const { jobs } = useLiveData();
  const [options, setOptions] = useState<ImportOptions>({
    name: "",
    threshold: 3,
    exact: false,
    encode: "",
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
        className={vstack({
          gap: "4",
          alignItems: "center",
          py: "10",
          px: "6",
          borderRadius: "xl",
          borderWidth: "1px",
          borderStyle: "dashed",
          borderColor: "border",
          bg: "surface",
          textAlign: "center",
          transition: "border-color 0.15s ease",
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
        <Button variant="primary" icon={<Upload size={16} />}>
          Choose files
        </Button>
      </section>

      <section className={vstack({ gap: "4", alignItems: "stretch" })}>
        <SectionTitle>Options</SectionTitle>
        <div
          className={grid({
            columns: { base: 1, md: 2 },
            gap: "4",
            p: "5",
            borderRadius: "lg",
            bg: "surface",
            borderWidth: "1px",
            borderColor: "border",
          })}
        >
          <Field label="Name" hint="What this ends up called on the shelf.">
            <input
              value={options.name}
              onChange={(e) => setOptions({ ...options, name: e.target.value })}
              placeholder="Untitled"
              className={inputClass}
            />
          </Field>

          <Field label="Re-encode pages" hint="Smaller files, slower import. AVIF wins on size.">
            <select
              value={options.encode}
              onChange={(e) => setOptions({ ...options, encode: e.target.value })}
              className={inputClass}
            >
              {ENCODINGS.map((e) => (
                <option key={e.value} value={e.value}>
                  {e.label}
                </option>
              ))}
            </select>
          </Field>

          <Field
            label="Duplicate sensitivity"
            hint={
              options.exact
                ? "Exact matching is on — only byte-for-byte copies are dropped."
                : "Higher catches more near-duplicates, at the risk of dropping a page that just looks similar."
            }
          >
            <div className={hstack({ gap: "3" })}>
              <input
                type="range"
                min={0}
                max={10}
                step={0.5}
                value={options.threshold}
                disabled={options.exact}
                onChange={(e) => setOptions({ ...options, threshold: Number(e.target.value) })}
                className={css({ flex: "1", accentColor: "accent", cursor: "pointer", _disabled: { opacity: 0.4 } })}
              />
              <span className={css({ fontFamily: "mono", fontSize: "sm", color: "textMuted", w: "8" })}>
                {options.threshold.toFixed(1)}
              </span>
            </div>
          </Field>

          <Field label="Exact matches only" hint="Skips the image comparison entirely.">
            <label className={hstack({ gap: "2.5", cursor: "pointer" })}>
              <input
                type="checkbox"
                checked={options.exact}
                onChange={(e) => setOptions({ ...options, exact: e.target.checked })}
                className={css({ w: "4", h: "4", accentColor: "accent", cursor: "pointer" })}
              />
              <span className={css({ fontSize: "sm", color: "text" })}>
                Only drop identical files
              </span>
            </label>
          </Field>
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
          <span className={css({ fontWeight: "semibold", truncate: true })}>{job.name}</span>
        </div>

        <div className={hstack({ gap: "3", flexShrink: 0 })}>
          <span className={css({ fontSize: "xs", color: "textMuted" })}>
            {STAGE_LABEL[job.stage] ?? job.stage}
          </span>
          {!done && !failed && (
            <button
              aria-label={`Cancel importing ${job.name}`}
              title="Cancel this import"
              className={css({
                color: "textMuted",
                cursor: "pointer",
                borderRadius: "sm",
                p: "0.5",
                _hover: { color: "danger", bg: "surfaceRaised" },
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
        {job.pageCount > 0 && <span>{job.pageCount} pages kept</span>}
        {job.exactDupes > 0 && <span>{job.exactDupes} exact duplicates dropped</span>}
        {job.nearDupes > 0 && <span>{job.nearDupes} near-duplicates dropped</span>}
        {job.message && (
          <span className={css({ color: failed ? "rust.300" : "textMuted" })}>{job.message}</span>
        )}
        {done && job.comicId && (
          <Link
            to="/comic/$id"
            params={{ id: job.comicId }}
            className={css({ color: "accent", fontWeight: "semibold", textDecoration: "none", _hover: { textDecoration: "underline" } })}
          >
            Read it now
          </Link>
        )}
      </div>
    </div>
  );
}

const inputClass = css({
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
