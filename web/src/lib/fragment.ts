/** tokenFromFragment reads `token` from a `#token=…` fragment. A fragment is
 * never sent to a server, so a link that carries its secret there stays out
 * of every request log (backlog 7.74, 7.80). */
export function tokenFromFragment(hash: string): string | null {
  return new URLSearchParams(hash.replace(/^#/, '')).get('token') || null;
}
