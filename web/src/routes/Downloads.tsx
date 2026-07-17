import { useEffect, useState } from "react";
import { Link } from "@tanstack/react-router";
import { CloudOff, DownloadCloud, Trash2, X } from "lucide-react";
import { css } from "styled-system/css";
import { hstack, vstack } from "styled-system/patterns";
import { Button } from "../components/Button";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";
import { cancelDownload, deleteDownload, downloadComic, storageUsage } from "../offline/downloads";
import { useDownloads } from "../offline/useDownloads";
import type { DownloadState } from "../offline/downloads";
import { toaster } from "../lib/toaster";
import { formatBytes, formatRelative } from "../lib/format";

/** What the origin is using, refreshed as downloads land. */
function useStorageUsage(dep: unknown): number | null {
  const [usage, setUsage] = useState<number | null>(null);
  useEffect(() => {
    let live = true;
    void storageUsage().then((n) => {
      if (live) setUsage(n);
    });
    return () => {
      live = false;
    };
  }, [dep]);
  return usage;
}

export function DownloadsPage() {
  const downloads = useDownloads();
  const [confirmDelete, setConfirmDelete] = useState<DownloadState | null>(null);

  const rows = [...downloads.values()].sort((a, b) => b.downloadedAt - a.downloadedAt);
  const manifestBytes = rows.reduce((n, r) => n + r.bytes, 0);
  // Recomputed whenever a download finishes or is removed — estimate() is a
  // snapshot, and a stale one on this page is the number people will read.
  const usage = useStorageUsage(`${rows.length}:${manifestBytes}`);

  async function remove(row: DownloadState) {
    await deleteDownload(row.comicId);
    toaster.create({ type: "success", title: `Removed ${row.title}` });
  }

  return (
    <div className={vstack({ gap: "7", alignItems: "stretch", maxW: "3xl" })}>
      <PageHeader
        eyebrow="Offline"
        title="Downloads"
        subtitle={
          rows.length === 0
            ? "Comics you save here stay readable with no connection."
            : `${rows.length === 1 ? "1 comic" : `${rows.length} comics`} on this device · ${formatBytes(manifestBytes)}`
        }
      />

      {rows.length === 0 ? (
        <EmptyState
          icon={CloudOff}
          title="Nothing saved for offline"
          action={
            <Link
              to="/"
              className={hstack({
                gap: "2",
                px: "4",
                py: "2.5",
                borderRadius: "md",
                bg: "accent",
                color: "white",
                fontSize: "sm",
                fontWeight: "bold",
                textDecoration: "none",
                _hover: { bg: "accentHover" },
              })}
            >
              <DownloadCloud size={16} />
              Find something to read
            </Link>
          }
        >
          Open a comic and use the download button in the reader's toolbar. Its
          pages are kept on this device, so you can read it on a plane, on the
          tube, or anywhere your server isn't.
        </EmptyState>
      ) : (
        <div className={vstack({ gap: "2", alignItems: "stretch" })}>
          {rows.map((row) => (
            <DownloadRow
              key={row.comicId}
              row={row}
              onDelete={() => setConfirmDelete(row)}
            />
          ))}
        </div>
      )}

      {usage !== null && (
        <p className={css({ fontSize: "xs", color: "textMuted", lineHeight: "1.6" })}>
          Longbox is using {formatBytes(usage)} on this device, counting the app
          itself and the covers you've browsed past.
          {/* No "x of y GB free" here, and no progress bar against a quota.
              Since Chrome 133 navigator.storage.estimate().quota reports a
              synthetic usage + 10 GiB on desktop and Android to frustrate
              fingerprinting, so any headroom figure derived from it is made up.
              The enforced limit — 60% of the disk, per origin — is unchanged
              but simply isn't reportable. usage is real; quota is not. Please
              don't "fix" this by dividing by quota. */}
        </p>
      )}

      <ConfirmDialog
        open={confirmDelete !== null}
        onOpenChange={(open) => {
          if (!open) setConfirmDelete(null);
        }}
        title="Remove this download?"
        description={
          <>
            <strong>{confirmDelete?.title}</strong> will be deleted from this
            device and won't be readable offline. It stays on your server, and
            your place in it is kept.
          </>
        }
        confirmLabel="Remove"
        tone="danger"
        onConfirm={() => confirmDelete && void remove(confirmDelete)}
      />
    </div>
  );
}

function DownloadRow({ row, onDelete }: { row: DownloadState; onDelete: () => void }) {
  const pct = row.pageCount > 0 ? Math.round((row.pagesDownloaded / row.pageCount) * 100) : 0;

  return (
    <div
      className={vstack({
        gap: "2.5",
        alignItems: "stretch",
        px: "3.5",
        py: "3",
        borderRadius: "md",
        bg: "surface",
        borderWidth: "1px",
        borderColor: "border",
      })}
    >
      <div className={hstack({ gap: "3", justify: "space-between" })}>
        <div className={vstack({ gap: "0.5", alignItems: "flex-start", minW: "0" })}>
          <Link
            to="/comic/$id"
            params={{ id: row.comicId }}
            className={css({
              fontSize: "sm",
              fontWeight: "semibold",
              color: "text",
              textDecoration: "none",
              truncate: true,
              _hover: { color: "accent" },
            })}
          >
            {row.title}
          </Link>
          <span className={css({ fontSize: "xs", color: "textMuted" })}>
            {row.complete
              ? `${row.pageCount} pages · ${formatBytes(row.bytes)} · saved ${formatRelative(row.downloadedAt)}`
              : `${row.pagesDownloaded} of ${row.pageCount} pages · ${formatBytes(row.bytes)} so far`}
          </span>
          {row.error && (
            <span className={css({ fontSize: "xs", color: "danger" })}>{row.error}</span>
          )}
        </div>

        <div className={hstack({ gap: "1", flexShrink: 0 })}>
          {row.active ? (
            <Button
              variant="ghost"
              icon={<X size={15} />}
              onClick={() => cancelDownload(row.comicId)}
            >
              Stop
            </Button>
          ) : (
            !row.complete && (
              <Button
                variant="ghost"
                icon={<DownloadCloud size={15} />}
                onClick={() => void downloadComic(row.comicId)}
              >
                Resume
              </Button>
            )
          )}
          <button
            onClick={onDelete}
            aria-label={`Remove ${row.title} from this device`}
            title={`Remove ${row.title} from this device`}
            className={css({
              p: "2",
              borderRadius: "md",
              color: "textMuted",
              cursor: "pointer",
              flexShrink: 0,
              _hover: { color: "danger", bg: "surfaceRaised" },
            })}
          >
            <Trash2 size={15} />
          </button>
        </div>
      </div>

      {/* The bar is for the download, not the book. A finished one has nothing
          left to say and the row reads cleaner without a full bar under it. */}
      {!row.complete && (
        <span
          className={css({ h: "3px", borderRadius: "full", bg: "ink.750", overflow: "hidden" })}
          role="progressbar"
          aria-valuenow={pct}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-label={`${row.title} download progress`}
        >
          <span
            className={css({ display: "block", h: "full", bg: "accent", transition: "width 0.3s ease" })}
            style={{ width: `${pct}%` }}
          />
        </span>
      )}
    </div>
  );
}
