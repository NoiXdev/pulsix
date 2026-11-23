/**
 * mapPool runs `fn` over `items` with at most `limit` calls in flight and
 * answers the results in the items' order. The first rejection rejects the
 * whole and no further item is started; neither is one once `signal` aborts
 * (which rejects with its reason). `fn` gets the signal to pass on. For reads
 * the API has no batch endpoint for: one request per item, never a burst of
 * hundreds, and none left running for a caller that went away.
 */
export async function mapPool<T, R>(
  items: readonly T[],
  limit: number,
  fn: (item: T, signal?: AbortSignal) => Promise<R>,
  signal?: AbortSignal,
): Promise<R[]> {
  const out = new Array<R>(items.length);
  let next = 0;
  let stopped = false;
  async function worker() {
    while (!stopped && next < items.length) {
      signal?.throwIfAborted();
      const i = next++;
      try {
        out[i] = await fn(items[i], signal);
      } catch (err) {
        stopped = true;
        throw err;
      }
    }
  }
  await Promise.all(
    Array.from({ length: Math.min(Math.max(limit, 1), items.length) }, worker),
  );
  signal?.throwIfAborted();
  return out;
}
