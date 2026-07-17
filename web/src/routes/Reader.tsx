import { useCallback, useEffect, useState } from "react";
import { Link } from "@tanstack/react-router";
import { BookX, ChevronLeft, ChevronRight, X } from "lucide-react";
import { css } from "styled-system/css";
import { flex, hstack, vstack } from "styled-system/patterns";
import { comicLabel } from "../lib/format";
import type { ComicDetail } from "../api/generated";

// TODO(Reader): wire to GET /api/comics/{id} for the ComicDetail, page images
// from GET /api/comics/{id}/pages/{index}, and PUT /api/comics/{id}/progress on
// every page turn (debounced — a fast reader flips faster than the network).
// Seed `page` from detail.progress?.page so a comic reopens where it was left.
const detail: ComicDetail | null = null;

/**
 * Fullscreen and chromeless: no shell, no sidebar, nothing but the page. The
 * controls fade out while reading and come back on hover or keyboard, because
 * anything permanently on screen is something you stop seeing but still lose
 * pixels to.
 */
export function ReaderPage({ id }: { id: string }) {
  const [page, setPage] = useState(0);
  const [chromeVisible, setChromeVisible] = useState(true);

  const pageCount = detail?.pages.length ?? 0;

  const turn = useCallback(
    (delta: number) => {
      setPage((p) => Math.min(Math.max(p + delta, 0), Math.max(pageCount - 1, 0)));
    },
    [pageCount],
  );

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === "ArrowRight" || e.key === " ") turn(1);
      if (e.key === "ArrowLeft") turn(-1);
      // Any key means the reader is present; show them where they are.
      setChromeVisible(true);
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [turn]);

  // Idle hands mean reading. Pull the chrome back after a beat of stillness.
  useEffect(() => {
    if (!chromeVisible) return;
    const timer = window.setTimeout(() => setChromeVisible(false), 2600);
    return () => window.clearTimeout(timer);
  }, [chromeVisible, page]);

  if (!detail) {
    return (
      <div
        className={vstack({
          gap: "5",
          alignItems: "center",
          justify: "center",
          minH: "100vh",
          bg: "reader",
          p: "6",
          textAlign: "center",
        })}
      >
        <BookX size={34} className={css({ color: "ink.600" })} strokeWidth={1.5} />
        <div className={vstack({ gap: "1.5", maxW: "sm" })}>
          <h1 className={css({ fontSize: "lg", fontWeight: "bold" })}>
            This comic isn't here
          </h1>
          <p className={css({ color: "textMuted", fontSize: "sm", lineHeight: "1.6" })}>
            It may have been removed from the library, or the file has gone
            missing from disk.
          </p>
        </div>
        <Link
          to="/"
          className={css({
            px: "4",
            py: "2.5",
            borderRadius: "md",
            bg: "surfaceRaised",
            borderWidth: "1px",
            borderColor: "border",
            color: "text",
            fontSize: "sm",
            fontWeight: "semibold",
            textDecoration: "none",
            _hover: { bg: "ink.750" },
          })}
        >
          Back to the library
        </Link>
      </div>
    );
  }

  const current = detail.pages[page];

  return (
    <div
      onMouseMove={() => setChromeVisible(true)}
      className={css({
        position: "relative",
        minH: "100vh",
        bg: "reader",
        cursor: chromeVisible ? "default" : "none",
      })}
    >
      <header
        className={hstack({
          justify: "space-between",
          gap: "4",
          position: "fixed",
          top: "0",
          left: "0",
          right: "0",
          zIndex: "30",
          px: "4",
          h: "14",
          bg: "linear-gradient(to bottom, rgba(10, 8, 9, 0.95), transparent)",
          opacity: chromeVisible ? 1 : 0,
          transition: "opacity 0.25s ease",
          pointerEvents: chromeVisible ? "auto" : "none",
        })}
      >
        <Link
          to="/"
          aria-label="Close the reader"
          title="Close the reader"
          className={flex({
            align: "center",
            justify: "center",
            w: "9",
            h: "9",
            borderRadius: "md",
            color: "ink.200",
            flexShrink: 0,
            _hover: { bg: "rgba(255, 255, 255, 0.08)", color: "text" },
          })}
        >
          <X size={19} />
        </Link>

        <span
          className={css({
            fontSize: "sm",
            fontWeight: "semibold",
            color: "ink.200",
            truncate: true,
          })}
        >
          {comicLabel(detail.comic)}
        </span>

        <span
          className={css({
            fontFamily: "mono",
            fontSize: "xs",
            color: "ink.400",
            flexShrink: 0,
          })}
        >
          {page + 1} / {pageCount}
        </span>
      </header>

      <div className={flex({ align: "center", justify: "center", minH: "100vh" })}>
        <img
          src={`/api/comics/${id}/pages/${page}`}
          alt={`Page ${page + 1}`}
          width={current?.width}
          height={current?.height}
          className={css({ maxW: "full", maxH: "100vh", objectFit: "contain", display: "block" })}
        />
      </div>

      {/* Half the screen each: tap anywhere to turn, no aiming required. */}
      <button
        onClick={() => turn(-1)}
        disabled={page === 0}
        aria-label="Previous page"
        title="Previous page"
        className={flex({
          position: "fixed",
          left: "0",
          top: "0",
          bottom: "0",
          w: "35%",
          align: "center",
          justify: "flex-start",
          px: "4",
          color: "ink.300",
          cursor: "pointer",
          opacity: chromeVisible && page > 0 ? 0.75 : 0,
          transition: "opacity 0.25s ease",
          _disabled: { cursor: "default" },
        })}
      >
        <ChevronLeft size={38} strokeWidth={1.5} />
      </button>
      <button
        onClick={() => turn(1)}
        disabled={page >= pageCount - 1}
        aria-label="Next page"
        title="Next page"
        className={flex({
          position: "fixed",
          right: "0",
          top: "0",
          bottom: "0",
          w: "65%",
          align: "center",
          justify: "flex-end",
          px: "4",
          color: "ink.300",
          cursor: "pointer",
          opacity: chromeVisible && page < pageCount - 1 ? 0.75 : 0,
          transition: "opacity 0.25s ease",
          _disabled: { cursor: "default" },
        })}
      >
        <ChevronRight size={38} strokeWidth={1.5} />
      </button>

      <div
        className={css({
          position: "fixed",
          left: "0",
          right: "0",
          bottom: "0",
          zIndex: "30",
          h: "3px",
          bg: "rgba(255, 255, 255, 0.08)",
        })}
      >
        <span
          className={css({ display: "block", h: "full", bg: "accent", transition: "width 0.2s ease" })}
          style={{ width: `${pageCount ? ((page + 1) / pageCount) * 100 : 0}%` }}
        />
      </div>
    </div>
  );
}
