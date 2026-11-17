/**
 * The offset between the server's clock and this browser's, learned from
 * the `Date` header of /auth/me answers (backlog 3.94). The session expiry
 * is a server timestamp; counting it down against a skewed local clock
 * would warn too early, too late, or end a live session on screen.
 *
 * A header has one-second resolution and the answer took a while to
 * arrive, so the offset is good to about a second — plenty for a
 * five-minute warning. Without a header (a proxy that strips it, a test)
 * the offset stays zero and the local clock stands alone.
 */
let offsetMs = 0;

/** recordServerDate learns the offset from an RFC 7231 `Date` header
 * value, or resets it to zero when there is none or it is unreadable. */
export function recordServerDate(header: string | null): void {
  const server = header ? Date.parse(header) : NaN;
  offsetMs = Number.isNaN(server) ? 0 : server - Date.now();
}

/** serverNow is the current time on the server's clock, in epoch ms. */
export function serverNow(): number {
  return Date.now() + offsetMs;
}
