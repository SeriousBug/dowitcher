import { useRef, useState } from "react";
import { FolderUp, Loader2 } from "lucide-react";
import { css } from "styled-system/css";
import { vstack } from "styled-system/patterns";
import { filesFromDrop } from "../lib/upload";
import { useComicUploads } from "../lib/useComicUploads";

/**
 * Wraps a page so a CBZ, PDF, or folder of images dropped anywhere over it is
 * uploaded, showing a dashed overlay while a drag is in progress and while the
 * upload runs. Shared by the Library and Collection pages; the Import page keeps
 * its own richer picker.
 *
 * collectionId files the dropped comics into that collection. disabled turns the
 * whole thing back into a plain wrapper — a non-owner viewing someone else's
 * collection cannot drop-add.
 */
export function DropOverlay({
  collectionId,
  disabled,
  children,
}: {
  collectionId?: string;
  disabled?: boolean;
  children: React.ReactNode;
}) {
  const { start, busy, progress } = useComicUploads({ collectionId });
  const [over, setOver] = useState(false);
  // dragenter/dragleave fire for every child element crossed, so a depth counter
  // is what keeps the overlay from flickering as the cursor moves inside.
  const depth = useRef(0);

  if (disabled) return <>{children}</>;

  const hasFiles = (e: React.DragEvent) => e.dataTransfer.types.includes("Files");

  return (
    <div
      onDragEnter={(e) => {
        if (!hasFiles(e)) return;
        e.preventDefault();
        depth.current += 1;
        setOver(true);
      }}
      onDragOver={(e) => {
        if (hasFiles(e)) e.preventDefault();
      }}
      onDragLeave={() => {
        depth.current = Math.max(0, depth.current - 1);
        if (depth.current === 0) setOver(false);
      }}
      onDrop={async (e) => {
        if (!hasFiles(e)) return;
        e.preventDefault();
        depth.current = 0;
        setOver(false);
        const dropped = await filesFromDrop(e.dataTransfer.items);
        start(dropped.length > 0 ? dropped : [...e.dataTransfer.files]);
      }}
      className={css({ position: "relative" })}
    >
      {children}

      {(over || busy) && (
        <div
          className={vstack({
            gap: "3",
            justify: "center",
            alignItems: "center",
            position: "fixed",
            inset: "0",
            zIndex: "40",
            // Visual only: the drop is handled by the wrapper below it.
            pointerEvents: "none",
            bg: "rgba(10, 8, 9, 0.72)",
            backdropFilter: "blur(2px)",
            p: "6",
            textAlign: "center",
          })}
        >
          <div
            className={vstack({
              gap: "3",
              alignItems: "center",
              px: "8",
              py: "7",
              borderRadius: "xl",
              borderWidth: "2px",
              borderStyle: "dashed",
              borderColor: "accent",
              bg: "surface",
              maxW: "md",
            })}
          >
            {busy ? (
              <>
                <Loader2
                  size={30}
                  className={css({
                    color: "accent",
                    animation: "spin 0.9s linear infinite",
                    _motionReduce: { animation: "none" },
                  })}
                  strokeWidth={1.5}
                />
                <span className={css({ fontSize: "lg", fontWeight: "bold", color: "text" })}>
                  {progress
                    ? progress.count > 1
                      ? `Uploading ${progress.name} (${progress.index + 1} of ${progress.count})`
                      : `Uploading ${progress.name}`
                    : "Uploading…"}
                </span>
                {progress && (
                  <span
                    className={css({ h: "3px", w: "full", borderRadius: "full", bg: "ink.750", overflow: "hidden" })}
                    role="progressbar"
                    aria-valuenow={progress.total > 0 ? Math.round((progress.loaded / progress.total) * 100) : 0}
                    aria-valuemin={0}
                    aria-valuemax={100}
                    aria-label="Upload progress"
                  >
                    <span
                      className={css({ display: "block", h: "full", bg: "accent", transition: "width 0.2s ease" })}
                      style={{ width: `${progress.total > 0 ? (progress.loaded / progress.total) * 100 : 0}%` }}
                    />
                  </span>
                )}
              </>
            ) : (
              <>
                <FolderUp size={30} className={css({ color: "accent" })} strokeWidth={1.5} />
                <span className={css({ fontSize: "lg", fontWeight: "bold", color: "text" })}>
                  Drop to add to your library
                </span>
                <span className={css({ fontSize: "sm", color: "textMuted", lineHeight: "1.5" })}>
                  A CBZ, a PDF, or a folder of images.
                </span>
              </>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
