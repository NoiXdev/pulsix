import type { SortState } from './types.ts';

/** The next sort after a click on a column: none → ascending → descending →
 * none (the list's default order). Another column starts at ascending. */
export function nextSort(sort: SortState | null | undefined, key: string): SortState | null {
  if (sort?.key !== key) return { key, order: 'asc' };
  return sort.order === 'asc' ? { key, order: 'desc' } : null;
}
