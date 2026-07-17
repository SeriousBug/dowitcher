import type { ButtonHTMLAttributes, ReactNode } from "react";
import { Loader2 } from "lucide-react";
import { css, cx } from "styled-system/css";
import { hstack } from "styled-system/patterns";

type Variant = "primary" | "secondary" | "ghost" | "danger";

// Each variant is its own literal css() call, and only the finished class name
// is looked up at runtime. Handing Panda a style object through a variable
// yields no CSS — it extracts at build time and never sees the value.
const VARIANT: Record<Variant, string> = {
  // Magenta is reserved for the primary action, one per screen at most.
  primary: css({ bg: "accent", color: "white", _hover: { bg: "accentHover" } }),
  secondary: css({
    bg: "surfaceRaised",
    color: "text",
    borderWidth: "1px",
    borderColor: "border",
    _hover: { bg: "ink.750", borderColor: "ink.600" },
  }),
  ghost: css({
    bg: "transparent",
    color: "textMuted",
    _hover: { bg: "surfaceRaised", color: "text" },
  }),
  danger: css({ bg: "danger", color: "white", _hover: { bg: "dangerHover" } }),
};

export function Button({
  variant = "secondary",
  busy = false,
  icon,
  children,
  className,
  disabled,
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: Variant;
  busy?: boolean;
  icon?: ReactNode;
}) {
  return (
    <button
      disabled={disabled || busy}
      aria-busy={busy}
      className={cx(
        hstack({
          gap: "2",
          px: "4",
          py: "2.5",
          borderRadius: "md",
          fontSize: "sm",
          fontWeight: "semibold",
          cursor: "pointer",
          flexShrink: 0,
          transition: "background 0.15s ease, color 0.15s ease, border-color 0.15s ease",
          _disabled: { opacity: 0.55, cursor: "not-allowed" },
        }),
        VARIANT[variant],
        className,
      )}
      {...rest}
    >
      {busy ? (
        <Loader2
          size={16}
          aria-hidden
          className={css({ animation: "spin 0.9s linear infinite" })}
        />
      ) : (
        icon
      )}
      {children}
    </button>
  );
}
