import { STORE_SESSION } from "./cacheNames";
import { idbClear, idbGet, idbPut, idbTry } from "./db";
import type { Session } from "../api/generated";

/**
 * The last session GET /auth/me confirmed.
 *
 * Offline, "who is signed in?" has no answer available — the cookie is opaque
 * to us and the only thing that can read it is unreachable. Without this the
 * unanswered question resolves to "nobody", and a reader who downloaded comics
 * for a flight gets bounced to a login page that cannot log them in either.
 *
 * This grants no access the client did not already have: it unlocks the bytes
 * in PAGE_CACHE, which this same session put on disk, and nothing else. Every
 * live request still goes to the server, which re-checks the cookie itself. A
 * 401 or a sign-out clears this, so a session the server has actually ended
 * survives here only until the next time the client can reach it.
 */

const KEY = "current";

export function cacheSession(session: Session): Promise<void> {
  return idbTry(idbPut(STORE_SESSION, session, KEY), undefined);
}

export function readCachedSession(): Promise<Session | undefined> {
  return idbTry(idbGet<Session>(STORE_SESSION, KEY), undefined);
}

export function forgetCachedSession(): Promise<void> {
  return idbTry(idbClear(STORE_SESSION), undefined);
}
