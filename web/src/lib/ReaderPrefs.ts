import { useCallback, useEffect, useRef, useState } from "react";

/**
 * How the page is scaled to the viewport. Reading posture, not a property of
 * the comic: the same book wants fit-height on a laptop and fit-width on a
 * phone, so these live in localStorage and never go to the server.
 */
export type FitMode = "height" | "width" | "original";

const FIT_KEY = "dowitcher.reader.fit";
const SPREAD_KEY = "dowitcher.reader.spread";

/**
 * Reading direction is a property of the book, not the device — a manga reads
 * right-to-left on every screen you own — but it is still stored locally.
 * Progress is the only thing worth syncing; a per-comic server field for this
 * would be a migration and an endpoint to let someone re-tick a checkbox they
 * tick once per series.
 */
const rtlKey = (comicId: string) => `dowitcher.reader.rtl.${comicId}`;

// Private-mode Safari throws on localStorage access rather than no-opping, and
// a reader that won't open because it couldn't remember a zoom setting is a
// worse bug than forgetting the zoom setting.
function readStored(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function writeStored(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* storage unavailable; the preference lasts the session */
  }
}

function isFitMode(v: string | null): v is FitMode {
  return v === "height" || v === "width" || v === "original";
}

export function useFitMode(): [FitMode, (mode: FitMode) => void] {
  const [fit, setFit] = useState<FitMode>(() => {
    const stored = readStored(FIT_KEY);
    return isFitMode(stored) ? stored : "height";
  });

  const update = useCallback((mode: FitMode) => {
    setFit(mode);
    writeStored(FIT_KEY, mode);
  }, []);

  return [fit, update];
}

export function useSpread(): [boolean, (on: boolean) => void] {
  const [spread, setSpread] = useState(() => readStored(SPREAD_KEY) === "1");

  const update = useCallback((on: boolean) => {
    setSpread(on);
    writeStored(SPREAD_KEY, on ? "1" : "0");
  }, []);

  return [spread, update];
}

export function useRtl(comicId: string): [boolean, (on: boolean) => void] {
  const [rtl, setRtl] = useState(() => readStored(rtlKey(comicId)) === "1");

  // The router keeps this component mounted across /comic/a -> /comic/b, so the
  // initialiser above runs once for the wrong comic. Re-read on every id change.
  const loadedFor = useRef(comicId);
  useEffect(() => {
    if (loadedFor.current === comicId) return;
    loadedFor.current = comicId;
    setRtl(readStored(rtlKey(comicId)) === "1");
  }, [comicId]);

  const update = useCallback(
    (on: boolean) => {
      setRtl(on);
      writeStored(rtlKey(comicId), on ? "1" : "0");
    },
    [comicId],
  );

  return [rtl, update];
}
