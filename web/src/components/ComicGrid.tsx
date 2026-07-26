import type { ReactNode } from "react";
import { css } from "styled-system/css";
import { grid, hstack, vstack } from "styled-system/patterns";
import { ComicCard } from "./ComicCard";
import type { Comic, Progress } from "../api/generated";

/** The one cover grid. Every page that shows covers uses these columns. */
export function ComicGrid({ children }: { children: ReactNode }) {
  return (
    <div
      className={grid({
        columns: { base: 2, sm: 3, md: 4, lg: 5, xl: 6 },
        gap: { base: "4", md: "5" },
      })}
    >
      {children}
    </div>
  );
}

/**
 * Placeholders in the real cover aspect ratio, so the grid it fills is the grid
 * that was already there and nothing jumps when the art lands.
 */
export function ComicGridSkeleton({ count = 12 }: { count?: number }) {
  return (
    <ComicGrid>
      {Array.from({ length: count }, (_, i) => (
        <div key={i} className={vstack({ gap: "2.5", alignItems: "stretch" })}>
          <div
            className={css({
              aspectRatio: "0.65",
              borderRadius: "md",
              bg: "surface",
              borderWidth: "1px",
              borderColor: "ink.750",
              animation: "shimmer 2.4s ease-in-out infinite",
            })}
            // Offsetting each tile keeps a screenful of them from breathing in
            // unison, which reads as a broken page rather than a loading one.
            style={{ animationDelay: `${(i % 6) * 0.18}s` }}
          />
          <div className={css({ h: "3", w: "4/5", borderRadius: "sm", bg: "surface" })} />
          <div className={css({ h: "2.5", w: "2/5", borderRadius: "sm", bg: "ink.850" })} />
        </div>
      ))}
    </ComicGrid>
  );
}

/**
 * A cover plus whatever actions the page allows on it. The card itself is a link
 * into the reader, so the actions sit above it in their own layer rather than
 * inside it — a button nested in an anchor is a button you can't reliably click.
 *
 * A comic whose file has vanished stays on the shelf, dimmed and labelled: the
 * row is kept on purpose so an unmounted volume doesn't take its tags and
 * reading progress with it, and hiding it here would tell the opposite story.
 */
export function ComicTile({
  comic,
  progress,
  actions,
}: {
  comic: Comic;
  progress?: Progress;
  actions?: ReactNode;
}) {
  return (
    <div
      className={css({
        position: "relative",
        "&:hover .tile-actions, &:focus-within .tile-actions": { opacity: 1 },
      })}
    >
      <div className={comic.missing ? MISSING : undefined}>
        <ComicCard comic={comic} progress={progress} />
      </div>

      {comic.missing && (
        <span
          className={hstack({
            gap: "1.5",
            mt: "1",
            fontSize: "2xs",
            fontWeight: "semibold",
            color: "attention",
          })}
        >
          File not found
        </span>
      )}

      {actions && (
        <div
          className={`tile-actions ${hstack({
            position: "absolute",
            top: "2",
            left: "2",
            gap: "1",
            opacity: 0,
            transition: "opacity 0.15s ease",
            // Touch has no hover, so the actions have to be there all along.
            "@media (hover: none)": { opacity: 1 },
          })}`}
        >
          {actions}
        </div>
      )}
    </div>
  );
}

/** Dimmed, and greyed enough that it reads as absent rather than merely dark. */
const MISSING = css({ opacity: 0.42, filter: "saturate(0.35)" });

/** Square icon button for the overlay above a cover. */
// disabled carries the reason rather than a boolean: a greyed-out button with no
// explanation is worse than no button, and the reason is the only thing the
// hover state has room to say.
export function TileButton({
  onClick,
  label,
  disabled,
  children,
}: {
  onClick: () => void;
  label: string;
  disabled?: string;
  children: ReactNode;
}) {
  return (
    <button
      onClick={onClick}
      disabled={disabled !== undefined}
      aria-label={label}
      title={disabled ?? label}
      className={css({
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        w: "7",
        h: "7",
        borderRadius: "sm",
        bg: "rgba(10, 8, 9, 0.82)",
        color: "ink.100",
        cursor: "pointer",
        backdropFilter: "blur(4px)",
        _hover: { bg: "accent", color: "white" },
        _disabled: {
          color: "ink.500",
          cursor: "not-allowed",
          _hover: { bg: "rgba(10, 8, 9, 0.82)", color: "ink.500" },
        },
      })}
    >
      {children}
    </button>
  );
}
