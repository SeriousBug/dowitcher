/** Standard US comic trim, 6.625" x 10.1875" — the same ratio as the cover token. */
export const TRIM_RATIO = 0.65;

/**
 * Groups page indices into what gets shown at once. Single-page mode is the
 * degenerate case of one page per group, so every caller — navigation,
 * preloading, the progress "completed" test — can work in spreads and never
 * branch on the mode.
 *
 * Two rules keep the pairing honest, and both are what naive implementations
 * get wrong:
 *
 *  - The cover stands alone. Pairing page 0 with page 1 offsets every spread in
 *    the book by one, which splits every real double-page spread down the
 *    middle and pairs artwork that was never drawn to sit together.
 *  - A landscape page is already a double-page spread that the scanner captured
 *    in one shot. Pairing it with its neighbour puts a two-page-wide image next
 *    to an unrelated page and shrinks both to unreadable. It gets the width to
 *    itself, and — critically — it also resets the odd/even rhythm, which is why
 *    this walks the list rather than slicing it into fixed pairs.
 */
export function buildSpreads(
  pageCount: number,
  isLandscape: (index: number) => boolean,
  enabled: boolean,
): number[][] {
  if (pageCount <= 0) return [];
  if (!enabled) return Array.from({ length: pageCount }, (_, i) => [i]);

  const spreads: number[][] = [[0]];
  let i = 1;
  while (i < pageCount) {
    if (isLandscape(i) || i + 1 >= pageCount || isLandscape(i + 1)) {
      spreads.push([i]);
      i += 1;
    } else {
      spreads.push([i, i + 1]);
      i += 2;
    }
  }
  return spreads;
}

/** Which group holds a page. Position is tracked as a page, never a spread, so that
 *  re-pairing (a late-measured landscape page) can't move the reader. */
export function spreadIndexOf(spreads: number[][], page: number): number {
  const found = spreads.findIndex((s) => s.includes(page));
  return found === -1 ? 0 : found;
}
