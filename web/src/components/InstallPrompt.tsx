import { useEffect, useState, useSyncExternalStore, type ReactNode } from "react";
import { Download, Share, X } from "lucide-react";
import { css } from "styled-system/css";
import { hstack, vstack } from "styled-system/patterns";
import { requestPersistentStorage } from "../offline/persist";
import { router } from "../router";
import { Button } from "./Button";

/** Chromium-only, and absent from the DOM lib because no other engine fires it. */
interface BeforeInstallPromptEvent extends Event {
  prompt: () => Promise<void>;
  userChoice: Promise<{ outcome: "accepted" | "dismissed" }>;
}

declare global {
  interface Window {
    // Stashed by the inline listener in index.html, which is early enough to
    // catch the event; see the comment there.
    __longboxInstallPrompt?: BeforeInstallPromptEvent;
  }
}

const DISMISSED_KEY = "longbox.install-prompt.dismissed";

function isStandalone(): boolean {
  return (
    window.matchMedia("(display-mode: standalone)").matches ||
    // Safari's own flag, still the only signal for an iOS Home Screen launch.
    (navigator as { standalone?: boolean }).standalone === true
  );
}

function isIOS(): boolean {
  // An iPad on iPadOS 13+ claims to be a Mac; touch points give it away.
  return (
    /iphone|ipad|ipod/i.test(navigator.userAgent) ||
    (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1)
  );
}

/** The reader is a comic and nothing else. Nagging over a page is what makes people
 *  dismiss the thing forever. */
function useOnReader(): boolean {
  const pathname = useSyncExternalStore(
    (onChange) => router.subscribe("onResolved", onChange),
    () => router.state.location.pathname,
    () => "/",
  );
  return pathname.startsWith("/comic/");
}

function Card({ children, onDismiss }: { children: ReactNode; onDismiss: () => void }) {
  return (
    <div
      className={hstack({
        position: "fixed",
        left: { base: "4", md: "4" },
        right: { base: "4", md: "auto" },
        bottom: { base: "20", md: "4" },
        zIndex: "40",
        maxW: "sm",
        gap: "3",
        alignItems: "flex-start",
        p: "4",
        borderRadius: "lg",
        bg: "surfaceRaised",
        borderWidth: "1px",
        borderColor: "border",
        borderLeftWidth: "3px",
        borderLeftColor: "accent",
        boxShadow: "pop",
      })}
    >
      {children}
      <button
        onClick={onDismiss}
        aria-label="Dismiss"
        title="Dismiss"
        className={css({
          color: "textMuted",
          cursor: "pointer",
          borderRadius: "sm",
          p: "0.5",
          flexShrink: 0,
          _hover: { color: "text", bg: "ink.750" },
        })}
      >
        <X size={16} />
      </button>
    </div>
  );
}

/**
 * Offers installation the way the platform actually allows it: a button where
 * one can be wired up, instructions where it has to be done by hand, and an
 * explanation where the browser has taken the option away entirely.
 */
export function InstallPrompt() {
  const [installEvent, setInstallEvent] = useState<BeforeInstallPromptEvent | null>(
    () => window.__longboxInstallPrompt ?? null,
  );
  const [installed, setInstalled] = useState(isStandalone);
  const [dismissed, setDismissed] = useState(() => localStorage.getItem(DISMISSED_KEY) === "1");
  const onReader = useOnReader();

  useEffect(() => {
    // Covers the event arriving after this mounted; the inline listener in
    // index.html covers it arriving before. Between them the card cannot miss it.
    function onInstallable() {
      setInstallEvent(window.__longboxInstallPrompt ?? null);
    }
    function onInstalled() {
      setInstalled(true);
      setInstallEvent(null);
      delete window.__longboxInstallPrompt;
      // Installation is the strongest hint a browser weighs when deciding
      // whether to make our storage persistent, so this is the moment to ask.
      void requestPersistentStorage();
    }

    window.addEventListener("longbox:installable", onInstallable);
    window.addEventListener("appinstalled", onInstalled);
    return () => {
      window.removeEventListener("longbox:installable", onInstallable);
      window.removeEventListener("appinstalled", onInstalled);
    };
  }, []);

  function dismiss() {
    localStorage.setItem(DISMISSED_KEY, "1");
    setDismissed(true);
  }

  async function install() {
    if (!installEvent) return;
    // The event is spent once prompted, whatever the user picks; a second
    // prompt() on it throws, so it goes from both places that hold it.
    setInstallEvent(null);
    delete window.__longboxInstallPrompt;
    await installEvent.prompt();
    const { outcome } = await installEvent.userChoice;
    if (outcome === "accepted") setInstalled(true);
  }

  if (installed || dismissed || onReader) return null;

  // Service workers need a secure context, and plenty of people run a
  // self-hosted server on a LAN over plain http. Nothing offline can work
  // there, and silently missing features are worse than a stated reason.
  if (!window.isSecureContext) {
    return (
      <Card onDismiss={dismiss}>
        <div className={vstack({ gap: "1", alignItems: "stretch", flex: "1", minW: "0" })}>
          <span className={css({ fontWeight: "bold", fontSize: "sm", color: "text" })}>
            Offline reading is off
          </span>
          <span className={css({ fontSize: "sm", color: "textMuted" })}>
            Browsers only allow installing and downloading over HTTPS. Longbox is being served over
            http://{window.location.host}, so it stays online-only.
          </span>
        </div>
      </Card>
    );
  }

  if (isIOS()) {
    return (
      <Card onDismiss={dismiss}>
        <Share size={19} className={css({ flexShrink: 0, color: "accent", mt: "0.5" })} aria-hidden />
        <div className={vstack({ gap: "1", alignItems: "stretch", flex: "1", minW: "0" })}>
          <span className={css({ fontWeight: "bold", fontSize: "sm", color: "text" })}>
            Add Longbox to your Home Screen
          </span>
          {/* Not a nicety on iOS: Safari erases caches and service workers after
              seven days without a visit, and a Home Screen app is the documented
              way out. Skip this and downloaded comics quietly disappear. */}
          <span className={css({ fontSize: "sm", color: "textMuted" })}>
            Tap Share, then <strong>Add to Home Screen</strong>. Safari deletes downloaded comics
            after a week unless Longbox lives on your Home Screen.
          </span>
        </div>
      </Card>
    );
  }

  if (!installEvent) return null;

  return (
    <Card onDismiss={dismiss}>
      <div className={vstack({ gap: "2.5", alignItems: "flex-start", flex: "1", minW: "0" })}>
        <div className={vstack({ gap: "1", alignItems: "stretch" })}>
          <span className={css({ fontWeight: "bold", fontSize: "sm", color: "text" })}>
            Install Longbox
          </span>
          <span className={css({ fontSize: "sm", color: "textMuted" })}>
            Opens in its own window and keeps downloaded comics around for reading offline.
          </span>
        </div>
        <Button variant="primary" icon={<Download size={16} />} onClick={install}>
          Install
        </Button>
      </div>
    </Card>
  );
}
