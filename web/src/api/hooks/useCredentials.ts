import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';

export interface CredentialInput {
  name: string;
  username?: string;
  expires_at?: string;
  generate?: boolean;
  private_key?: string;
  passphrase?: string;
  password?: string;
  token?: string;
}

/** Every credential write refreshes the credentials, the SSH settings that
 * reference them, and the inventory: a credential's username is the
 * preview's ansible_user wherever a host names none. */
function useInvalidate(org: string) {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ['credentials', org] });
    void qc.invalidateQueries({ queryKey: ['ssh', org] });
    void qc.invalidateQueries({ queryKey: ['inventory', org] });
  };
}

export function useCredentials(org: string, opts: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: ['credentials', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/credentials', { params: { path: { org } } })),
    enabled: !!org && (opts.enabled ?? true),
  });
}

export function useCredential(org: string, id: string) {
  return useQuery({
    queryKey: ['credentials', org, id],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/credentials/{id}', { params: { path: { org, id } } })),
    enabled: !!org && !!id,
  });
}

/** gcTime: 0 — the variables carry private keys, passwords or tokens. The
 * dialogs also call reset() once the result has been shown. */
export function useCreateCredential(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    // The kind union is written out rather than taken from the schema
    // because CredentialInput's own kind is optional; keeping it here
    // means a kind added to the server without being added here is a
    // type error rather than a request the server quietly refuses.
    mutationFn: (body: CredentialInput & { kind: 'ssh_key' | 'ssh_password' | 'git_key' | 'git_token' | 'vault_password' }) =>
      unwrap(api.POST('/api/v1/orgs/{org}/credentials', { params: { path: { org } }, body })),
    gcTime: 0,
    onSuccess: invalidate,
  });
}

export function useUpdateCredential(org: string, id: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: CredentialInput) => unwrap(api.PATCH('/api/v1/orgs/{org}/credentials/{id}', { params: { path: { org, id } }, body })),
    gcTime: 0,
    onSuccess: invalidate,
  });
}

export function useDeleteCredential(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/credentials/{id}', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

/** useRotateCredential re-keys an ssh_key credential (202, { job_id,
 * job_count }): job_count 0 means nothing referenced it and the new
 * fingerprint is already live; otherwise the rotation group runs first and
 * the fingerprint changes only when its jobs succeed. */
export function useRotateCredential(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.POST('/api/v1/orgs/{org}/credentials/{id}/rotate', { params: { path: { org, id } } })),
    gcTime: 0,
    onSuccess: invalidate,
  });
}
