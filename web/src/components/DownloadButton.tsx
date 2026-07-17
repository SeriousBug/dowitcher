import { Check, Download, Loader2, RotateCw, X } from "lucide-react";
import { css, cx } from "styled-system/css";
import { hstack } from "styled-system/patterns";
import { cancelDownload, downloadComic } from "../offline/downloads";
import { useDownload } from "../offline/useDownloads";

// Finished class names — Panda extracts at build time and never sees a style
// object reached through a variable.
const BUTTON_STATE = {
  idle: css({ color: "ink.300", _hover: { bg: "rgba(255, 255, 255, 0.08)", color: "text" } }),
  busy: css({ color: "accent", _hover: { bg: "rgba(255, 255, 255, 0.08)" } }),
  done: css({ color: "ok", cursor: "default" }),
};

const base = hstack({
  gap: "1.5",
  justify: "center",
  h: "8",
  px: "2",
  minW: "8",
  borderRadius: "sm",
  fontSize: "2xs",
  fontWeight: "bold",
  fontVariantNumeric: "tabular-nums",
  cursor: "pointer",
  flexShrink: 0,
  transition: "background 0.15s ease, color 0.15s ease",
  _motionReduce: { transition: "none" },
});

/**
 * Download, resume, cancel and "saved", in the one slot the reader has for it.
 * Which of those it is depends on the manifest row, not on what this component
 * last did — the download outlives the button, and a second tab is looking at
 * the same row.
 */
export function DownloadButton({ comicId }: { comicId: string }) {
  const state = useDownload(comicId);

  if (state?.active) {
    const pct = state.pageCount > 0 ? Math.round((state.pagesDownloaded / state.pageCount) * 100) : 0;
    return (
      <button
        onClick={() => cancelDownload(comicId)}
        aria-label={`Stop downloading — ${pct}% saved`}
        title={`Downloading ${state.pagesDownloaded} of ${state.pageCount} pages. Click to stop.`}
        className={cx(base, BUTTON_STATE.busy)}
      >
        <Loader2
          size={15}
          className={css({
            animation: "spin 0.9s linear infinite",
            _motionReduce: { animation: "none" },
          })}
        />
        {pct}%
        <X size={13} />
      </button>
    );
  }

  if (state?.complete) {
    return (
      <span
        aria-label="Saved for offline reading"
        title="Saved for offline reading. Remove it from Downloads."
        className={cx(base, BUTTON_STATE.done)}
      >
        <Check size={15} />
      </span>
    );
  }

  // A row that exists but isn't complete is an interrupted download, and its
  // pages are still on disk — offering "download" again would suggest starting
  // over, which is not what happens.
  const resuming = Boolean(state);
  return (
    <button
      onClick={() => void downloadComic(comicId)}
      aria-label={resuming ? "Resume the download" : "Download for offline reading"}
      title={
        resuming
          ? `Resume — ${state!.pagesDownloaded} of ${state!.pageCount} pages already saved`
          : "Download for offline reading"
      }
      className={cx(base, BUTTON_STATE.idle)}
    >
      {resuming ? <RotateCw size={15} /> : <Download size={15} />}
    </button>
  );
}
