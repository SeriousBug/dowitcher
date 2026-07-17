/**
 * Asks the browser to exempt our storage from eviction under disk pressure.
 *
 * Worth calling once the user has shown they mean it — installed the app, or
 * started a first download — because without it a comic downloaded for a flight
 * is only a suggestion the browser may drop.
 *
 * Whether it prompts is entirely the vendor's business: Chrome never asks and
 * decides from heuristics (installed, engaged, bookmarked, notifications
 * allowed), Safari never asks and leans towards Home Screen web apps, Firefox
 * does put up a permission prompt. None of them promise a grant, so callers
 * must believe the returned boolean rather than the fact that they asked.
 */
export async function requestPersistentStorage(): Promise<boolean> {
  if (!navigator.storage?.persist) return false;

  try {
    // Already granted: asking again is a no-op at best and a second Firefox
    // prompt at worst.
    if (await navigator.storage.persisted()) return true;
    return await navigator.storage.persist();
  } catch {
    return false;
  }
}
