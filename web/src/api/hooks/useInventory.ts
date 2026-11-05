import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { paths } from '../schema.d.ts';
import type { VarSchema, Vars } from '../../features/inventory/schema/types.ts';

type SchemaBody = paths['/api/v1/orgs/{org}/inventory/groups/{id}/schema']['put']['requestBody']['content']['application/json'];

const ifMatch = (revision: number) => ({ 'If-Match': `"${revision}"` });

/** toVarSchema reads a schema from an API response; the generated type
 * mirrors VarSchema but may mark lists nullable. */
export function toVarSchema(raw: unknown): VarSchema {
  const fields = (raw as { fields?: VarSchema['fields'] | null } | undefined)?.fields;
  return { fields: fields ?? [] };
}

function useInvalidate(org: string) {
  const qc = useQueryClient();
  return () => qc.invalidateQueries({ queryKey: ['inventory', org] });
}

export function useGroups(org: string) {
  return useQuery({
    queryKey: ['inventory', org, 'groups'],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/inventory/groups', { params: { path: { org } } })),
    enabled: !!org,
  });
}

const groupKey = (org: string, id: string) => ['inventory', org, 'group', id];

function fetchGroup(org: string, id: string) {
  return unwrap(api.GET('/api/v1/orgs/{org}/inventory/groups/{id}', { params: { path: { org, id } } }));
}

export function useGroup(org: string, id: string) {
  return useQuery({
    queryKey: groupKey(org, id),
    queryFn: () => fetchGroup(org, id),
    enabled: !!org && !!id,
  });
}

/** useReloadInventory answers a 412: it refetches what the organization's
 * inventory pages show and resolves, once that is done, to the stored
 * version of the given group, so a form can start over from it. It
 * rejects when a refetch fails. */
export function useReloadInventory(org: string) {
  const qc = useQueryClient();
  return async (groupId: string) => {
    await qc.invalidateQueries({ queryKey: ['inventory', org] }, { throwOnError: true });
    return qc.getQueryData<Awaited<ReturnType<typeof fetchGroup>>>(groupKey(org, groupId))?.group;
  };
}

export function useCreateGroup(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: { name: string; description?: string; parent_id?: string }) =>
      unwrap(api.POST('/api/v1/orgs/{org}/inventory/groups', { params: { path: { org } }, body })),
    onSuccess: invalidate,
  });
}

export interface GroupPatch {
  name: string;
  description?: string;
  parent_id?: string;
  bastion_id?: string;
  revision: number;
}

export function useUpdateGroup(org: string, id: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: ({ revision, ...body }: GroupPatch) =>
      unwrap(api.PATCH('/api/v1/orgs/{org}/inventory/groups/{id}', { params: { path: { org, id }, header: ifMatch(revision) }, body })),
    onSuccess: invalidate,
  });
}

export function useDeleteGroup(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/inventory/groups/{id}', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

export function useSetGroupSchema(org: string, id: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: ({ schema, revision }: { schema: VarSchema; revision: number }) =>
      unwrap(
        api.PUT('/api/v1/orgs/{org}/inventory/groups/{id}/schema', {
          params: { path: { org, id }, header: ifMatch(revision) },
          body: { schema } as SchemaBody,
        }),
      ),
    onSuccess: invalidate,
  });
}

/** gcTime: 0 — the variables carry plaintext secrets the user just typed;
 * the page also calls reset() after a successful save (the v0.1 token
 * lesson: nothing secret may linger in the mutation cache). */
export function useSetGroupVars(org: string, id: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: ({ vars, revision }: { vars: Vars; revision: number }) =>
      unwrap(api.PUT('/api/v1/orgs/{org}/inventory/groups/{id}/vars', { params: { path: { org, id }, header: ifMatch(revision) }, body: { vars } })),
    gcTime: 0,
    onSuccess: invalidate,
  });
}

export function useGroupMembers(org: string, id: string) {
  return useQuery({
    queryKey: ['inventory', org, 'members', id],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/inventory/groups/{id}/members', { params: { path: { org, id } } })),
    enabled: !!org && !!id,
  });
}

/** gcTime: 0 for the same reason as useSetGroupVars. */
export function useAssignHost(org: string, groupId: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: ({ hostId, vars, revision }: { hostId: string; vars: Vars; revision?: number }) =>
      unwrap(
        api.PUT('/api/v1/orgs/{org}/inventory/groups/{id}/members/{host}', {
          params: { path: { org, id: groupId, host: hostId }, header: revision === undefined ? {} : ifMatch(revision) },
          body: { vars },
        }),
      ),
    gcTime: 0,
    onSuccess: invalidate,
  });
}

export function useUnassignHost(org: string, groupId: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (hostId: string) =>
      unwrap(api.DELETE('/api/v1/orgs/{org}/inventory/groups/{id}/members/{host}', { params: { path: { org, id: groupId, host: hostId } } })),
    onSuccess: invalidate,
  });
}

export function useInventoryPreview(org: string, groupId?: string) {
  return useQuery({
    queryKey: ['inventory', org, 'preview', groupId ?? ''],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/inventory/preview', { params: { path: { org }, query: { group: groupId ?? '' } } })),
    enabled: !!org,
  });
}

export function useHostInventoryGroups(org: string, hostId: string) {
  return useQuery({
    queryKey: ['inventory', org, 'host-groups', hostId],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/hosts/{id}/inventory/groups', { params: { path: { org, id: hostId } } })),
    enabled: !!org && !!hostId,
  });
}

export function useHostInventoryPreview(org: string, hostId: string) {
  return useQuery({
    queryKey: ['inventory', org, 'host-preview', hostId],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/hosts/{id}/inventory/preview', { params: { path: { org, id: hostId } } })),
    enabled: !!org && !!hostId,
  });
}
