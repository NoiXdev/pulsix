import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';

function useInvalidate(org: string) {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ['agents', org] });
    void qc.invalidateQueries({ queryKey: ['agent-tokens', org] });
    void qc.invalidateQueries({ queryKey: ['host-agent', org] });
    // Revoking an agent clears the host's monitored capability, so the
    // host views the page links to are stale too.
    void qc.invalidateQueries({ queryKey: ['hosts', org] });
  };
}

/** useAgents lists the org's host agents; refreshed every 30 s — the
 * heartbeat cadence — so online and offline follow it without a reload. */
export function useAgents(org: string, enabled = true) {
  return useQuery({
    queryKey: ['agents', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/agents', { params: { path: { org } } })),
    enabled: !!org && enabled,
    refetchInterval: 30_000,
  });
}

/** useHostAgent is the host detail card's read. A host without an agent
 * answers 404, which is a state the card renders, not an error to retry. */
export function useHostAgent(org: string, hostId: string, enabled = true) {
  return useQuery({
    queryKey: ['host-agent', org, hostId],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/hosts/{id}/agent', { params: { path: { org, id: hostId } } })),
    enabled: !!org && !!hostId && enabled,
    retry: false,
    refetchInterval: 30_000,
  });
}

export function useAgentTokens(org: string, enabled = true) {
  return useQuery({
    queryKey: ['agent-tokens', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/agents/tokens', { params: { path: { org } } })),
    enabled: !!org && enabled,
  });
}

/** useCreateAgentToken returns the token once; gcTime 0 keeps it out of
 * the query cache like every other secret-bearing mutation. */
export function useCreateAgentToken(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: { zone_id: string; host_id?: string; name_hint?: string; max_uses: number; expires_in_minutes?: number }) =>
      unwrap(api.POST('/api/v1/orgs/{org}/agents/tokens', { params: { path: { org } }, body })),
    onSuccess: invalidate,
    gcTime: 0,
  });
}

export function useWithdrawAgentToken(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/agents/tokens/{id}', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

export function useRevokeAgent(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (hostId: string) => unwrap(api.POST('/api/v1/orgs/{org}/agents/{host_id}/revoke', { params: { path: { org, host_id: hostId } } })),
    onSuccess: invalidate,
  });
}
