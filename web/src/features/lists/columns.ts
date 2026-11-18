import type { DataListColumn } from '../../ui/data-list/index.ts';

/** A saved column choice: the ids shown, in order, and every id the list
 * offered when it was saved (absent in older data: then `columns`). */
export interface SavedColumns {
  columns: readonly string[] | null;
  known?: readonly string[] | null;
}

/**
 * The columns a list shows, in order: the saved choice when there is one,
 * else every column with `defaultVisible`. This is the one place that puts
 * the required (name) column first (ruling P9; DataList renders what it is
 * given): ids no column has any more and repeated ids are dropped without a
 * word, and a required column the saved list leaves out is added. A column
 * that was not offered when the choice was saved (not in `known`) and is
 * `defaultVisible` is shown at its default position (backlog 3.101); a known
 * column the saved list leaves out stays hidden. A choice saved without
 * `known` is kept as it is.
 */
export function mergeColumns<Row>(saved: SavedColumns | null | undefined, columns: DataListColumn<Row>[]): string[] {
  const offered = new Map(columns.map((c) => [c.id, c]));
  const defaults = columns.filter((c) => c.defaultVisible).map((c) => c.id);
  const required = columns.filter((c) => c.required).map((c) => c.id);
  const out = [...required];
  for (const id of saved?.columns ?? defaults) {
    if (offered.has(id) && !out.includes(id)) out.push(id);
  }
  if (saved?.columns) {
    // A choice saved before `known` existed is kept as it was: every column
    // offered now counts as known, so nothing the person hid comes back.
    const known = new Set(saved.known?.length ? saved.known : offered.keys());
    for (const [i, id] of defaults.entries()) {
      if (known.has(id) || out.includes(id)) continue;
      // After the nearest default column before it that is shown, else first.
      let at = required.length;
      for (let j = i - 1; j >= 0; j--) {
        const k = out.indexOf(defaults[j]);
        if (k >= 0) {
          at = k + 1;
          break;
        }
      }
      out.splice(at, 0, id);
    }
  }
  return out;
}
