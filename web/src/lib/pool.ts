/**
 * mapPool runs `fn` over `items` with at most `limit` calls in flight and
 * answers the results in the items' order; the first rejection rejects the
 * whole. For reads the API has no batch endpoint for: one request per item,
 * but never a burst of hundreds.
 */
export async function mapPool<T, R>(items: readonly T[], limit: number, fn: (item: T) => Promise<R>): Promise<R[]> {
  const out = new Array<R>(items.length);
  let next = 0;
  async function worker() {
    while (next < items.length) {
      const i = next++;
      out[i] = await fn(items[i]);
    }
  }
  await Promise.all(Array.from({ length: Math.min(Math.max(limit, 1), items.length) }, worker));
  return out;
}
