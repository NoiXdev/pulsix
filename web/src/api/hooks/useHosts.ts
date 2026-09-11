import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { HostInput } from '../types.ts';

export interface HostListFilter {
  zone?: string;
  managed?: boolean;
  monitored?: boolean;
  q?: string;
}

export function useHosts(org: string, filter: HostListFilter = {}) {
  return useQuery({
    queryKey: ['hosts', org, filter],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/orgs/{org}/hosts', {
          params: {
            path: { org },
            query: {
              zone: filter.zone ?? '',
              managed: filter.managed === undefined ? '' : String(filter.managed),
              monitored: filter.monitored === undefined ? '' : String(filter.monitored),
              q: filter.q ?? '',
            },
          },
        }),
      ),
    enabled: !!org,
  });
}

export function useHost(org: string, id: string) {
  return useQuery({
    queryKey: ['host', org, id],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/hosts/{id}', { params: { path: { org, id } } })),
    enabled: !!org && !!id,
  });
}

export function useCreateHost(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: HostInput) => unwrap(api.POST('/api/v1/orgs/{org}/hosts', { params: { path: { org } }, body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['hosts', org] }),
  });
}

export function useSetHostCapabilities(org: string, id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (patch: { managed?: boolean; monitored?: boolean }) =>
      unwrap(api.PATCH('/api/v1/orgs/{org}/hosts/{id}/capabilities', { params: { path: { org, id } }, body: patch })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['hosts', org] });
      void qc.invalidateQueries({ queryKey: ['host', org, id] });
    },
  });
}

export function useDeleteHost(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/hosts/{id}', { params: { path: { org, id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['hosts', org] }),
  });
}

export function useZones(org: string) {
  return useQuery({
    queryKey: ['zones', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/zones', { params: { path: { org } } })),
    enabled: !!org,
  });
}

export function useCreateZone(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { slug: string; name: string }) =>
      unwrap(api.POST('/api/v1/orgs/{org}/zones', { params: { path: { org } }, body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['zones', org] }),
  });
}
