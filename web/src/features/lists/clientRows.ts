import type { DataListColumn, SortState } from '../../ui/data-list/index.ts';

type Value = string | number | boolean | null | undefined;

function missing(v: Value): boolean {
  return v === null || v === undefined || v === '';
}

/**
 * Sorts a whole list in the browser by the column whose `sortKey` is
 * `sort.key`, through its `sortValue`. Strings compare in the page's
 * language with numbers inside them read as numbers (host9 before host10),
 * case ignored. Missing values (null, undefined, '') go last in both
 * directions, as the server sorts NULLs; equal values keep the `rowKey`
 * order, so the order never depends on the input's. Without a sort, or for
 * a key no column offers, the rows stay as given. Returns a new array.
 */
export function sortRows<Row>(
  rows: readonly Row[],
  columns: DataListColumn<Row>[],
  sort: SortState | null,
  rowKey: (r: Row) => string,
  locale: string,
): Row[] {
  const column = sort ? columns.find((c) => c.sortKey === sort.key && c.sortValue) : undefined;
  if (!sort || !column?.sortValue) return [...rows];
  const value = column.sortValue;
  const collator = new Intl.Collator(locale, { numeric: true, sensitivity: 'base' });
  const dir = sort.order === 'desc' ? -1 : 1;
  const keyed = rows.map((row) => ({ row, v: value(row), k: rowKey(row) }));
  keyed.sort((a, b) => {
    const am = missing(a.v);
    const bm = missing(b.v);
    if (am !== bm) return am ? 1 : -1;
    const c = am ? 0 : compare(a.v, b.v, collator) * dir;
    if (c !== 0) return c;
    return a.k < b.k ? -1 : a.k > b.k ? 1 : 0;
  });
  return keyed.map((x) => x.row);
}

function compare(a: Value, b: Value, collator: Intl.Collator): number {
  if (typeof a === 'number' && typeof b === 'number') return a - b;
  if (typeof a === 'boolean' && typeof b === 'boolean') return Number(a) - Number(b);
  return collator.compare(String(a), String(b));
}

/** The rows of page `page` (1-based) at `perPage` rows a page. */
export function pageRows<Row>(rows: readonly Row[], page: number, perPage: number): Row[] {
  const from = (page - 1) * perPage;
  return rows.slice(from, from + perPage);
}
