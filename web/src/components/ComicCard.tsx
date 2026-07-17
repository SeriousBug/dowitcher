import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { BookOpen, FileWarning } from "lucide-react";
import { css } from "styled-system/css";
import { flex, vstack } from "styled-system/patterns";
import { comicLabel } from "../lib/format";
import type { Comic, Progress } from "../api/generated";

/**
 * The cover is the card. No frame, no panel, no chrome — the artwork sits on the
 * background and the only thing the app draws over it is the progress a reader
 * has actually made. Everything else stays underneath in small quiet type.
 */
export function ComicCard({ comic, progress }: { comic: Comic; progress?: Progress }) {
  const [failed, setFailed] = useState(false);
  const pct =
    progress && progress.pageCount > 0
      ? Math.round(((progress.page + 1) / progress.pageCount) * 100)
      : 0;

  return (
    <Link
      to="/comic/$id"
      params={{ id: comic.id }}
      className={vstack({
        gap: "2.5",
        alignItems: "stretch",
        textDecoration: "none",
        borderRadius: "md",
        transition: "transform 0.18s ease",
        _hover: { transform: "translateY(-3px)" },
        "&:hover .cover": { boxShadow: "pop", borderColor: "ink.600" },
        "&:hover .title": { color: "accent" },
      })}
    >
      <div
        className={`cover ${flex({
          position: "relative",
          align: "center",
          justify: "center",
          aspectRatio: "0.65",
          borderRadius: "md",
          borderWidth: "1px",
          borderColor: "ink.750",
          bg: "surface",
          overflow: "hidden",
          boxShadow: "cover",
          transition: "box-shadow 0.18s ease, border-color 0.18s ease",
        })}`}
      >
        {failed ? (
          <span className={css({ color: "ink.600" })}>
            <BookOpen size={30} strokeWidth={1.5} />
          </span>
        ) : (
          <img
            src={`/api/comics/${comic.id}/cover`}
            alt=""
            loading="lazy"
            onError={() => setFailed(true)}
            className={css({ w: "full", h: "full", objectFit: "cover", display: "block" })}
          />
        )}

        {comic.missing && (
          <span
            className={flex({
              position: "absolute",
              top: "2",
              right: "2",
              align: "center",
              justify: "center",
              w: "7",
              h: "7",
              borderRadius: "sm",
              bg: "rgba(10, 8, 9, 0.82)",
              color: "attention",
            })}
            title="This file has gone missing"
            aria-label="This file has gone missing"
          >
            <FileWarning size={15} />
          </span>
        )}

        {pct > 0 && (
          <span
            className={css({
              position: "absolute",
              left: "0",
              right: "0",
              bottom: "0",
              h: "3px",
              bg: "rgba(10, 8, 9, 0.75)",
            })}
          >
            <span
              className={css({ display: "block", h: "full", bg: "accent" })}
              style={{ width: `${pct}%` }}
            />
          </span>
        )}
      </div>

      <div className={vstack({ gap: "0.5", alignItems: "stretch", minW: "0" })}>
        <span
          className={`title ${css({
            fontSize: "sm",
            fontWeight: "semibold",
            color: "text",
            lineHeight: "1.3",
            transition: "color 0.15s ease",
            lineClamp: "2",
          })}`}
        >
          {comicLabel(comic)}
        </span>
        <span className={css({ fontSize: "xs", color: "textMuted" })}>
          {progress?.completed
            ? "Read"
            : pct > 0
              ? `${pct}% · page ${progress!.page + 1} of ${progress!.pageCount}`
              : `${comic.pageCount} pages`}
        </span>
      </div>
    </Link>
  );
}
