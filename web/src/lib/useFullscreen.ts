import { useCallback, useEffect, useState } from "react";

/**
 * A PWA launched from the Home Screen already fills the screen, so the browser
 * Fullscreen API is only worth offering inside a normal tab. Same signals as
 * the install prompt: the media query covers everything except an iOS Home
 * Screen launch, which only sets `navigator.standalone`.
 */
function isStandalone(): boolean {
  return (
    window.matchMedia("(display-mode: standalone)").matches ||
    (navigator as { standalone?: boolean }).standalone === true
  );
}

/**
 * Wraps the Fullscreen API for the reader. Returns `null` for `active` when
 * fullscreen isn't worth offering — a standalone PWA (already full-screen) or a
 * browser without the API, notably iOS Safari, which never exposes it on the
 * document element. The caller hides the control in that case rather than
 * showing a button that does nothing.
 */
export function useFullscreen(): {
  active: boolean | null;
  toggle: () => void;
} {
  const supported =
    !isStandalone() && typeof document !== "undefined" && document.fullscreenEnabled;

  const [active, setActive] = useState(() => Boolean(document.fullscreenElement));

  useEffect(() => {
    if (!supported) return;
    const onChange = () => setActive(Boolean(document.fullscreenElement));
    document.addEventListener("fullscreenchange", onChange);
    return () => document.removeEventListener("fullscreenchange", onChange);
  }, [supported]);

  const toggle = useCallback(() => {
    if (document.fullscreenElement) {
      void document.exitFullscreen().catch(() => {});
    } else {
      // The whole document, not the reader element: a stray descendant request
      // would trap the browser's own escape hatch behind our chrome.
      void document.documentElement.requestFullscreen().catch(() => {});
    }
  }, []);

  return { active: supported ? active : null, toggle };
}
