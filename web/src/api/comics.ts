import { http } from "./http";
import type { Comic, Progress } from "./generated";

/** One page of the library grid. Big enough to fill a wide screen twice over. */
export const COMICS_PAGE_SIZE = 60;

export interface ComicFilters {
  q?: string;
  tag?: string;
  series?: string;
  collection?: string;
}

/** A page of comics with the caller's progress already paired up by comic id. */
export interface ComicPage {
  comics: Comic[];
  progress: Map<string, Progress>;
  /** Total matching the filter across all pages, or null when the server didn't say. */
  total: number | null;
}

/**
 * The list endpoint pairs each comic with the caller's progress, and there is no
 * generated type for that pairing yet — Comic has no progress field and
 * ComicDetail carries the page list a grid has no use for. Rather than pick one
 * of the plausible shapes and break silently on the others, this reads whichever
 * the server sends: a bare Comic[], {comics, progress?, total?}, or a list of
 * {comic, progress} pairs. Delete the branches that turn out to be dead once
 * tygo regenerates a real type for this response.
 */
function parseComicPage(raw: unknown): ComicPage {
  const progress = new Map<string, Progress>();
  const collect = (rows: Progress[] | undefined) => {
    for (const p of rows ?? []) progress.set(p.comicId, p);
  };

  if (Array.isArray(raw)) {
    const pairs = raw as Array<Comic | { comic: Comic; progress?: Progress }>;
    const comics: Comic[] = [];
    for (const row of pairs) {
      if (row && typeof row === "object" && "comic" in row) {
        comics.push(row.comic);
        if (row.progress) progress.set(row.comic.id, row.progress);
      } else {
        comics.push(row as Comic);
      }
    }
    return { comics, progress, total: null };
  }

  const body = (raw ?? {}) as {
    comics?: Array<Comic | { comic: Comic; progress?: Progress }>;
    progress?: Progress[];
    total?: number;
  };
  const nested = parseComicPage(body.comics ?? []);
  collect(body.progress);
  for (const [id, p] of nested.progress) progress.set(id, p);
  return {
    comics: nested.comics,
    progress,
    total: typeof body.total === "number" ? body.total : null,
  };
}

export function comicsPath(filters: ComicFilters, offset = 0, limit = COMICS_PAGE_SIZE): string {
  const params = new URLSearchParams();
  if (filters.q) params.set("q", filters.q);
  if (filters.tag) params.set("tag", filters.tag);
  if (filters.series) params.set("series", filters.series);
  if (filters.collection) params.set("collection", filters.collection);
  params.set("limit", String(limit));
  params.set("offset", String(offset));
  return `/api/comics?${params.toString()}`;
}

export async function fetchComics(
  filters: ComicFilters,
  offset = 0,
  limit = COMICS_PAGE_SIZE,
): Promise<ComicPage> {
  return parseComicPage(await http.get<unknown>(comicsPath(filters, offset, limit)));
}

/** How far through a comic a reader is, 0–100. */
export function progressPct(progress: Progress | undefined): number {
  if (!progress || progress.pageCount <= 0) return 0;
  return Math.round(((progress.page + 1) / progress.pageCount) * 100);
}
