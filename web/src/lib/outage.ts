import { toaster } from "./toaster";

/**
 * Says once that the server is answering badly, while the app keeps running on
 * what's already on disk.
 *
 * A 5xx used to be loud by accident: it broke the shelf and the reader, which
 * told the reader something was wrong by failing in front of them. Falling back
 * to disk fixed that and took the telling with it — downloads keep working and
 * nothing says why the rest is stale. This is the replacement.
 *
 * Only a real answer counts. A dead network raises no toast: the OfflineIndicator
 * pill already owns that case, and it says "Offline", which is the truth this
 * cannot tell from a 502 without duplicating the pill's job badly.
 */

const TOAST_ID = "server-outage";

// Two flags, because they answer different questions. `reported` is the once-a-
// session latch and never resets: a server flapping between 500 and 200 would
// otherwise re-toast on every swing, which is the spam this exists to avoid.
// `visible` only tracks whether there is a toast up to take down.
let reported = false;
let visible = false;

export function noteOutage(): void {
  if (reported) return;
  reported = true;
  visible = true;

  toaster.create({
    id: TOAST_ID,
    type: "error",
    title: "Dowitcher's server is having trouble",
    description: "Downloaded comics still work. Anything else may be out of date until it recovers.",
    // No duration, for the reason the update notice has none: ToasterView lives
    // in AppShell and the reader is fullscreen without one, so a toast raised
    // mid-comic renders only once the reader leaves. An expiring one would tick
    // away behind the page it was meant to explain.
    duration: Infinity,
  });
}

/** The server answered, so retire the notice rather than leave it lying. */
export function noteReachable(): void {
  if (!visible) return;
  visible = false;
  toaster.dismiss(TOAST_ID);
}
