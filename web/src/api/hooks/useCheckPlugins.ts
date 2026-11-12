import { useQuery } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';

/**
 * The check plugins (backlog 3.54): every plugin the manager knows, in the
 * order a form offers them, with its parameters and the kind of each
 * metric. The check forms render their parameter fields from it and the
 * host page asks it which metrics have a series.
 *
 * Read once per session: the descriptors are compiled into the manager
 * and never change while it runs, so the cached answer never goes stale.
 * Sign-out resets the whole cache (lib/session.ts), so the next session
 * reads them again.
 */
export function useCheckPlugins(opts: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: ['check-plugins'] as const,
    queryFn: () => unwrap(api.GET('/api/v1/check-plugins')),
    select: (body) => body.plugins,
    staleTime: Infinity,
    gcTime: Infinity,
    enabled: opts.enabled,
  });
}
