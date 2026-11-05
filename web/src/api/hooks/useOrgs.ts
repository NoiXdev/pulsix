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
    // Returned, so `mutateAsync` resolves only once the fresh name is in the
    // cache: a form that drops its draft on success would otherwise show the
    // old name until the refetch lands.
    onSuccess: () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: ['org', slug] }),
        qc.invalidateQueries({ queryKey: ['orgs'] }),
      ]),
  });
}

/** useAdminOrgs is the instance admin's list of every organization, with
 * member counts and whether the admin is a member of each. */
export function useAdminOrgs() {
  return useQuery({
    queryKey: ['admin-orgs'],
    queryFn: async () => (await unwrap(api.GET('/api/v1/admin/orgs'))).orgs ?? [],
  });
}

/** useCreateOrg creates an organization (POST /orgs, instance admins
 * only); the caller becomes its owner. Refreshes every list of
 * organizations: the switcher's and the Organizations page's. */
export function useCreateOrg() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { slug: string; name: string }) => unwrap(api.POST('/api/v1/orgs', { body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['orgs'] });
      void qc.invalidateQueries({ queryKey: ['admin-orgs'] });
    },
  });
}
