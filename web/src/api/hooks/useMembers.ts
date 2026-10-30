import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';

export function useMembers(org: string) {
  return useQuery({
    queryKey: ['members', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/members', { params: { path: { org } } })),
    enabled: !!org,
  });
}

export function useInviteMember(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { email: string; role: 'admin' | 'operator' | 'viewer' }) =>
      unwrap(api.POST('/api/v1/orgs/{org}/invites', { params: { path: { org } }, body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['members', org] }),
  });
}

export function useChangeMemberRole(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ userId, role }: { userId: string; role: string }) =>
      unwrap(
        api.PATCH('/api/v1/orgs/{org}/members/{userId}', {
          params: { path: { org, userId } },
          body: { role: role as 'owner' | 'admin' | 'operator' | 'viewer' },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['members', org] }),
  });
}

export function useRemoveMember(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (userId: string) =>
      unwrap(api.DELETE('/api/v1/orgs/{org}/members/{userId}', { params: { path: { org, userId } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['members', org] }),
  });
}

/** acceptInvite redeems an invitation. display_name and password are for
 * creating the account; a signed-in account accepting for its own address
 * sends the token alone. */
export async function acceptInvite(body: { token: string; display_name?: string; password?: string }) {
  return unwrap(api.POST('/api/v1/invites/accept', { body }));
}
