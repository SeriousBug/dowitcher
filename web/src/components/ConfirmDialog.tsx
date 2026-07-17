import { Dialog, Portal } from "@ark-ui/react";
import type { ReactNode } from "react";
import { css, cx } from "styled-system/css";
import { hstack, vstack } from "styled-system/patterns";

// Pre-built class names — Panda extracts at build time and cannot see a token
// passed to css() through a variable.
const CONFIRM_TONE = {
  accent: css({ bg: "accent", _hover: { bg: "accentHover" } }),
  danger: css({ bg: "danger", _hover: { bg: "dangerHover" } }),
};

export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  cancelLabel = "Never mind",
  tone = "accent",
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: ReactNode;
  confirmLabel: string;
  cancelLabel?: string;
  tone?: "accent" | "danger";
  onConfirm: () => void;
}) {
  return (
    <Dialog.Root
      open={open}
      onOpenChange={(d) => onOpenChange(d.open)}
      lazyMount
      unmountOnExit
    >
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
              gap: "4",
              alignItems: "stretch",
              w: "full",
              maxW: "sm",
              p: "6",
              bg: "surfaceRaised",
              borderWidth: "1px",
              borderColor: "border",
              borderRadius: "xl",
              boxShadow: "pop",
            })}
          >
            <Dialog.Title
              className={css({ fontSize: "xl", fontWeight: "bold", color: "text" })}
            >
              {title}
            </Dialog.Title>
            <Dialog.Description className={css({ color: "textMuted", lineHeight: "1.55" })}>
              {description}
            </Dialog.Description>
            <div className={hstack({ gap: "3", justify: "flex-end", mt: "1" })}>
              <Dialog.CloseTrigger
                className={css({
                  px: "4",
                  py: "2.5",
                  borderRadius: "md",
                  fontWeight: "semibold",
                  color: "text",
                  bg: "ink.750",
                  cursor: "pointer",
                  _hover: { bg: "ink.700" },
                })}
              >
                {cancelLabel}
              </Dialog.CloseTrigger>
              <button
                onClick={() => {
                  onConfirm();
                  onOpenChange(false);
                }}
                className={cx(
                  css({
                    px: "5",
                    py: "2.5",
                    borderRadius: "md",
                    fontWeight: "bold",
                    color: "white",
                    cursor: "pointer",
                  }),
                  CONFIRM_TONE[tone],
                )}
              >
                {confirmLabel}
              </button>
            </div>
          </Dialog.Content>
        </Dialog.Positioner>
      </Portal>
    </Dialog.Root>
  );
}
