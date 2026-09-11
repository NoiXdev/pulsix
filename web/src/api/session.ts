import { useCallback } from 'react';
import { useQueryClient, type QueryClient } from '@tanstack/react-query';

/**
 * resetSessionCache drops everything the SPA has cached on behalf of the
 * signed-in principal. Query keys are not scoped by user (['hosts', org],
 * ['me'], ...), so without this a second person signing in on the same
 * tab — or the same person after their session expired — was served the
 * previous session's cached data: pressing Back after a sign-out and a
 * sign-in as someone else rendered the first user's host list straight
 * from the cache while the refetch was still in flight.
 *
 * `QueryClient.clear()` empties both the query cache (destroying each
 * query, which also cancels any in-flight fetch so a late response cannot
 * repopulate it) and the mutation cache (which can still hold request
 * payloads such as a TOTP code or an issued token's secret).
 *
 * Call it at every session boundary: sign-out, a successful sign-in
 * (before re-fetching /auth/me), a 401 redirect to /login, and anywhere
 * else a request swaps the session cookie for a different principal
 * (invitation acceptance, first-run setup).
 *
 * When a page that is still mounted triggers the reset (sign-out from the
 * shell, a 401 on a mounted page), navigate away first and reset after:
 * clearing first would let the still-mounted page's observers rebuild
 * their queries on its next render and re-fetch them with the dead
 * session.
 */
export function resetSessionCache(qc: QueryClient): void {
  qc.clear();
}

/** useResetSession is resetSessionCache bound to the QueryClient from
 * context; the returned function is stable across renders, so it is safe
 * to list in an effect's dependencies. */
export function useResetSession(): () => void {
  const qc = useQueryClient();
  return useCallback(() => resetSessionCache(qc), [qc]);
}
