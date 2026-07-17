import type { ReactNode } from "react";
import { css } from "styled-system/css";
import { flex, vstack } from "styled-system/patterns";

/**
 * Every page opens the same way: a divider tab naming the section, the title,
 * one line of orientation, and the page's actions pinned right.
 */
export function PageHeader({
  eyebrow,
  title,
  subtitle,
  actions,
}: {
  eyebrow: string;
  title: string;
  subtitle?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div
      className={flex({
        justify: "space-between",
        align: { base: "stretch", md: "flex-end" },
        direction: { base: "column", md: "row" },
        gap: "4",
      })}
    >
      <div className={vstack({ gap: "1.5", alignItems: "flex-start", minW: "0" })}>
        <span
          className={css({
            fontSize: "2xs",
            fontWeight: "bold",
            letterSpacing: "0.14em",
            textTransform: "uppercase",
            color: "accent",
          })}
        >
          {eyebrow}
        </span>
        <h1
          className={css({
            fontSize: { base: "2xl", md: "3xl" },
            fontWeight: "bold",
            letterSpacing: "-0.025em",
            lineHeight: "1.1",
          })}
        >
          {title}
        </h1>
        {subtitle && (
          <p className={css({ color: "textMuted", fontSize: "sm" })}>{subtitle}</p>
        )}
      </div>
      {actions && <div className={flex({ gap: "2", flexShrink: 0 })}>{actions}</div>}
    </div>
  );
}
