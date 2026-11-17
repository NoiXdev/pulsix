import type { DataListColumn } from '../../ui/data-list/index.ts';

/**
 * The columns a list shows, in order: the saved choice when there is one,
 * else every column with `defaultVisible`. This is the one place that puts
 * the required (name) column first (ruling P9; DataList renders what it is
 * given): ids no column has any more and repeated ids are dropped without a
 * word, and a required column the saved list leaves out is added.
 */
export function mergeColumns<Row>(saved: readonly string[] | null | undefined, columns: DataListColumn<Row>[]): string[] {
  const known = new Map(columns.map((c) => [c.id, c]));
  const wanted = saved ?? columns.filter((c) => c.defaultVisible).map((c) => c.id);
  const required = columns.filter((c) => c.required).map((c) => c.id);
  const out = [...required];
  for (const id of wanted) {
    if (known.has(id) && !out.includes(id)) out.push(id);
  }
  return out;
}
