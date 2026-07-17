import { useSyncExternalStore } from "react";
import { CloudOff } from "lucide-react";
import { hstack } from "styled-system/patterns";

function subscribe(onChange: () => void) {
  window.addEventListener("online", onChange);
  window.addEventListener("offline", onChange);
  return () => {
    window.removeEventListener("online", onChange);
    window.removeEventListener("offline", onChange);
  };
}

/** navigator.onLine is only honest about "no network at all" — a captive portal or a
 *  dead server still reads as online. That is why this says "Offline" and not
 *  "Can't reach Longbox": the stream's connection light already covers the
 *  second case, and overclaiming here would make both untrustworthy. */
export function useOnline(): boolean {
  return useSyncExternalStore(
    subscribe,
    () => navigator.onLine,
    () => true,
  );
}

/**
 * A pill that shows up only when the device has no network, so that a page that
 * won't turn or a progress write that didn't land has a visible reason. Sits
 * above the mobile tab bar, out of the way of everything else.
 */
export function OfflineIndicator() {
  const online = useOnline();
  if (online) return null;

  return (
    <div
      role="status"
      aria-live="polite"
      className={hstack({
        position: "fixed",
        bottom: { base: "20", md: "4" },
        left: "50%",
        transform: "translateX(-50%)",
        zIndex: "50",
        gap: "2",
        px: "3",
        py: "2",
        borderRadius: "full",
        bg: "surfaceRaised",
        borderWidth: "1px",
        borderColor: "amber.600",
        color: "amber.300",
        fontSize: "xs",
        fontWeight: "semibold",
        boxShadow: "pop",
        pointerEvents: "none",
      })}
    >
      <CloudOff size={14} aria-hidden />
      Offline — downloaded comics only
    </div>
  );
}
