import { Slider } from "@ark-ui/react";
import { css } from "styled-system/css";
import { hstack } from "styled-system/patterns";

/**
 * Jump-anywhere control. It always runs left-to-right, even for a right-to-left
 * comic: the numbers beside it count up either way, and a slider whose handle
 * travels backwards as the number rises is a puzzle rather than a shortcut.
 *
 * Dragging pins the chrome open via onFocusChange — the toolbar fading out from
 * under the handle you are holding is the one moment stillness doesn't mean
 * "leave me alone".
 */
export function ReaderScrubber({
  page,
  pageCount,
  onScrub,
  onDraggingChange,
  visible,
}: {
  page: number;
  pageCount: number;
  onScrub: (page: number) => void;
  onDraggingChange: (dragging: boolean) => void;
  visible: boolean;
}) {
  if (pageCount <= 1) return null;

  return (
    <div
      className={hstack({
        gap: "3",
        position: "fixed",
        left: "0",
        right: "0",
        bottom: "0",
        zIndex: "30",
        px: "5",
        pb: "calc(token(spacing.4) + env(safe-area-inset-bottom))",
        pt: "10",
        bg: "linear-gradient(to top, rgba(10, 8, 9, 0.95), rgba(10, 8, 9, 0))",
        opacity: visible ? 1 : 0,
        transition: "opacity 0.25s ease",
        _motionReduce: { transition: "none" },
        pointerEvents: visible ? "auto" : "none",
      })}
    >
      <Slider.Root
        value={[page]}
        min={0}
        max={pageCount - 1}
        step={1}
        onValueChange={(d) => onScrub(d.value[0] ?? 0)}
        onFocusChange={(d) => onDraggingChange(d.focusedIndex !== -1)}
        thumbAlignment="center"
        className={css({ flex: "1", minW: "0" })}
      >
        <Slider.Label className={css({ srOnly: true })}>Jump to page</Slider.Label>
        <Slider.Control className={css({ display: "flex", alignItems: "center", h: "6" })}>
          <Slider.Track
            className={css({
              h: "3px",
              flex: "1",
              borderRadius: "full",
              bg: "rgba(255, 255, 255, 0.18)",
            })}
          >
            <Slider.Range className={css({ h: "full", borderRadius: "full", bg: "accent" })} />
          </Slider.Track>
          <Slider.Thumb
            index={0}
            className={css({
              w: "3.5",
              h: "3.5",
              borderRadius: "full",
              bg: "accent",
              boxShadow: "0 0 0 3px rgba(10, 8, 9, 0.8)",
              cursor: "grab",
              _active: { cursor: "grabbing" },
            })}
          >
            <Slider.HiddenInput />
          </Slider.Thumb>
        </Slider.Control>
      </Slider.Root>

      <span
        className={css({
          fontFamily: "mono",
          fontSize: "xs",
          color: "ink.300",
          flexShrink: 0,
          fontVariantNumeric: "tabular-nums",
        })}
      >
        {page + 1} / {pageCount}
      </span>
    </div>
  );
}
