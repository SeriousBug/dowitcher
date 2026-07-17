import { css } from "styled-system/css";
import { hstack } from "styled-system/patterns";
import { useLiveData } from "../live/LiveData";

const LABEL: Record<string, string> = {
  open: "Live",
  connecting: "Reconnecting…",
  closed: "Not connected",
};

/**
 * Just the dot. Scan progress and import spinners all arrive over the stream, so
 * a reader needs to know at a glance whether what they are looking at is still
 * true — but a healthy connection is the normal case and doesn't deserve a word
 * of chrome.
 */
export function ConnectionLight() {
  const { connection } = useLiveData();
  const label = LABEL[connection] ?? "";

  return (
    <span className={css({ display: "flex", flexShrink: 0 })} title={label}>
      <span className={`conn-dot conn-dot--${connection}`} aria-hidden />
      <span className={css({ srOnly: true })} aria-live="polite">
        {label}
      </span>
    </span>
  );
}

/**
 * The sentence version, for the one place with room for it. Silent while the
 * stream is healthy: this only speaks up when something is wrong.
 */
export function ConnectionNotice() {
  const { connection } = useLiveData();
  if (connection === "open") return null;

  return (
    <span
      className={hstack({
        gap: "2",
        px: "3",
        py: "2",
        borderRadius: "md",
        bg: "surface",
        borderWidth: "1px",
        borderColor: connection === "closed" ? "rust.700" : "amber.600",
        fontSize: "xs",
        fontWeight: "semibold",
        color: connection === "closed" ? "rust.300" : "amber.300",
      })}
    >
      <span className={`conn-dot conn-dot--${connection}`} aria-hidden />
      {connection === "closed" ? "Not connected" : "Reconnecting…"}
    </span>
  );
}
