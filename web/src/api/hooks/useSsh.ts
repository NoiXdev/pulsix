import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { Host } from '../types.ts';
import { ifMatch } from './useHosts.ts';

export function useBastions(org: string) {
  return useQuery({
    queryKey: ['ssh', org, 'bastions'],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/bastions', { params: { path: { org } } })),
    enabled: !!org,
  });
}

export interface BastionInput {
  name: string;
  address: string;
  port?: number;
  username: string;
  credential_id?: string;
  via_bastion_id?: string;
}

export interface HostSSHInput {
  username?: string;
  port?: number;
  credential_id?: string;
  bastion_mode?: 'inherit' | 'none' | 'explicit';
  bastion_id?: string;
}

function useInvalidateSsh(org: string) {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ['ssh', org] });
    void qc.invalidateQueries({ queryKey: ['zones', org] });
    void qc.invalidateQueries({ queryKey: ['inventory', org] });
    void qc.invalidateQueries({ queryKey: ['credentials', org] });
  };
}

export function useCreateBastion(org: string) {
  const invalidate = useInvalidateSsh(org);
  return useMutation({
    mutationFn: (body: BastionInput) => unwrap(api.POST('/api/v1/orgs/{org}/bastions', { params: { path: { org } }, body })),
    onSuccess: invalidate,
  });
}

export function useUpdateBastion(org: string, id: string) {
  const invalidate = useInvalidateSsh(org);
  return useMutation({
    mutationFn: (body: BastionInput) => unwrap(api.PATCH('/api/v1/orgs/{org}/bastions/{id}', { params: { path: { org, id } }, body })),
    onSuccess: invalidate,
  });
}

export function useDeleteBastion(org: string) {
  const invalidate = useInvalidateSsh(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/bastions/{id}', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

// useSetBastionKey pins a jump host's key: the fingerprint the newest
// connection test observed at the hop (the pin-what-you-saw rule is the
// server's check, backlog 2.40).
export function useSetBastionKey(org: string, id: string) {
  const invalidate = useInvalidateSsh(org);
  return useMutation({
    mutationFn: (fingerprint: string) =>
      unwrap(api.PUT('/api/v1/orgs/{org}/bastions/{id}/host-key', { params: { path: { org, id } }, body: { fingerprint } })),
    onSuccess: invalidate,
  });
}

// useClearBastionKey removes the jump host's pin.
export function useClearBastionKey(org: string, id: string) {
  const invalidate = useInvalidateSsh(org);
  return useMutation({
    mutationFn: () => unwrap(api.DELETE('/api/v1/orgs/{org}/bastions/{id}/host-key', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

export function useSetZoneBastion(org: string) {
  const invalidate = useInvalidateSsh(org);
  return useMutation({
    mutationFn: ({ zoneId, bastionId }: { zoneId: string; bastionId?: string }) =>
      unwrap(api.PUT('/api/v1/orgs/{org}/zones/{id}/bastion', { params: { path: { org, id: zoneId } }, body: bastionId ? { bastion_id: bastionId } : {} })),
    onSuccess: invalidate,
  });
}

export function useHostSSH(org: string, hostId: string) {
  return useQuery({
    queryKey: ['ssh', org, 'host', hostId],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/hosts/{id}/ssh', { params: { path: { org, id: hostId } } })),
    enabled: !!org && !!hostId,
  });
}

/** useSetHostSSH replaces the host's SSH settings under If-Match (a stale
 * revision answers 412) and caches the host's new revision, like
 * useSetHostTags. */
export function useSetHostSSH(org: string, hostId: string) {
  const qc = useQueryClient();
  const invalidate = useInvalidateSsh(org);
  return useMutation({
    mutationFn: ({ body, revision }: { body: HostSSHInput; revision: number }) =>
      unwrap(api.PUT('/api/v1/orgs/{org}/hosts/{id}/ssh', { params: { path: { org, id: hostId }, header: ifMatch(revision) }, body })),
    onSuccess: (res) => {
      qc.setQueryData<{ host: Host }>(['host', org, hostId], (cached) =>
        cached ? { ...cached, host: { ...cached.host, revision: res.host_revision } } : cached,
      );
      void qc.invalidateQueries({ queryKey: ['host', org, hostId] });
      invalidate();
    },
  });
}
