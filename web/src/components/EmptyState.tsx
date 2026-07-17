import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import { css } from "styled-system/css";
import { flex, vstack } from "styled-system/patterns";

/**
 * The empty screen is where most people meet a self-hosted app for the first
 * time, so it says what goes here and gives them the one thing to do next.
 */
export function EmptyState({
  icon: Icon,
  title,
  children,
  action,
}: {
  icon: LucideIcon;
  title: string;
  children: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div
      className={vstack({
        gap: "4",
        alignItems: "center",
        textAlign: "center",
        py: { base: "12", md: "20" },
        px: "6",
        borderWidth: "1px",
        borderStyle: "dashed",
        borderColor: "border",
        borderRadius: "xl",
      })}
    >
      <span
        className={flex({
          align: "center",
          justify: "center",
          w: "14",
          h: "14",
          borderRadius: "lg",
          bg: "surfaceRaised",
          color: "ink.500",
        })}
      >
        <Icon size={26} strokeWidth={1.6} />
      </span>
      <div className={vstack({ gap: "1.5", maxW: "sm" })}>
        <h2 className={css({ fontSize: "lg", fontWeight: "bold", color: "text" })}>{title}</h2>
        <p className={css({ color: "textMuted", fontSize: "sm", lineHeight: "1.6" })}>
          {children}
        </p>
      </div>
      {action}
    </div>
  );
}
