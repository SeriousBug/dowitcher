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

/**
 * The list response. There is no generated type for it: Comic has no progress
 * field, and ComicDetail carries the page list a grid has no use for, so the
 * endpoint sends the two side by side. Replace this with the tygo output once
 * internal/api grows a named type for it.
 */
interface ComicListResponse {
  comics: Comic[];
  progress: Progress[];
  total: number;
}

/** A page of comics with the caller's progress paired up by comic id. */
export interface ComicPage {
  comics: Comic[];
  progress: Map<string, Progress>;
  total: number;
}

function parseComicPage(body: ComicListResponse): ComicPage {
  const progress = new Map<string, Progress>();
  for (const p of body.progress ?? []) progress.set(p.comicId, p);
  return { comics: body.comics ?? [], progress, total: body.total ?? 0 };
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
  return parseComicPage(await http.get<ComicListResponse>(comicsPath(filters, offset, limit)));
}

/** How far through a comic a reader is, 0–100. */
export function progressPct(progress: Progress | undefined): number {
  if (!progress || progress.pageCount <= 0) return 0;
  return Math.round(((progress.page + 1) / progress.pageCount) * 100);
}
