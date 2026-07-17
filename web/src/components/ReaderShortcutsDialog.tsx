import { Dialog, Portal } from "@ark-ui/react";
import { X } from "lucide-react";
import { css } from "styled-system/css";
import { hstack, vstack } from "styled-system/patterns";

/** Grouped the way a reader reaches for them, not the way they are implemented. */
const GROUPS: { heading: string; rows: { keys: string[]; label: string }[] }[] = [
  {
    heading: "Turning pages",
    rows: [
      { keys: ["←", "→"], label: "Turn left / right on screen" },
      { keys: ["h", "l"], label: "Turn left / right on screen" },
      { keys: ["j", "k"], label: "Next / previous in reading order" },
      { keys: ["Space"], label: "Next page" },
      { keys: ["Shift", "Space"], label: "Previous page" },
      { keys: ["PageDown", "PageUp"], label: "Next / previous in reading order" },
      { keys: ["Home", "End"], label: "First / last page" },
    ],
  },
  {
    heading: "Layout",
    rows: [
      { keys: ["f"], label: "Cycle fit: height, width, original" },
      { keys: ["s"], label: "Toggle two-page spread" },
      { keys: ["d"], label: "Toggle right-to-left reading order" },
    ],
  },
  {
    heading: "Elsewhere",
    rows: [
      { keys: ["?"], label: "Show this list" },
      { keys: ["Esc"], label: "Close the reader" },
    ],
  },
];

const GESTURES: { label: string }[] = [
  { label: "Swipe left or right to turn the page." },
  { label: "Tap the left or right third of the screen to turn the page." },
  { label: "Tap the middle to show or hide the controls." },
  { label: "Swipe down to close the comic, when the page fits the screen." },
];

const kbd = css({
  display: "inline-flex",
  alignItems: "center",
  justifyContent: "center",
  minW: "7",
  px: "1.5",
  py: "0.5",
  borderRadius: "sm",
  bg: "ink.750",
  borderWidth: "1px",
  borderColor: "border",
  borderBottomWidth: "2px",
  fontFamily: "mono",
  fontSize: "xs",
  color: "ink.200",
  flexShrink: 0,
});

export function ReaderShortcutsDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  return (
    <Dialog.Root open={open} onOpenChange={(d) => onOpenChange(d.open)} lazyMount unmountOnExit>
      <Portal>
        <Dialog.Backdrop
          className={css({
            position: "fixed",
            inset: "0",
            bg: "rgba(10, 8, 9, 0.7)",
            backdropFilter: "blur(3px)",
            zIndex: "50",
          })}
        />
        <Dialog.Positioner
          className={css({
            position: "fixed",
            inset: "0",
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
            p: "4",
            zIndex: "50",
          })}
        >
          <Dialog.Content
            className={vstack({
              gap: "5",
              alignItems: "stretch",
              w: "full",
              maxW: "md",
              maxH: "calc(100dvh - token(spacing.8))",
              overflowY: "auto",
              p: "6",
              bg: "surfaceRaised",
              borderWidth: "1px",
              borderColor: "border",
              borderRadius: "xl",
              boxShadow: "pop",
            })}
          >
            <div className={hstack({ justify: "space-between", gap: "4" })}>
              <Dialog.Title className={css({ fontSize: "xl", fontWeight: "bold", color: "text" })}>
                Shortcuts
              </Dialog.Title>
              <Dialog.CloseTrigger
                aria-label="Close the shortcut list"
                title="Close the shortcut list"
                className={css({
                  display: "inline-flex",
                  alignItems: "center",
                  justifyContent: "center",
                  w: "8",
                  h: "8",
                  borderRadius: "sm",
                  color: "textMuted",
                  cursor: "pointer",
                  flexShrink: 0,
                  _hover: { bg: "ink.750", color: "text" },
                })}
              >
                <X size={18} />
              </Dialog.CloseTrigger>
            </div>

            {GROUPS.map(({ heading, rows }) => (
              <div key={heading} className={vstack({ gap: "2", alignItems: "stretch" })}>
                <h2
                  className={css({
                    fontSize: "xs",
                    fontWeight: "bold",
                    color: "textMuted",
                    textTransform: "uppercase",
                    letterSpacing: "wide",
                  })}
                >
                  {heading}
                </h2>
                {rows.map(({ keys, label }) => (
                  <div key={label + keys.join()} className={hstack({ gap: "3", justify: "space-between" })}>
                    <span className={css({ fontSize: "sm", color: "text" })}>{label}</span>
                    <span className={hstack({ gap: "1", flexShrink: 0 })}>
                      {keys.map((k) => (
                        <kbd key={k} className={kbd}>
                          {k}
                        </kbd>
                      ))}
                    </span>
                  </div>
                ))}
              </div>
            ))}

            <div className={vstack({ gap: "2", alignItems: "stretch" })}>
              <h2
                className={css({
                  fontSize: "xs",
                  fontWeight: "bold",
                  color: "textMuted",
                  textTransform: "uppercase",
                  letterSpacing: "wide",
                })}
              >
                Touch
              </h2>
              {GESTURES.map(({ label }) => (
                <p key={label} className={css({ fontSize: "sm", color: "text", lineHeight: "1.5" })}>
                  {label}
                </p>
              ))}
            </div>
          </Dialog.Content>
        </Dialog.Positioner>
      </Portal>
    </Dialog.Root>
  );
}
