import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import {
  ArrowLeftRight,
  ArrowRightLeft,
  ArrowUpDown,
  BookOpen,
  Expand,
  Keyboard,
  Maximize,
  Shrink,
  X,
} from "lucide-react";
import { css, cx } from "styled-system/css";
import { flex, hstack } from "styled-system/patterns";
import type { FitMode } from "../lib/ReaderPrefs";

const FITS: { value: FitMode; label: string; icon: typeof ArrowUpDown }[] = [
  { value: "height", label: "Fit page height", icon: ArrowUpDown },
  { value: "width", label: "Fit page width", icon: ArrowLeftRight },
  { value: "original", label: "Original size", icon: Maximize },
];

// Pre-built class names: Panda cannot extract a style object reached through a
// variable, so the on/off states are two finished classes picked at runtime.
const TOGGLE_STATE = {
  on: css({ bg: "accent", color: "white", _hover: { bg: "accentHover" } }),
  off: css({ color: "ink.300", _hover: { bg: "rgba(255, 255, 255, 0.08)", color: "text" } }),
};

const iconButton = css({
  display: "inline-flex",
  alignItems: "center",
  justifyContent: "center",
  w: "8",
  h: "8",
  borderRadius: "sm",
  cursor: "pointer",
  flexShrink: 0,
  transition: "background 0.15s ease, color 0.15s ease",
  _motionReduce: { transition: "none" },
});

export function ReaderToolbar({
  title,
  page,
  pageCount,
  fit,
  onFit,
  spread,
  onSpread,
  rtl,
  onRtl,
  fullscreen,
  onFullscreen,
  visible,
  onShortcuts,
  download,
}: {
  title: string;
  page: number;
  pageCount: number;
  fit: FitMode;
  onFit: (mode: FitMode) => void;
  spread: boolean;
  onSpread: (on: boolean) => void;
  rtl: boolean;
  onRtl: (on: boolean) => void;
  /** null when fullscreen isn't on offer (standalone PWA or no API); the
   *  toolbar then omits the control entirely. */
  fullscreen: boolean | null;
  onFullscreen: () => void;
  visible: boolean;
  onShortcuts: () => void;
  /** Slot rather than a comic id: the toolbar has no business knowing what a
   *  download is, and the reader already holds the one it would ask about. */
  download?: ReactNode;
}) {
  return (
    <header
      className={hstack({
        justify: "space-between",
        gap: "4",
        position: "fixed",
        top: "0",
        left: "0",
        right: "0",
        zIndex: "30",
        px: "3",
        py: "2",
        // The reader has no shell, so on a notched phone the close button lands
        // under the sensor housing without this.
        pt: "calc(token(spacing.2) + env(safe-area-inset-top))",
        pl: "calc(token(spacing.3) + env(safe-area-inset-left))",
        pr: "calc(token(spacing.3) + env(safe-area-inset-right))",
        bg: "linear-gradient(to bottom, rgba(10, 8, 9, 0.95), rgba(10, 8, 9, 0))",
        opacity: visible ? 1 : 0,
        transition: "opacity 0.25s ease",
        _motionReduce: { transition: "none" },
        pointerEvents: visible ? "auto" : "none",
      })}
    >
      <div className={hstack({ gap: "2", minW: "0", flex: "1" })}>
        <Link
          to="/"
          aria-label="Close the reader"
          title="Close the reader (Esc)"
          className={cx(iconButton, TOGGLE_STATE.off, css({ textDecoration: "none" }))}
        >
          <X size={18} />
        </Link>
        <span
          className={css({
            fontSize: "sm",
            fontWeight: "semibold",
            color: "ink.200",
            truncate: true,
            minW: "0",
          })}
        >
          {title}
        </span>
      </div>

      {/* The visible counter is hidden below sm, and display:none takes it out of
          the a11y tree with the live region — so page turns go unannounced on a
          phone. The srOnly twin is always in the tree and does the announcing. */}
      <span
        className={css({
          fontFamily: "mono",
          fontSize: "xs",
          color: "ink.400",
          flexShrink: 0,
          display: { base: "none", sm: "block" },
        })}
        aria-hidden
      >
        {pageCount ? `${page + 1} / ${pageCount}` : "—"}
      </span>
      <span className={css({ srOnly: true })} aria-live="polite">
        {pageCount ? `${page + 1} / ${pageCount}` : "—"}
      </span>

      <div className={hstack({ gap: "1", flex: "1", justify: "flex-end" })}>
        {download}
        <div
          className={flex({
            gap: "0.5",
            p: "0.5",
            borderRadius: "md",
            bg: "rgba(255, 255, 255, 0.06)",
          })}
        >
          {FITS.map(({ value, label, icon: Icon }) => (
            <button
              key={value}
              onClick={() => onFit(value)}
              aria-label={label}
              aria-pressed={fit === value}
              title={label}
              className={cx(iconButton, fit === value ? TOGGLE_STATE.on : TOGGLE_STATE.off)}
            >
              <Icon size={16} />
            </button>
          ))}
        </div>

        <button
          onClick={() => onSpread(!spread)}
          aria-label="Two-page spread"
          aria-pressed={spread}
          title="Two-page spread"
          className={cx(iconButton, spread ? TOGGLE_STATE.on : TOGGLE_STATE.off)}
        >
          <BookOpen size={16} />
        </button>

        <button
          onClick={() => onRtl(!rtl)}
          aria-label="Right-to-left reading order"
          aria-pressed={rtl}
          title={rtl ? "Reading right to left (manga)" : "Reading left to right"}
          className={cx(iconButton, rtl ? TOGGLE_STATE.on : TOGGLE_STATE.off)}
        >
          <ArrowRightLeft size={16} />
        </button>

        {fullscreen !== null && (
          <button
            onClick={onFullscreen}
            aria-label={fullscreen ? "Exit fullscreen" : "Enter fullscreen"}
            aria-pressed={fullscreen}
            title={fullscreen ? "Exit fullscreen" : "Enter fullscreen"}
            className={cx(iconButton, fullscreen ? TOGGLE_STATE.on : TOGGLE_STATE.off)}
          >
            {fullscreen ? <Shrink size={16} /> : <Expand size={16} />}
          </button>
        )}

        <button
          onClick={onShortcuts}
          aria-label="Shortcuts"
          title="Shortcuts (?)"
          className={cx(iconButton, TOGGLE_STATE.off)}
        >
          <Keyboard size={16} />
        </button>
      </div>
    </header>
  );
}
