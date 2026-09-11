import { useEffect, useRef, type ReactNode } from 'react';
import { useNavigate } from '@tanstack/react-router';
import type { UseInfiniteQueryResult, UseQueryResult } from '@tanstack/react-query';
import { errorStatus } from '../lib/apiError.ts';
import { useToast } from './toastContext.ts';
import { QueryError } from './QueryState.tsx';

/** GuardedQuery is what useQueryGuard accepts: a plain `useQuery` result or
 * a paginated `useInfiniteQuery` one (the hosts list, the activity feed).
 * Both expose the same `data`/`isError`/`error`/`errorUpdatedAt` fields
 * the guard decides on. */
export type GuardedQuery<TData, TError> = UseQueryResult<TData, TError> | UseInfiniteQueryResult<TData, TError>;

/**
 * useQueryGuard is the single place that decides what a failed query does
 * to the page rendering it — split out of QueryState.tsx (which owns the
 * error/loading presentation) only because that file is a fast-refresh
 * boundary that may export components alone (react-refresh/only-export-
 * components); this hook is the natural extension of the same decision,
 * fixing the regression the previous round's own
 * `if (query.isError) return <QueryError/>` wiring introduced everywhere
 * it was used.
 *
 * TanStack Query sets `isError`/`error` on ANY failed fetch — including a
 * background refetch (a transient 500, a brief network drop, a refetch
 * on window focus) — and it leaves the previous `data` in place when that
 * happens. Checking `isError` before `data` treated "never loaded" and
 * "loaded fine, then a background refresh hiccuped" identically, so a
 * transient background failure unmounted a page the user was actively
 * using and replaced it with a full-page error, discarding whatever they
 * were doing.
 *
 * The distinction this hook makes:
 * - No data yet and the query failed → return the `QueryError` element;
 *   the caller renders it exactly as before (fix round 1's "Important 7").
 * - Data already present and a background refresh failed → return `null`
 *   (the caller keeps rendering its data) and instead raise a toast, once
 *   per failed fetch (`errorUpdatedAt` dedupes — a re-render caused by
 *   something unrelated, e.g. the toast's own auto-dismiss timer, does
 *   not raise it again for the same failure).
 * - A 401 on a background refresh is the one exception: the session is
 *   gone and every further action on the page will fail too, so this
 *   redirects to /login itself — one place, not one per page.
 * - For an infinite query, a failed "Load more" page fetch is a background
 *   failure like any other: the loaded pages stay and one toast says so.
 *
 * Usage (replaces the old `if (query.isError) return <QueryError .../>`):
 *
 *   const guard = useQueryGuard(host);
 *   if (guard) return guard;
 *   if (!host.data) return <QueryLoading />;
 *
 * A layout with several queries (OrgLayout, AccountSecurityLayout) calls
 * this once per query and returns the first non-null guard.
 *
 * `options.enabled` (default true) lets a page opt a query out entirely —
 * HostsListPage's tag-selector mode displays `hostQuery.data`, not
 * `hosts.data`, while active, so a background failure of the (currently
 * unused) plain hosts list should neither toast nor redirect. The hook
 * still has to be called unconditionally (rules of hooks), so this is a
 * flag rather than a conditional call.
 */
export function useQueryGuard<TData, TError>(
  query: GuardedQuery<TData, TError>,
  options?: { enabled?: boolean },
): ReactNode | null {
  const enabled = options?.enabled ?? true;
  const navigate = useNavigate();
  const toast = useToast();
  const hasData = query.data !== undefined;
  const nextPageFailed = 'isFetchNextPageError' in query && query.isFetchNextPageError;
  // Tracks the errorUpdatedAt of the last background failure this hook
  // has already reacted to, so the toast/redirect below fires once per
  // failed fetch, not once per render.
  const lastHandled = useRef<number | null>(null);

  useEffect(() => {
    if (!enabled || !query.isError || !hasData) return;
    if (lastHandled.current === query.errorUpdatedAt) return;
    lastHandled.current = query.errorUpdatedAt;

    if (errorStatus(query.error) === 401) {
      void navigate({ to: '/login', search: { step: undefined } });
      return;
    }
    toast.show(
      nextPageFailed
        ? 'Could not load more — showing what is already loaded.'
        : 'Could not refresh this page — showing the last loaded data.',
      'destructive',
    );
  }, [enabled, query.isError, hasData, nextPageFailed, query.errorUpdatedAt, query.error, navigate, toast]);

  if (enabled && query.isError && !hasData) {
    return <QueryError error={query.error} />;
  }
  return null;
}
