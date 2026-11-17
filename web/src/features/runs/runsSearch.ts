/**
 * The runs list's search for one host's live runs (`?host=<id>&status=active`):
 * where the hosts list's Active runs cell and its row menu lead.
 */
export function liveRunsOf(hostId: string) {
  return { host: hostId, status: 'active', kind: undefined, trigger: undefined, schedule: undefined };
}
