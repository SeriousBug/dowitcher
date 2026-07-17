import { Toast, Toaster } from "@ark-ui/react";
import { CheckCircle2, AlertTriangle, Loader2, Info, X } from "lucide-react";
import { css, cx } from "styled-system/css";
import { hstack, vstack } from "styled-system/patterns";
import { toaster } from "../lib/toaster";

// Finished class names, not token names: Panda extracts styles at build time, so
// a colour handed to css() through a variable would produce no rule at all.
const ACCENT: Record<string, string> = {
  success: css({ borderLeftColor: "ok" }),
  error: css({ borderLeftColor: "danger" }),
  loading: css({ borderLeftColor: "accent" }),
  info: css({ borderLeftColor: "ink.400" }),
};

function ToastIcon({ type }: { type?: string }) {
  if (type === "success")
    return <CheckCircle2 size={19} className={css({ flexShrink: 0, color: "ok" })} />;
  if (type === "error")
    return <AlertTriangle size={19} className={css({ flexShrink: 0, color: "danger" })} />;
  if (type === "loading")
    return (
      <Loader2
        size={19}
        className={css({ flexShrink: 0, color: "accent", animation: "spin 0.9s linear infinite" })}
      />
    );
  return <Info size={19} className={css({ flexShrink: 0, color: "textMuted" })} />;
}

export function ToasterView() {
  return (
    <Toaster toaster={toaster}>
      {(toast) => (
        <Toast.Root
          className={cx(
            hstack({
              gap: "3",
              alignItems: "flex-start",
              w: { base: "calc(100vw - 32px)", sm: "sm" },
              p: "4",
              bg: "surfaceRaised",
              borderRadius: "lg",
              borderWidth: "1px",
              borderColor: "border",
              borderLeftWidth: "3px",
              boxShadow: "pop",
            }),
            ACCENT[toast.type ?? "info"] ?? ACCENT.info,
          )}
        >
          <ToastIcon type={toast.type} />
          <div className={vstack({ gap: "0.5", alignItems: "stretch", flex: "1", minW: "0" })}>
            <Toast.Title className={css({ fontWeight: "bold", fontSize: "sm", color: "text" })}>
              {toast.title}
            </Toast.Title>
            {toast.description ? (
              <Toast.Description
                className={css({ fontSize: "sm", color: "textMuted", wordBreak: "break-word" })}
              >
                {toast.description}
              </Toast.Description>
            ) : null}
          </div>
          <Toast.CloseTrigger
            aria-label="Dismiss"
            title="Dismiss"
            className={css({
              color: "textMuted",
              cursor: "pointer",
              borderRadius: "sm",
              p: "0.5",
              _hover: { color: "text", bg: "ink.750" },
            })}
          >
            <X size={16} />
          </Toast.CloseTrigger>
        </Toast.Root>
      )}
    </Toaster>
  );
}
