import { BookMarked, X } from "lucide-react";
import { css } from "styled-system/css";
import { hstack } from "styled-system/patterns";

/**
 * Offers the saved position instead of taking it.
 *
 * The obvious implementation jumps to progress.page on open, and it is wrong in
 * the one direction that costs the reader something. Someone who wanted to
 * resume and is offered a button loses one click. Someone who opened the comic
 * to re-read it from the start and gets silently dropped on page 74 has to work
 * out what happened, find their way back, and — because scrubbing backwards
 * writes progress too — has already destroyed the bookmark they were carrying.
 * The asymmetry decides it: offer, don't teleport.
 */
export function ReaderResumeBanner({
  page,
  onResume,
  onDismiss,
}: {
  page: number;
  onResume: () => void;
  onDismiss: () => void;
}) {
  return (
    <div
      role="status"
      className={hstack({
        gap: "3",
        position: "fixed",
        left: "50%",
        transform: "translateX(-50%)",
        bottom: "calc(token(spacing.16) + env(safe-area-inset-bottom))",
        zIndex: "40",
        px: "4",
        py: "3",
        maxW: "calc(100vw - token(spacing.8))",
        borderRadius: "lg",
        bg: "surfaceRaised",
        borderWidth: "1px",
        borderColor: "border",
        boxShadow: "pop",
      })}
    >
      <BookMarked size={16} className={css({ color: "textMuted", flexShrink: 0 })} />
      <span className={css({ fontSize: "sm", color: "text", truncate: true })}>
        You left off on page {page + 1}.
      </span>
      <button
        onClick={onResume}
        className={css({
          px: "3",
          py: "1.5",
          borderRadius: "md",
          bg: "accent",
          color: "white",
          fontSize: "xs",
          fontWeight: "bold",
          cursor: "pointer",
          flexShrink: 0,
          _hover: { bg: "accentHover" },
        })}
      >
        Resume
      </button>
      <button
        onClick={onDismiss}
        aria-label="Stay on page 1"
        title="Stay on page 1"
        className={css({
          display: "inline-flex",
          alignItems: "center",
          justifyContent: "center",
          w: "7",
          h: "7",
          borderRadius: "md",
          color: "textMuted",
          cursor: "pointer",
          flexShrink: 0,
          _hover: { bg: "surface", color: "text" },
        })}
      >
        <X size={15} />
      </button>
    </div>
  );
}
