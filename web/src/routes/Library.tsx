import { useEffect, useMemo, useState } from "react";
import { Library as LibraryIcon, Search, Tag as TagIcon, Upload, X } from "lucide-react";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { css } from "styled-system/css";
import { hstack, vstack } from "styled-system/patterns";
import { Button } from "../components/Button";
import { ClaimButton } from "../components/ClaimButton";
import { ComicGrid, ComicGridSkeleton, ComicTile, TileButton } from "../components/ComicGrid";
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";
import { TagEditorDialog } from "../components/TagEditorDialog";
import { useAuth } from "../auth/AuthProvider";
import { useLiveData } from "../live/LiveData";
import { wsClient } from "../api/ws";
import { HttpError, isUnanswered } from "../api/http";
import { fetchComics } from "../api/comics";
import { cacheLibraryPage, readLibraryPage } from "../offline/metaCache";
import { comicLabel } from "../lib/format";
import type { Comic, Progress } from "../api/generated";

type Sort = "added" | "title" | "series";

const SORTS: { value: Sort; label: string }[] = [
  { value: "added", label: "Recently added" },
  { value: "title", label: "Title" },
  { value: "series", label: "Series" },
];

/** Sorting runs over what has been loaded, so it never waits on a round trip. */
function sortComics(comics: Comic[], sort: Sort): Comic[] {
  const next = [...comics];
  if (sort === "title") {
    next.sort((a, b) => comicLabel(a).localeCompare(comicLabel(b)));
  } else if (sort === "series") {
    next.sort(
      (a, b) =>
        // Untitled series sort last rather than first: a loose one-shot is not
        // the thing anyone opened this list to find.
        (a.series ?? "￿").localeCompare(b.series ?? "￿") ||
        // Issue numbers are strings because "12AU" and "0" both exist, but they
        // are numbers often enough that text order putting #10 before #2 reads
        // as broken.
        (Number(a.number) || 0) - (Number(b.number) || 0) ||
        a.title.localeCompare(b.title),
    );
  } else {
    next.sort((a, b) => b.addedAt - a.addedAt);
  }
  return next;
}

/**
 * Which comics an admin may claim or hand back. An upload is neither: it has an
 * owner already. A claim someone else made is not offered either — the server
 * would allow an admin to undo it, but a button to take over a colleague's
 * shelf is not something to put a hover away from a misclick.
 */
function claimable(comic: Comic, isAdmin: boolean): boolean {
  if (!isAdmin) return false;
  return comic.source === "library" || (comic.source === "claimed" && comic.ownedByMe);
}

export function LibraryPage() {
  const { tag, q } = useSearch({ from: "/" });
  const navigate = useNavigate({ from: "/" });
  const queryClient = useQueryClient();
  const { library } = useLiveData();
  const { user } = useAuth();

  const [draft, setDraft] = useState(q ?? "");
  const [sort, setSort] = useState<Sort>("added");
  const [editing, setEditing] = useState<Comic | null>(null);

  // Typing shouldn't fire a request per keystroke, and replace: true keeps it
  // from stacking a history entry per keystroke either.
  useEffect(() => {
    const id = setTimeout(() => {
      const next = draft.trim() || undefined;
      if (next !== q) navigate({ search: (prev) => ({ ...prev, q: next }), replace: true });
    }, 250);
    return () => clearTimeout(id);
  }, [draft, q, navigate]);

  // The watcher announces a changed shelf over the stream. Refetching on that
  // signal is what fills the grid in front of you as a scan runs, without the
  // page asking the server every few seconds whether anything happened yet.
  useEffect(
    () => wsClient.subscribe("comics", () => {
      queryClient.invalidateQueries({ queryKey: ["comics"] });
    }),
    [queryClient],
  );

  const filterKey = `${q ?? ""}|${tag ?? ""}`;

  const comicsQuery = useInfiniteQuery({
    queryKey: ["comics", { q: q ?? "", tag: tag ?? "" }],
    // Read through the offline copy of the shelf. Only the first page is kept:
    // offline there is nothing behind "Show more" to fetch anyway, and caching
    // every scroll depth of every search anyone ever typed would grow without
    // bound for a list they can't page through.
    queryFn: async ({ pageParam }) => {
      try {
        const page = await fetchComics({ q, tag }, pageParam);
        if (pageParam === 0) void cacheLibraryPage(filterKey, page);
        return page;
      } catch (err) {
        if (!isUnanswered(err) || pageParam !== 0) throw err;
        const cached = await readLibraryPage(filterKey);
        // Returning cached data resolves the query, so it never enters the
        // retry backoff — offline, the shelf is on screen immediately rather
        // than after three doomed attempts.
        if (cached) return cached;
        throw err;
      }
    },
    initialPageParam: 0,
    getNextPageParam: (last, all) => {
      const loaded = all.reduce((n, p) => n + p.comics.length, 0);
      return loaded < last.total ? loaded : undefined;
    },
  });

  const comics = useMemo(
    () => sortComics(comicsQuery.data?.pages.flatMap((p) => p.comics) ?? [], sort),
    [comicsQuery.data, sort],
  );

  const progress = useMemo(() => {
    const merged = new Map<string, Progress>();
    for (const page of comicsQuery.data?.pages ?? []) {
      for (const [id, p] of page.progress) merged.set(id, p);
    }
    return merged;
  }, [comicsQuery.data]);

  const filtered = Boolean(q || tag);

  return (
    <div className={vstack({ gap: "7", alignItems: "stretch" })}>
      <PageHeader
        eyebrow="Shelf"
        title="Library"
        subtitle={
          library?.scanning
            ? "Still reading your shelves — new comics will appear as they turn up."
            : "Everything on this server that you can read."
        }
      />

      <div className={hstack({ gap: "3", flexWrap: "wrap" })}>
        <label
          className={hstack({
            gap: "2.5",
            flex: "1",
            minW: "56",
            px: "3.5",
            py: "2.5",
            borderRadius: "md",
            bg: "surface",
            borderWidth: "1px",
            borderColor: "border",
            _focusWithin: { borderColor: "accent" },
          })}
        >
          <Search size={16} className={css({ color: "ink.500", flexShrink: 0 })} />
          <input
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            placeholder="Search by title or series"
            aria-label="Search the library"
            className={css({
              flex: "1",
              minW: "0",
              bg: "transparent",
              color: "text",
              fontSize: "sm",
              _placeholder: { color: "ink.500" },
              _focus: { outline: "none" },
            })}
          />
          {draft && (
            <button
              onClick={() => setDraft("")}
              aria-label="Clear the search"
              title="Clear the search"
              className={css({
                display: "flex",
                color: "ink.500",
                cursor: "pointer",
                _hover: { color: "text" },
              })}
            >
              <X size={15} />
            </button>
          )}
        </label>

        <div
          className={hstack({
            gap: "1",
            p: "1",
            borderRadius: "md",
            bg: "surface",
            borderWidth: "1px",
            borderColor: "border",
          })}
        >
          {SORTS.map((s) => (
            <button
              key={s.value}
              onClick={() => setSort(s.value)}
              className={css({
                px: "3",
                py: "1.5",
                borderRadius: "sm",
                fontSize: "xs",
                fontWeight: "semibold",
                cursor: "pointer",
                color: sort === s.value ? "text" : "textMuted",
                bg: sort === s.value ? "surfaceRaised" : "transparent",
                _hover: { color: "text" },
              })}
            >
              {s.label}
            </button>
          ))}
        </div>
      </div>

      {tag && (
        <div className={hstack({ gap: "2.5", flexWrap: "wrap" })}>
          <span className={css({ fontSize: "sm", color: "textMuted" })}>Showing only</span>
          <span
            className={hstack({
              gap: "1.5",
              pl: "3",
              pr: "1.5",
              py: "1",
              borderRadius: "full",
              bg: "accentQuiet",
              borderWidth: "1px",
              borderColor: "magenta.900",
              color: "magenta.300",
              fontSize: "xs",
              fontWeight: "bold",
            })}
          >
            <TagIcon size={12} />
            {tag}
            <button
              onClick={() => navigate({ search: (prev) => ({ ...prev, tag: undefined }) })}
              aria-label={`Stop filtering by ${tag}`}
              title={`Stop filtering by ${tag}`}
              className={css({
                display: "flex",
                p: "0.5",
                borderRadius: "full",
                color: "magenta.300",
                cursor: "pointer",
                _hover: { bg: "magenta.900", color: "text" },
              })}
            >
              <X size={12} />
            </button>
          </span>
        </div>
      )}

      {comicsQuery.isLoading ? (
        <ComicGridSkeleton />
      ) : comicsQuery.isError ? (
        <EmptyState
          icon={LibraryIcon}
          title="Couldn't reach your library"
          action={
            <Button variant="primary" onClick={() => comicsQuery.refetch()}>
              Try again
            </Button>
          }
        >
          {comicsQuery.error instanceof HttpError
            ? comicsQuery.error.message
            : "The server didn't answer. It may still be starting up."}
        </EmptyState>
      ) : comics.length === 0 ? (
        filtered ? (
          <EmptyState
            icon={Search}
            title={q ? `Nothing matches “${q}”` : `Nothing tagged “${tag}”`}
            action={
              <Button
                onClick={() => {
                  setDraft("");
                  navigate({ search: {} });
                }}
              >
                Clear the filters
              </Button>
            }
          >
            Try a shorter search, or check the spelling of the series name.
          </EmptyState>
        ) : (
          <EmptyState
            icon={LibraryIcon}
            title="Your shelf is empty"
            action={
              <Link
                to="/import"
                className={hstack({
                  gap: "2",
                  px: "4",
                  py: "2.5",
                  borderRadius: "md",
                  bg: "accent",
                  color: "white",
                  fontSize: "sm",
                  fontWeight: "bold",
                  textDecoration: "none",
                  _hover: { bg: "accentHover" },
                })}
              >
                <Upload size={16} />
                Import comics
              </Link>
            }
          >
            Drop CBZ files into{" "}
            <code className={css({ fontFamily: "mono", color: "ink.200" })}>
              {library?.root ?? "your library folder"}
            </code>{" "}
            and they'll show up here on their own. Or bring in a folder of images
            and Dowitcher will package it for you.
          </EmptyState>
        )
      ) : (
        <>
          <ComicGrid>
            {comics.map((comic) => (
              <ComicTile
                key={comic.id}
                comic={comic}
                progress={progress.get(comic.id)}
                actions={
                  <>
                    {claimable(comic, user?.isAdmin ?? false) && <ClaimButton comic={comic} />}
                    <TileButton
                      label={`Edit tags on ${comicLabel(comic)}`}
                      onClick={() => setEditing(comic)}
                    >
                      <TagIcon size={14} />
                    </TileButton>
                  </>
                }
              />
            ))}
          </ComicGrid>

          {comicsQuery.hasNextPage && (
            <Button
              onClick={() => comicsQuery.fetchNextPage()}
              busy={comicsQuery.isFetchingNextPage}
              className={css({ alignSelf: "center" })}
            >
              Show more
            </Button>
          )}
        </>
      )}

      <TagEditorDialog
        comic={editing}
        open={editing !== null}
        onOpenChange={(open) => {
          if (!open) setEditing(null);
        }}
      />
    </div>
  );
}
