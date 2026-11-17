/**
 * The page buttons of a pager: always the first and the last page and the
 * current one with its neighbours, and a gap for each run of pages left
 * out. A gap that would stand for exactly one page shows that page
 * instead, since a "…" is no shorter than a number.
 */
export function pageItems(page: number, pageCount: number): (number | 'gap')[] {
  const last = Math.max(1, pageCount);
  const current = Math.min(Math.max(1, page), last);
  const shown = new Set([1, current - 1, current, current + 1, last].filter((p) => p >= 1 && p <= last));
  const sorted = [...shown].sort((a, b) => a - b);
  const items: (number | 'gap')[] = [];
  let prev = 0;
  for (const p of sorted) {
    if (p - prev === 2) items.push(p - 1);
    else if (p - prev > 2) items.push('gap');
    items.push(p);
    prev = p;
  }
  return items;
}
