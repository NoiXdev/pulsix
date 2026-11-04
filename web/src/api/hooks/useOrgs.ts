import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';

/** The organizations the signed-in user belongs to — used by the root
 * redirect (first org wins) and the org switcher. */
export function useOrgs(opts: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: ['orgs'],
    queryFn: () => unwrap(api.GET('/api/v1/orgs')),
    enabled: opts.enabled,
  });
}

export function useOrg(slug: string, opts: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: ['org', slug],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}', { params: { path: { org: slug } } })),
    enabled: !!slug && (opts.enabled ?? true),
  });
}

/** Renames the organization (PATCH /orgs/{org}, org.manage). The slug is
 * fixed at creation. Refreshes the org itself and the lists that show its
 * name. */
export function useUpdateOrg(slug: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) =>
      unwrap(api.PATCH('/api/v1/orgs/{org}', { params: { path: { org: slug } }, body: { name } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['org', slug] });
      void qc.invalidateQueries({ queryKey: ['orgs'] });
    },
  });
}
