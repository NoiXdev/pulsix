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
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['members', org] });
      void qc.invalidateQueries({ queryKey: ['invites', org] });
    },
  });
}

/** usePendingInvites lists the organization's open invitations (backlog
 * 7.29). One whose sender may no longer add members is not open and is not
 * listed, which is why a role change or a removal refreshes it too. */
export function usePendingInvites(org: string) {
  return useQuery({
    queryKey: ['invites', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/invites', { params: { path: { org } } })),
    enabled: !!org,
  });
}

export function useRevokeInvite(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (inviteId: string) =>
      unwrap(api.DELETE('/api/v1/orgs/{org}/invites/{inviteId}', { params: { path: { org, inviteId } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['invites', org] }),
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
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['members', org] });
      void qc.invalidateQueries({ queryKey: ['invites', org] });
    },
  });
}

export function useRemoveMember(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (userId: string) =>
      unwrap(api.DELETE('/api/v1/orgs/{org}/members/{userId}', { params: { path: { org, userId } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['members', org] });
      void qc.invalidateQueries({ queryKey: ['invites', org] });
    },
  });
}

/** acceptInvite redeems an invitation. display_name and password are for
 * creating the account; a signed-in account accepting for its own address
 * sends the token alone. */
export async function acceptInvite(body: { token: string; display_name?: string; password?: string }) {
  return unwrap(api.POST('/api/v1/invites/accept', { body }));
}
