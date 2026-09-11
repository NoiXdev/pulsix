import { useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';

/** The signed-in principal (src/routes root logic reads `mfa_complete` to
 * decide whether a session still needs its second factor). Disabled
 * callers should pass `enabled: false` rather than relying on a 401 —
 * see useAuth.ts's login/logout mutations, which invalidate this query
 * instead of calling it eagerly on an anonymous session. */
export function useMe(opts: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: ['me'],
    queryFn: () => unwrap(api.GET('/api/v1/auth/me')),
    retry: false,
    enabled: opts.enabled,
  });
}

export function invalidateMe(qc: QueryClient) {
  return qc.invalidateQueries({ queryKey: ['me'] });
}

export function useInvalidateMe() {
  const qc = useQueryClient();
  return () => invalidateMe(qc);
}
