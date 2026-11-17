import type { QueryClient } from '@tanstack/react-query';
import { meQueryKey } from '../api/hooks/useMe.ts';
import { serverNow } from './serverClock.ts';

/**
 * How a session ends, for the places that send the person to /login
 * (backlog 3.94).
 *
 * A renewal replaces the session and revokes the old one at once, so a
 * request that left with the old cookie just before (a poll, another tab)
 * is answered 401 although the person is signed in. For RENEWAL_GRACE_MS
 * after a completed renewal (this tab's, or one announced by another tab)
 * a 401 is therefore retried once instead of being taken as final.
 */
export const RENEWAL_GRACE_MS = 15_000;

let renewedAt: number | null = null;

/** markRenewed records that a renewal has just completed. */
export function markRenewed(): void {
  renewedAt = Date.now();
}

/** clearRenewed forgets the last renewal (tests). */
export function clearRenewed(): void {
  renewedAt = null;
}

/** recentlyRenewed reports whether a renewal completed within the grace window. */
export function recentlyRenewed(): boolean {
  return renewedAt !== null && Date.now() - renewedAt <= RENEWAL_GRACE_MS;
}

/** loginSearchAfter401 is the /login search for a 401: with the
 * `session_expired` notice when the cached /auth/me says the session's end
 * has passed on the server's clock (a poll's 401 can beat the expiry
 * watcher to it), without otherwise. */
export function loginSearchAfter401(qc: QueryClient): { step: undefined; notice?: string } {
  const me = qc.getQueryData<{ session_expires_at?: string }>(meQueryKey);
  const end = me?.session_expires_at ? Date.parse(me.session_expires_at) : NaN;
  return !Number.isNaN(end) && end <= serverNow() ? { step: undefined, notice: 'session_expired' } : { step: undefined };
}
