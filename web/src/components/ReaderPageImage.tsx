import { useCallback, useState } from "react";
import { ImageOff, RotateCw } from "lucide-react";
import { css, cx } from "styled-system/css";
import { vstack } from "styled-system/patterns";
import { TRIM_RATIO } from "../lib/ReaderLayout";
import type { Page } from "../api/generated";

/** Page bytes. Served `immutable`, so a re-request of a page already seen is free. */
export function pageSrc(comicId: string, index: number, attempt = 0): string {
  const base = `/api/comics/${comicId}/pages/${index}`;
  // A retry has to miss the cache: the failure itself isn't cached, but a
  // truncated or errored response can be, and re-requesting the identical URL
  // would just re-serve whatever went wrong.
  return attempt === 0 ? base : `${base}?retry=${attempt}`;
}

// Finished class names per (fit mode x panes). Panda extracts CSS at build time
// and never sees a value handed to css() through a variable, so every variant is
// its own literal call and only the class name is chosen at runtime.
const IMG_CLASS: Record<string, string> = {
  "height-1": css({ w: "auto", h: "auto", maxW: "100%", maxH: "100dvh", objectFit: "contain" }),
  "height-2": css({ w: "auto", h: "auto", maxW: "50%", maxH: "100dvh", objectFit: "contain" }),
  "width-1": css({ w: "100%", h: "auto", maxW: "100%" }),
  "width-2": css({ w: "50%", h: "auto", maxW: "50%" }),
  // Original size overflows on purpose — the document scrolls under the chrome.
  "original-1": css({ w: "auto", h: "auto", maxW: "none", maxH: "none" }),
  "original-2": css({ w: "auto", h: "auto", maxW: "none", maxH: "none" }),
};

// Idles rather than pulses, matching the library's cover skeletons: a page
// blinking at you in a dark room is worse than a page that just sits there.
const SKELETON_CLASS = css({
  bg: "surface",
  animation: "shimmer 2.4s ease-in-out infinite",
  _motionReduce: { animation: "none" },
});

/**
 * One page, sized before its bytes arrive. The width/height attributes give the
 * browser the aspect box up front so the page doesn't jump into place when it
 * decodes, and `aspect-ratio: auto W/H` means the real ratio silently takes over
 * once the image loads — which is what rescues the AVIF pages that report 0x0.
 */
export function ReaderPageImage({
  comicId,
  index,
  page,
  fit,
  panes,
  onNaturalSize,
}: {
  comicId: string;
  index: number;
  page: Page | undefined;
  fit: "height" | "width" | "original";
  panes: 1 | 2;
  onNaturalSize?: (index: number, width: number, height: number) => void;
}) {
  const [attempt, setAttempt] = useState(0);
  const [failed, setFailed] = useState(false);
  const [loaded, setLoaded] = useState(false);

  // Some encoders (AVIF especially) leave the store with no dimensions. Guess
  // comic trim rather than render a zero-height box: a wrong-but-plausible
  // reservation costs a small settle, no reservation costs a full reflow.
  const known = Boolean(page?.width && page?.height);
  const width = known ? page!.width! : Math.round(1000 * TRIM_RATIO);
  const height = known ? page!.height! : 1000;

  const measure = useCallback(
    (el: HTMLImageElement | null) => {
      if (!el) return;
      // A preloaded page is already decoded when this mounts and will never fire
      // onLoad, so it would sit under the skeleton forever.
      if (el.complete && el.naturalWidth > 0) {
        setLoaded(true);
        onNaturalSize?.(index, el.naturalWidth, el.naturalHeight);
      }
    },
    [index, onNaturalSize],
  );

  if (failed) {
    return (
      <div
        className={vstack({
          gap: "3",
          justify: "center",
          alignItems: "center",
          maxH: "100dvh",
          w: "full",
          maxW: "md",
          bg: "surface",
          borderRadius: "md",
          color: "textMuted",
          p: "6",
          // The page-turn tap zones are fixed, so they paint over this box and
          // eat the retry click. Lift it into the positioned layer above them,
          // but keep it under the toolbar at 30.
          position: "relative",
          zIndex: "20",
        })}
        style={{ aspectRatio: `${width} / ${height}` }}
      >
        <ImageOff size={30} strokeWidth={1.5} className={css({ color: "ink.600" })} />
        <p className={css({ fontSize: "sm", textAlign: "center", lineHeight: "1.6" })}>
          Page {index + 1} didn&apos;t load.
        </p>
        <button
          onClick={() => {
            setFailed(false);
            setLoaded(false);
            setAttempt((a) => a + 1);
          }}
          className={css({
            display: "inline-flex",
            alignItems: "center",
            gap: "2",
            px: "3.5",
            py: "2",
            borderRadius: "md",
            bg: "surfaceRaised",
            borderWidth: "1px",
            borderColor: "border",
            color: "text",
            fontSize: "sm",
            fontWeight: "semibold",
            cursor: "pointer",
            _hover: { bg: "ink.750", borderColor: "ink.600" },
          })}
        >
          <RotateCw size={14} />
          Try again
        </button>
      </div>
    );
  }

  return (
    <img
      ref={measure}
      key={attempt}
      src={pageSrc(comicId, index, attempt)}
      alt={`Page ${index + 1}`}
      width={width}
      height={height}
      draggable={false}
      onLoad={(e) => {
        setLoaded(true);
        const el = e.currentTarget;
        onNaturalSize?.(index, el.naturalWidth, el.naturalHeight);
      }}
      onError={() => setFailed(true)}
      className={cx(
        css({ display: "block", userSelect: "none" }),
        IMG_CLASS[`${fit}-${panes}`],
        !loaded && SKELETON_CLASS,
      )}
    />
  );
}
