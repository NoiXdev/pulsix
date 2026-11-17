import { keepPreviousData, useMutation, useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { AccountNotice } from '../types.ts';
import type { ServerPageQuery } from './paging.ts';

/**
 * useAdminUsersPage is the instance admin's list of every account, one
 * numbered page per request (the API's page mode, which also answers the
 * total), filtered by `q` (name or e-mail). The previous page stays on
 * screen while the next one loads. `enabled` waits for the list's
 * preferences (useListView's `ready`).
 */
export function useAdminUsersPage(q: string, page: ServerPageQuery, opts: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: ['admin-users', 'page', q, page],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/admin/users', {
          params: {
            query: {
              q: q || undefined,
              page: page.page,
              per_page: page.per_page,
              sort: page.sort as 'email' | 'name' | 'last_sign_in' | undefined,
              order: page.order,
            },
          },
        }),
      ),
    placeholderData: keepPreviousData,
    enabled: opts.enabled ?? true,
  });
}

/** useAdminUser is one account for the account page and, for an instance
 * admin, the member page. */
export function useAdminUser(userId: string, opts: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: ['admin-user', userId],
    queryFn: async () => (await unwrap(api.GET('/api/v1/admin/users/{userId}', { params: { path: { userId } } }))).user,
    enabled: !!userId && (opts.enabled ?? true),
  });
}

/** After an account action the account, the list and the member lists
 * (which show whether a member is deactivated) are stale. */
function invalidateAccount(qc: QueryClient, userId: string) {
  void qc.invalidateQueries({ queryKey: ['admin-user', userId] });
  void qc.invalidateQueries({ queryKey: ['admin-users'] });
  void qc.invalidateQueries({ queryKey: ['members'] });
}

const path = (userId: string) => ({ params: { path: { userId } } });

function useAccountAction<T>(userId: string, call: () => Promise<T>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: call,
    onSuccess: () => invalidateAccount(qc, userId),
  });
}

/**
 * useSetTemporaryPassword sets a temporary password and hands it back to
 * the caller alone. It is deliberately not a useMutation: TanStack keeps a
 * mutation's result in the client's mutation cache, which any component
 * (and the devtools) can read. The password lives only where the caller
 * puts it, the reveal dialog's own state.
 */
export function useSetTemporaryPassword(userId: string) {
  const qc = useQueryClient();
  return {
    mutateAsync: async () => {
      const res = await unwrap(api.POST('/api/v1/admin/users/{userId}/temporary-password', path(userId)));
      invalidateAccount(qc, userId);
      return res;
    },
  };
}

export function useResetSecondFactor(userId: string) {
  return useAccountAction(userId, async (): Promise<AccountNotice> => {
    const res = await unwrap(api.POST('/api/v1/admin/users/{userId}/second-factor/reset', path(userId)));
    return res.notice;
  });
}

export function useEndSessions(userId: string) {
  return useAccountAction(userId, async (): Promise<undefined> => {
    await unwrap(api.POST('/api/v1/admin/users/{userId}/sessions/revoke', path(userId)));
    return undefined;
  });
}

export function useDeactivateUser(userId: string) {
  return useAccountAction(userId, async (): Promise<AccountNotice> => {
    const res = await unwrap(api.POST('/api/v1/admin/users/{userId}/deactivate', path(userId)));
    return res.notice;
  });
}

export function useReactivateUser(userId: string) {
  return useAccountAction(userId, async (): Promise<undefined> => {
    await unwrap(api.POST('/api/v1/admin/users/{userId}/reactivate', path(userId)));
    return undefined;
  });
}

export function useSendResetLink(userId: string) {
  return useAccountAction(userId, async (): Promise<AccountNotice> => {
    const res = await unwrap(api.POST('/api/v1/admin/users/{userId}/password-reset-link', path(userId)));
    return res.notice;
  });
}
