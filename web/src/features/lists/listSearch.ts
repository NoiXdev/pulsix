/** The highest page the server accepts (internal/paging.MaxPage); a later
 * one is answered with 422. A page between the last and this one is
 * clamped to the last by useListView. */
const MAX_PAGE = 1_000_000;

/**
 * A list's view in the URL: `?sort=&order=&page=`. Every key is optional
 * and absent for the default (the list's own order, page 1), so a link
 * without them stays valid.
 */
export interface ListSearch {
  sort?: string;
  order?: 'asc' | 'desc';
  page?: number;
}

/**
 * Reads a list's keys out of a route's raw search. A list route's
 * `validateSearch` spreads it next to its own keys:
 * `validateSearch: (s) => ({ ...validateListSearch(s), q: … })`.
 * Anything malformed is left out; page 1 is left out too. Whether the sort
 * key is one the list offers is useListView's call.
 */
export function validateListSearch(search: Record<string, unknown>): ListSearch {
  const out: ListSearch = {};
  if (typeof search.sort === 'string' && search.sort !== '') out.sort = search.sort;
  if (search.order === 'asc' || search.order === 'desc') out.order = search.order;
  const page = pageNumber(search.page);
  if (page !== undefined && page > 1) out.page = Math.min(page, MAX_PAGE);
  return out;
}

function pageNumber(value: unknown): number | undefined {
  const n = typeof value === 'number' ? value : typeof value === 'string' && /^\d+$/.test(value) ? Number(value) : NaN;
  return Number.isSafeInteger(n) ? n : Number.isFinite(n) && Number.isInteger(n) ? MAX_PAGE : undefined;
}
