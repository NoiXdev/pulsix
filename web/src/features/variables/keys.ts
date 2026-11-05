/**
 * Stable per-row keys for an editable list: a parallel array that moves
 * with its rows on add, move and remove instead of following their index,
 * so a row's inputs, focus and local state stay with the row.
 */
export function newKey(): string {
  return Math.random().toString(36).slice(2);
}

export function moveItem<T>(items: readonly T[], from: number, to: number): T[] {
  const next = [...items];
  next.splice(to, 0, next.splice(from, 1)[0]);
  return next;
}

export function removeItem<T>(items: readonly T[], index: number): T[] {
  return items.filter((_, i) => i !== index);
}
