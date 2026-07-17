import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { BookX, ChevronLeft, ChevronRight, Loader2 } from "lucide-react";
import { css, cx } from "styled-system/css";
import { flex, vstack } from "styled-system/patterns";
import { DownloadButton } from "../components/DownloadButton";
import { ReaderPageImage, pageSrc } from "../components/ReaderPageImage";
import { ReaderResumeBanner } from "../components/ReaderResumeBanner";
import { ReaderScrubber } from "../components/ReaderScrubber";
import { ReaderShortcutsDialog } from "../components/ReaderShortcutsDialog";
import { ReaderToolbar } from "../components/ReaderToolbar";
import { http, isUnanswered } from "../api/http";
import { cacheComicDetail, readComicDetail } from "../offline/metaCache";
import { enqueueProgress, saveProgress as putProgress } from "../offline/progressQueue";
import { buildSpreads, spreadIndexOf } from "../lib/ReaderLayout";
import { useFitMode, useRtl, useSpread } from "../lib/ReaderPrefs";
import { useFullscreen } from "../lib/useFullscreen";
import { comicLabel } from "../lib/format";
import type { FitMode } from "../lib/ReaderPrefs";
import type { ComicDetail, Progress, ProgressRequest } from "../api/generated";

/** Long enough that a flip-through collapses to one write, short enough that
 *  picking up your phone mid-page still saves. */
const PROGRESS_DEBOUNCE_MS = 1200;
const CHROME_IDLE_MS = 2500;
// A tap asks for the controls on purpose, so hold them long enough to read and
// use; the pointer's idle-hide is far too quick for a deliberate summon.
const CHROME_TAP_MS = 10000;
// A touchscreen fires a synthetic mousemove after a tap or swipe. Ignore the
// mousemove path for this long afterward so a page-turn gesture doesn't pop the
// chrome it meant to leave down.
const TOUCH_MOUSE_GRACE_MS = 500;
const SWIPE_MIN_PX = 48;
// Closing the comic is the one gesture that throws away what you were doing, so
// it is deliberately hard to trigger by accident: a drag two and a half times
// the page-turn threshold, running almost straight down. A flick meant for a
// page turn drifts nowhere near this.
const SWIPE_CLOSE_MIN_PX = 120;
const SWIPE_CLOSE_RATIO = 3;

const FIT_CYCLE: FitMode[] = ["height", "width", "original"];

// Pre-built class names — Panda extracts at build time and cannot see a style
// object reached through a variable, so each variant is a finished class.
const SURFACE_FIT = {
  // Nothing to scroll: the page is already inside the viewport.
  height: css({ minH: "100dvh", overflow: "hidden" }),
  // The document scrolls, not an inner box. Fixed chrome sits above it and the
  // wheel/touch still reaches the viewport underneath, which an inner
  // overflow container would swallow.
  width: css({ minH: "100dvh" }),
  original: css({ minH: "100dvh", w: "max-content", minW: "100%" }),
};

const SPREAD_DIR = {
  ltr: css({ flexDirection: "row" }),
  // The first page of the pair belongs on the right when reading right-to-left.
  rtl: css({ flexDirection: "row-reverse" }),
};

export function ReaderPage({ id }: { id: string }) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();

  const { data: detail, isLoading } = useQuery({
    queryKey: ["comic", id],
    // Read through to the offline copy. A downloaded comic has every page on
    // disk, and the only thing standing between the reader and them is this
    // request — so when it can't be made, the cached page list stands in and
    // the query *succeeds*. Rethrowing instead would spend the retry backoff
    // before showing a comic that was never actually unavailable.
    queryFn: async () => {
      try {
        const fresh = await http.get<ComicDetail>(`/api/comics/${id}`);
        void cacheComicDetail(fresh);
        return fresh;
      } catch (err) {
        if (!isUnanswered(err)) throw err;
        const cached = await readComicDetail(id);
        if (cached) return cached;
        throw err;
      }
    },
    retry: (count, err) => isUnanswered(err) && count < 2,
    // The page list and the file behind it don't change while you read. Refetching
    // on tab focus would only ever re-race our own progress writes.
    staleTime: Infinity,
    refetchOnWindowFocus: false,
  });

  const [fit, setFit] = useFitMode();
  const [spread, setSpread] = useSpread();
  const [rtl, setRtl] = useRtl(id);
  const { active: fullscreen, toggle: toggleFullscreen } = useFullscreen();

  const [page, setPage] = useState(0);
  const [chromeVisible, setChromeVisible] = useState(true);
  // The left/right arrows label the pointer tap-zones, so they only make sense
  // when a pointer summoned the chrome. Turning pages from the keyboard reveals
  // the toolbar and scrubber but leaves these off.
  const [arrowsVisible, setArrowsVisible] = useState(true);
  const [chromePinned, setChromePinned] = useState(false);
  const [activity, setActivity] = useState(0);
  const [resumeOffer, setResumeOffer] = useState<number | null>(null);
  const [shortcutsOpen, setShortcutsOpen] = useState(false);
  const [measuredLandscape, setMeasuredLandscape] = useState<ReadonlySet<number>>(
    () => new Set(),
  );

  const pages = useMemo(() => detail?.pages ?? [], [detail]);
  const pageCount = pages.length;

  const isLandscape = useCallback(
    (index: number) => {
      const p = pages[index];
      // Dimensions are optional in the API, absent whenever the server could
      // not read an image header. Assume portrait — overwhelmingly the common
      // case — and let the measurement below correct the pairing once the real
      // bytes land, rather than guessing landscape and splitting the book.
      if (p?.width && p?.height) return p.width > p.height;
      return measuredLandscape.has(index);
    },
    [pages, measuredLandscape],
  );

  const spreads = useMemo(
    () => buildSpreads(pageCount, isLandscape, spread),
    [pageCount, isLandscape, spread],
  );
  const spreadIndex = spreadIndexOf(spreads, page);
  const visiblePages = spreads[spreadIndex] ?? [];

  const onNaturalSize = useCallback(
    (index: number, width: number, height: number) => {
      if (width <= height) return;
      setMeasuredLandscape((prev) => {
        if (prev.has(index)) return prev;
        const next = new Set(prev);
        next.add(index);
        return next;
      });
    },
    [],
  );

  // --- progress -----------------------------------------------------------

  const saveProgress = useMutation({
    mutationFn: (body: ProgressRequest) => putProgress(id, body),
    onSuccess: (progress, body) => {
      // Patch the cache in place. Invalidating would refetch ComicDetail on every
      // page turn, and the arriving payload carries a `progress` a second or two
      // behind where the reader now is — the reader would spend the whole book
      // fighting its own writes.
      queryClient.setQueryData<ComicDetail>(["comic", id], (prev) => {
        if (!prev) return prev;
        // A null answer means the write was queued for reconnect. Take our own
        // claim for now: it is what the queue will replay, and the reader is
        // looking at the page it describes.
        const next: Progress = progress ?? {
          comicId: id,
          page: body.page,
          pageCount: prev.pages.length,
          completed: body.completed,
          updatedAt: body.updatedAt ?? Math.floor(Date.now() / 1000),
        };
        return { ...prev, progress: next };
      });
    },
  });

  const pendingRef = useRef<ProgressRequest | null>(null);
  const timerRef = useRef<number | null>(null);
  const mutateRef = useRef(saveProgress.mutate);
  mutateRef.current = saveProgress.mutate;

  const flushProgress = useCallback(
    (leaving = false) => {
      if (timerRef.current !== null) {
        window.clearTimeout(timerRef.current);
        timerRef.current = null;
      }
      const body = pendingRef.current;
      if (!body) return;
      pendingRef.current = null;
      if (leaving) {
        // The tab is being torn down; a normal fetch dies with it and the last
        // few pages of a session are exactly the ones worth keeping. keepalive
        // hands the request to the browser to finish without us.
        http
          .put(`/api/comics/${id}/progress`, { ...body }, { keepalive: true })
          // Offline the request fails at once, while the tab is still alive
          // enough to write to disk. Best-effort by nature — if the page is
          // already gone the enqueue goes with it — but it costs nothing and
          // rescues the common case of closing a comic on a train.
          .catch(() => {
            void enqueueProgress(id, body, body.updatedAt ?? Math.floor(Date.now() / 1000));
          });
        return;
      }
      mutateRef.current(body);
    },
    [id],
  );

  const queueProgress = useCallback(
    (body: ProgressRequest) => {
      pendingRef.current = body;
      if (timerRef.current !== null) window.clearTimeout(timerRef.current);
      timerRef.current = window.setTimeout(() => flushProgress(), PROGRESS_DEBOUNCE_MS);
    },
    [flushProgress],
  );

  useEffect(() => {
    const onVisibility = () => {
      if (document.visibilityState === "hidden") flushProgress(true);
    };
    const onPageHide = () => flushProgress(true);
    document.addEventListener("visibilitychange", onVisibility);
    window.addEventListener("pagehide", onPageHide);
    return () => {
      document.removeEventListener("visibilitychange", onVisibility);
      window.removeEventListener("pagehide", onPageHide);
      flushProgress(true);
    };
  }, [flushProgress]);

  // --- navigation ---------------------------------------------------------

  const jump = useCallback(
    (target: number) => {
      if (pageCount === 0) return;
      const next = Math.min(Math.max(target, 0), pageCount - 1);
      setPage(next);
      // Reaching the last *spread* finishes the book — in two-page mode the final
      // turn can land on pageCount-2 and never touch the last index.
      const lastStart = spreads[spreads.length - 1]?.[0] ?? pageCount - 1;
      // Stamped here, where the page was turned, and not where the write goes
      // out. The debounce alone puts a second between the two, and a queued
      // write can sit for hours — the server orders claims by this, so it has
      // to mean "when this was true".
      queueProgress({
        page: next,
        completed: next >= lastStart,
        updatedAt: Math.floor(Date.now() / 1000),
      });
    },
    [pageCount, spreads, queueProgress],
  );

  // Reading order: +1 is always "onward", whichever way the book runs.
  const turnReading = useCallback(
    (dir: 1 | -1) => {
      const target = spreads[spreadIndex + dir];
      if (!target) return;
      jump(target[0]!);
    },
    [spreads, spreadIndex, jump],
  );

  // Spatial input: +1 means "rightward". A right-to-left comic puts the next
  // page on the left, so the mapping flips here and nowhere else.
  const turnSpatial = useCallback(
    (dir: 1 | -1) => turnReading(rtl ? ((dir * -1) as 1 | -1) : dir),
    [turnReading, rtl],
  );

  // How long the current reveal waits before hiding. A hover keeps refreshing a
  // short window; a tap sets a long one. Read inside the idle effect, which
  // re-runs on every reveal via the activity bump.
  const hideDelayRef = useRef(CHROME_IDLE_MS);

  const showChrome = useCallback((withArrows = true, hideAfter = CHROME_IDLE_MS) => {
    hideDelayRef.current = hideAfter;
    setChromeVisible(true);
    setArrowsVisible(withArrows);
    setActivity((n) => n + 1);
  }, []);

  // Idle hands mean reading. Anything permanently on screen is something you
  // stop seeing but keep paying pixels for.
  useEffect(() => {
    if (!chromeVisible || chromePinned || resumeOffer !== null || shortcutsOpen) return;
    const timer = window.setTimeout(() => setChromeVisible(false), hideDelayRef.current);
    return () => window.clearTimeout(timer);
  }, [chromeVisible, chromePinned, resumeOffer, shortcutsOpen, activity]);

  const close = useCallback(() => {
    // The unmount cleanup flushes the pending progress write, so leaving here is
    // as safe as clicking the X in the toolbar.
    void navigate({ to: "/" });
  }, [navigate]);

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      // The scrubber owns the arrows while it has focus.
      const el = e.target as HTMLElement | null;
      if (el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.isContentEditable)) {
        return;
      }
      // A held modifier means the key belongs to the browser or the OS: Ctrl+F
      // is find, Cmd+D is bookmark. Only Shift+Space is ours, and it is handled
      // as its own case below.
      if (e.ctrlKey || e.metaKey || e.altKey) return;

      switch (e.key) {
        case "Escape":
          // The dialog closes itself on Escape and the resume banner is the
          // reader's own transient state — either one is what the reader means
          // to dismiss, so neither turn also closes the comic.
          if (shortcutsOpen) return;
          if (resumeOffer !== null) {
            setResumeOffer(null);
            break;
          }
          // In fullscreen, Escape is the browser's own way out of it — the same
          // press must not also close the comic out from under the reader. The
          // browser exits fullscreen; we do nothing and stay in the reader.
          if (fullscreen) return;
          close();
          return;
        case "ArrowRight":
        case "l":
          turnSpatial(1);
          break;
        case "ArrowLeft":
        case "h":
          turnSpatial(-1);
          break;
        case "PageDown":
        case "j":
          turnReading(1);
          break;
        case "PageUp":
        case "k":
          turnReading(-1);
          break;
        case " ":
          e.preventDefault();
          turnReading(e.shiftKey ? -1 : 1);
          break;
        case "Home":
          jump(0);
          break;
        case "End":
          jump(pageCount - 1);
          break;
        case "f":
          setFit(FIT_CYCLE[(FIT_CYCLE.indexOf(fit) + 1) % FIT_CYCLE.length]!);
          break;
        case "F":
          // Shift+f, paired with plain f for fit. No-op where fullscreen isn't
          // on offer (standalone PWA, iOS Safari) rather than a dead keypress.
          if (fullscreen === null) return;
          toggleFullscreen();
          break;
        case "s":
          setSpread(!spread);
          break;
        case "d":
          setRtl(!rtl);
          break;
        case "?":
          setShortcutsOpen(true);
          break;
        default:
          return;
      }
      // The chrome stays down for keyboard use: someone driving from the
      // keyboard can read the page count in the URL-free reader by tapping or
      // hovering, and the toolbar sliding in on every page turn is just flicker.
    }
    // Capture, so this runs before the dialog's own Escape handling rather than
    // after it. Ark closes on Escape from a listener below us, and React flushes
    // that state change synchronously — which tears down this effect and
    // re-subscribes a fresh closure *while the same event is still travelling*.
    // A bubble-phase listener would then be invoked with shortcutsOpen already
    // false and read the Escape a second time, dismissing whatever sits behind
    // the dialog. Nothing here depends on running last: the scrubber is fenced
    // off by the target check above, not by propagation order.
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [
    turnSpatial,
    turnReading,
    jump,
    pageCount,
    close,
    shortcutsOpen,
    resumeOffer,
    fit,
    setFit,
    spread,
    setSpread,
    rtl,
    setRtl,
    fullscreen,
    toggleFullscreen,
  ]);

  // A fit-width page is taller than the viewport, so a turn that kept the scroll
  // position would drop you into the middle of the new page.
  useEffect(() => {
    if (fit !== "height") window.scrollTo(0, 0);
  }, [page, fit]);

  // --- preloading ---------------------------------------------------------

  const preloadTargets = useMemo(() => {
    // Two spreads ahead is 2-4 pages in spread mode; three single pages ahead is
    // the same reach. One behind covers the back-up. Anything more and a 120-page
    // 1GB comic starts prefetching itself into the reader's disk cache.
    const ahead = spread ? 2 : 3;
    const wanted: number[] = [];
    for (let i = 1; i <= ahead; i++) wanted.push(...(spreads[spreadIndex + i] ?? []));
    wanted.push(...(spreads[spreadIndex - 1] ?? []));
    return wanted;
  }, [spreads, spreadIndex, spread]);

  const preloadRef = useRef(new Map<number, HTMLImageElement>());
  useEffect(() => {
    const cache = preloadRef.current;
    for (const [index, img] of cache) {
      if (preloadTargets.includes(index)) continue;
      // Dropping src aborts the in-flight decode. After a scrubber jump the old
      // prefetches are dead weight competing for bandwidth with the page the
      // reader is actually staring at.
      img.src = "";
      cache.delete(index);
    }
    for (const index of preloadTargets) {
      if (cache.has(index)) continue;
      const img = new Image();
      img.fetchPriority = "low";
      img.src = pageSrc(id, index);
      cache.set(index, img);
    }
  }, [id, preloadTargets]);

  useEffect(() => {
    const cache = preloadRef.current;
    return () => {
      for (const img of cache.values()) img.src = "";
      cache.clear();
    };
  }, []);

  // Offer the saved position exactly once per comic, and never for a finished
  // one — reopening a comic you completed means starting it again.
  const offeredFor = useRef<string | null>(null);
  useEffect(() => {
    if (!detail || offeredFor.current === id) return;
    offeredFor.current = id;
    setPage(0);
    const p = detail.progress;
    if (p && !p.completed && p.page > 0 && p.page < detail.pages.length) setResumeOffer(p.page);
  }, [detail, id]);

  // --- touch --------------------------------------------------------------

  const touchRef = useRef<{ x: number; y: number } | null>(null);
  const swipedRef = useRef(false);
  const lastTouchRef = useRef(0);

  const onTouchStart = (e: React.TouchEvent) => {
    lastTouchRef.current = Date.now();
    // Two fingers is a pinch-zoom; leave it entirely to the browser.
    if (e.touches.length !== 1) {
      touchRef.current = null;
      return;
    }
    const t = e.touches[0]!;
    touchRef.current = { x: t.clientX, y: t.clientY };
  };

  const onTouchEnd = (e: React.TouchEvent) => {
    lastTouchRef.current = Date.now();
    const start = touchRef.current;
    touchRef.current = null;
    if (!start || e.changedTouches.length !== 1) return;
    const t = e.changedTouches[0]!;
    const dx = t.clientX - start.x;
    const dy = t.clientY - start.y;

    // The tap zone underneath would otherwise act a second time on the same
    // finger.
    const consume = () => {
      swipedRef.current = true;
      window.setTimeout(() => {
        swipedRef.current = false;
      }, 0);
    };

    // Bias to vertical: on a fit-width page a scroll that drifts sideways must
    // not turn the page out from under the reader.
    if (Math.abs(dx) >= SWIPE_MIN_PX && Math.abs(dx) >= Math.abs(dy) * 1.5) {
      consume();
      // The page follows the finger: dragging left pulls in whatever sits to the
      // right, which is the next page only when the book runs left-to-right.
      turnSpatial(dx < 0 ? 1 : -1);
      return;
    }

    // Only fit-height has no scroll of its own, so it is the only mode where a
    // downward drag cannot mean "scroll the page" — in the other two this
    // gesture would fight the thing the reader was actually doing.
    if (fit !== "height") return;
    if (dy < SWIPE_CLOSE_MIN_PX || Math.abs(dx) * SWIPE_CLOSE_RATIO > dy) return;
    consume();
    close();
  };

  const guard = (fn: () => void) => () => {
    if (swipedRef.current) return;
    fn();
  };

  // --- render -------------------------------------------------------------

  if (isLoading) {
    return (
      <div className={flex({ align: "center", justify: "center", minH: "100dvh", bg: "reader" })}>
        <Loader2
          size={24}
          className={css({
            color: "ink.600",
            animation: "spin 0.9s linear infinite",
            _motionReduce: { animation: "none" },
          })}
        />
        <span className={css({ srOnly: true })}>Loading comic</span>
      </div>
    );
  }

  if (!detail) {
    return (
      <div
        className={vstack({
          gap: "5",
          alignItems: "center",
          justify: "center",
          minH: "100dvh",
          bg: "reader",
          p: "6",
          textAlign: "center",
        })}
      >
        <BookX size={34} className={css({ color: "ink.600" })} strokeWidth={1.5} />
        <div className={vstack({ gap: "1.5", maxW: "sm" })}>
          <h1 className={css({ fontSize: "lg", fontWeight: "bold" })}>This comic isn&apos;t here</h1>
          <p className={css({ color: "textMuted", fontSize: "sm", lineHeight: "1.6" })}>
            It may have been removed from the library, or the file has gone missing from disk.
          </p>
        </div>
        <Link
          to="/"
          className={css({
            px: "4",
            py: "2.5",
            borderRadius: "md",
            bg: "surfaceRaised",
            borderWidth: "1px",
            borderColor: "border",
            color: "text",
            fontSize: "sm",
            fontWeight: "semibold",
            textDecoration: "none",
            _hover: { bg: "ink.750" },
          })}
        >
          Back to the library
        </Link>
      </div>
    );
  }

  const atStart = spreadIndex === 0;
  const atEnd = spreadIndex >= spreads.length - 1;
  const canGoLeft = rtl ? !atEnd : !atStart;
  const canGoRight = rtl ? !atStart : !atEnd;

  return (
    <div
      onMouseMove={() => {
        if (Date.now() - lastTouchRef.current < TOUCH_MOUSE_GRACE_MS) return;
        showChrome();
      }}
      onTouchStart={onTouchStart}
      onTouchEnd={onTouchEnd}
      className={cx(
        css({
          position: "relative",
          bg: "reader",
          cursor: chromeVisible ? "default" : "none",
        }),
        SURFACE_FIT[fit],
      )}
    >
      <ReaderToolbar
        title={comicLabel(detail.comic)}
        page={page}
        pageCount={pageCount}
        fit={fit}
        onFit={setFit}
        spread={spread}
        onSpread={setSpread}
        rtl={rtl}
        onRtl={setRtl}
        fullscreen={fullscreen}
        onFullscreen={toggleFullscreen}
        visible={chromeVisible}
        onShortcuts={() => setShortcutsOpen(true)}
        download={<DownloadButton comicId={id} />}
      />

      <div
        className={cx(
          flex({ align: "center", justify: "center", minH: "100dvh", gap: "0" }),
          SPREAD_DIR[rtl ? "rtl" : "ltr"],
        )}
      >
        {visiblePages.map((index) => (
          <ReaderPageImage
            key={index}
            comicId={id}
            index={index}
            page={pages[index]}
            fit={fit}
            panes={visiblePages.length === 2 ? 2 : 1}
            onNaturalSize={onNaturalSize}
          />
        ))}
      </div>

      {/* Thirds: the outer two turn, the middle one summons the chrome. Aiming for
          a small control in the dark is the thing this replaces. */}
      <button
        onClick={guard(() => turnSpatial(-1))}
        disabled={!canGoLeft}
        aria-label={rtl ? "Next page" : "Previous page"}
        title={rtl ? "Next page" : "Previous page"}
        className={flex({
          position: "fixed",
          left: "0",
          top: "0",
          bottom: "0",
          w: "33%",
          align: "center",
          justify: "flex-start",
          px: "4",
          color: "ink.300",
          cursor: "pointer",
          opacity: chromeVisible && arrowsVisible && canGoLeft ? 0.7 : 0,
          transition: "opacity 0.25s ease",
          _motionReduce: { transition: "none" },
          _disabled: { cursor: "default" },
        })}
      >
        <ChevronLeft size={38} strokeWidth={1.5} />
      </button>

      <button
        onClick={guard(() => (chromeVisible ? setChromeVisible(false) : showChrome(true, CHROME_TAP_MS)))}
        aria-label={chromeVisible ? "Hide reader controls" : "Show reader controls"}
        title={chromeVisible ? "Hide reader controls" : "Show reader controls"}
        className={css({
          position: "fixed",
          left: "33%",
          right: "33%",
          top: "0",
          bottom: "0",
          cursor: "pointer",
        })}
      />

      <button
        onClick={guard(() => turnSpatial(1))}
        disabled={!canGoRight}
        aria-label={rtl ? "Previous page" : "Next page"}
        title={rtl ? "Previous page" : "Next page"}
        className={flex({
          position: "fixed",
          right: "0",
          top: "0",
          bottom: "0",
          w: "33%",
          align: "center",
          justify: "flex-end",
          px: "4",
          color: "ink.300",
          cursor: "pointer",
          opacity: chromeVisible && arrowsVisible && canGoRight ? 0.7 : 0,
          transition: "opacity 0.25s ease",
          _motionReduce: { transition: "none" },
          _disabled: { cursor: "default" },
        })}
      >
        <ChevronRight size={38} strokeWidth={1.5} />
      </button>

      {resumeOffer !== null && (
        <ReaderResumeBanner
          page={resumeOffer}
          onResume={() => {
            jump(resumeOffer);
            setResumeOffer(null);
          }}
          onDismiss={() => setResumeOffer(null)}
        />
      )}

      <ReaderScrubber
        page={page}
        pageCount={pageCount}
        onScrub={jump}
        onDraggingChange={setChromePinned}
        visible={chromeVisible}
      />

      <ReaderShortcutsDialog open={shortcutsOpen} onOpenChange={setShortcutsOpen} />
    </div>
  );
}
