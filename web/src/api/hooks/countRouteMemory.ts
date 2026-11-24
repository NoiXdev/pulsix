// When each org's run request count route last answered as missing, in epoch
// ms. Module state on purpose: it must outlive the query's cache entry, which
// a decision invalidates. Import-free so src/test/setup.ts can reset it after
// every test without loading src/api/client.ts before the fetch mock is
// installed.
const missing = new Map<string, number>();

export const countRouteMemory = {
  get: (org: string) => missing.get(org),
  set: (org: string, at: number) => void missing.set(org, at),
  delete: (org: string) => void missing.delete(org),
  clear: () => missing.clear(),
};

/** resetPendingCountRouteMemory forgets every remembered missing route. For tests. */
export function resetPendingCountRouteMemory() {
  missing.clear();
}
