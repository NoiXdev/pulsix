/**
 * Whether a client list's search text matches a row: every word of `q` must
 * appear (case ignored) in at least one of the row's `fields`, so "web prod"
 * finds a row that has both words, wherever they are. An empty search
 * matches everything; missing fields are skipped.
 */
export function matchesText(q: string | undefined, ...fields: Array<string | null | undefined>): boolean {
  const words = (q ?? '').toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return true;
  const haystack = fields
    .filter((f): f is string => !!f)
    .join('\n')
    .toLowerCase();
  return words.every((w) => haystack.includes(w));
}
