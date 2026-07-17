import { STORE_COMICS } from "./cacheNames";
import { idbDelete, idbGet, idbPut, idbTry } from "./db";
import type { ComicPage } from "../api/comics";
import type { ComicDetail, Progress } from "../api/generated";

/**
 * Comic metadata kept on disk so the library grid and the reader can render
 * with no network. The page *bytes* live in PAGE_CACHE (see downloads.ts);
 * this is only the paperwork around them — without it a downloaded comic has
 * every page on disk and no page list to show them in.
 *
 * Every write here is best-effort. Failing to cache metadata is not a reason
 * to fail the request that produced it.
 */

const detailKey = (comicID: string) => `detail:${comicID}`;
const libraryKey = (filterKey: string) => `library:${filterKey}`;

/** Maps are structured-cloneable, but a plain array survives a schema change better. */
interface StoredLibraryPage {
  comics: ComicPage["comics"];
  progress: Progress[];
  total: number;
}

export function cacheComicDetail(detail: ComicDetail): Promise<void> {
  return idbTry(idbPut(STORE_COMICS, detail, detailKey(detail.comic.id)), undefined);
}

export function readComicDetail(comicID: string): Promise<ComicDetail | undefined> {
  return idbTry(idbGet<ComicDetail>(STORE_COMICS, detailKey(comicID)), undefined);
}

export function forgetComicDetail(comicID: string): Promise<void> {
  return idbTry(idbDelete(STORE_COMICS, detailKey(comicID)), undefined);
}

/**
 * Only the first page of a filter is cached. Offline, "show more" has nothing
 * to fetch anyway, and caching every scroll depth of every search someone ever
 * typed would grow without bound for a list they can't read past.
 */
export function cacheLibraryPage(filterKey: string, page: ComicPage): Promise<void> {
  const stored: StoredLibraryPage = {
    comics: page.comics,
    progress: [...page.progress.values()],
    total: page.total,
  };
  return idbTry(idbPut(STORE_COMICS, stored, libraryKey(filterKey)), undefined);
}

export async function readLibraryPage(filterKey: string): Promise<ComicPage | undefined> {
  const stored = await idbTry(idbGet<StoredLibraryPage>(STORE_COMICS, libraryKey(filterKey)), undefined);
  if (!stored) return undefined;
  const progress = new Map<string, Progress>();
  for (const p of stored.progress) progress.set(p.comicId, p);
  // Total is clamped to what was actually kept, not what the server said the
  // shelf holds. The only thing reading it is the pager, and a cached page that
  // claims 200 comics behind it offers a "Show more" that cannot resolve — the
  // rest of the shelf is on the server, which is the thing we don't have.
  return { comics: stored.comics, progress, total: Math.min(stored.total, stored.comics.length) };
}
