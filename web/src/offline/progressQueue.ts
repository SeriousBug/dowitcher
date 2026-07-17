import { STORE_PROGRESS_QUEUE } from "./cacheNames";
import { idbDelete, idbGetAll, idbPut, idbTry } from "./db";
import { http, HttpError } from "../api/http";
import type { Progress, ProgressRequest } from "../api/generated";

/**
 * Reading position survives being offline.
 *
 * A page turn with no network is queued and answered optimistically, so the
 * reader never stalls waiting for a request that cannot succeed. The queue is
 * keyed by comic id (see db.ts), which collapses it for free: flipping through
 * forty pages leaves one row holding the newest position, not forty page turns
 * to replay.
 *
 * UpdatedAt is what makes replay safe. It is when the position was *observed*,
 * not when it was sent, so an hour-old queued position arriving after you have
 * read on elsewhere is recognisably older and the server drops it. Without it,
 * arrival order would decide, and a phone coming out of a tunnel would clobber
 * the page you are on now.
 */

export interface QueuedProgress {
  comicId: string;
  page: number;
  completed: boolean;
  updatedAt: number;
}

type Reconciler = (progress: Progress) => void;
const reconcilers = new Set<Reconciler>();

/**
 * The server answers a replay with the position it decided to keep, which may
 * be the one already stored rather than ours. Whoever is showing progress
 * subscribes here and takes what came back.
 */
export function onProgressReconciled(fn: Reconciler): () => void {
  reconcilers.add(fn);
  return () => reconcilers.delete(fn);
}

function reconcile(progress: Progress) {
  for (const fn of reconcilers) fn(progress);
}

function put(comicID: string, body: ProgressRequest): Promise<Progress> {
  return http.put<Progress>(`/api/comics/${comicID}/progress`, { ...body });
}

export function enqueueProgress(comicID: string, body: ProgressRequest, observedAt: number): Promise<void> {
  const row: QueuedProgress = {
    comicId: comicID,
    page: body.page,
    completed: body.completed,
    updatedAt: observedAt,
  };
  return idbTry(idbPut(STORE_PROGRESS_QUEUE, row), undefined);
}

/**
 * Writes a position, queueing it if the network is gone.
 *
 * Returns the server's answer, or null when the write was queued — the caller
 * has nothing to reconcile against yet and should keep showing its own
 * position. A rejected fetch is a lost network; an HttpError means the server
 * answered and disagreed, which queueing would not fix.
 */
export async function saveProgress(comicID: string, body: ProgressRequest): Promise<Progress | null> {
  const observedAt = body.updatedAt ?? Math.floor(Date.now() / 1000);
  try {
    return await put(comicID, { ...body, updatedAt: observedAt });
  } catch (err) {
    if (err instanceof HttpError) throw err;
    await enqueueProgress(comicID, body, observedAt);
    return null;
  }
}

/**
 * Whether a failed replay is worth keeping.
 *
 * A 4xx is the server saying the claim is wrong in a way that retrying cannot
 * fix — a comic that no longer exists, a body it won't take — so the row goes.
 * The exceptions are the ones that mean "not now": an expired session, a
 * timeout, a rate limit, and every 5xx.
 */
function keepOnError(err: unknown): boolean {
  if (!(err instanceof HttpError)) return true;
  if (err.status >= 500) return true;
  return err.status === 401 || err.status === 403 || err.status === 408 || err.status === 429;
}

let replaying = false;

/**
 * Drains the queue. Safe to call whenever — on launch, on `online`, and after
 * a sign-in.
 *
 * Background Sync would do this without a live page, but it does not exist on
 * Safari/iOS or Firefox for Android, which is most of the phones this app is
 * for. The `online` event plus a replay on launch is the portable path and is
 * enough on its own: reading position is not worth a second mechanism that
 * only two thirds of users would ever benefit from.
 */
export async function replayProgressQueue(): Promise<void> {
  if (replaying) return;
  replaying = true;
  try {
    const rows = await idbTry(idbGetAll<QueuedProgress>(STORE_PROGRESS_QUEUE), []);
    for (const row of rows) {
      try {
        const current = await put(row.comicId, {
          page: row.page,
          completed: row.completed,
          updatedAt: row.updatedAt,
        });
        await idbTry(idbDelete(STORE_PROGRESS_QUEUE, row.comicId), undefined);
        // Take the server's word, including when it kept a newer position over
        // ours. Retrying a claim it has already judged stale would never
        // converge.
        reconcile(current);
      } catch (err) {
        if (keepOnError(err)) {
          // Still offline, or not our moment. Stop rather than march through
          // the rest failing identically.
          if (!(err instanceof HttpError)) return;
          continue;
        }
        await idbTry(idbDelete(STORE_PROGRESS_QUEUE, row.comicId), undefined);
      }
    }
  } finally {
    replaying = false;
  }
}

export function queuedProgress(): Promise<QueuedProgress[]> {
  return idbTry(idbGetAll<QueuedProgress>(STORE_PROGRESS_QUEUE), []);
}
