import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { MaintenanceWindowInput } from '../types.ts';

const ifMatch = (revision: number) => ({ 'If-Match': `"${revision}"` });

function useInvalidate(org: string) {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ['maintenance-windows', org] });
    void qc.invalidateQueries({ queryKey: ['calendar', org] });
  };
}

export function useMaintenanceWindows(org: string) {
  return useQuery({
    queryKey: ['maintenance-windows', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/maintenance-windows', { params: { path: { org } } })),
    enabled: !!org,
  });
}

export function useCreateMaintenanceWindow(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: MaintenanceWindowInput) => unwrap(api.POST('/api/v1/orgs/{org}/maintenance-windows', { params: { path: { org } }, body })),
    onSuccess: invalidate,
  });
}

export function useUpdateMaintenanceWindow(org: string, id: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: ({ revision, ...body }: MaintenanceWindowInput & { revision: number }) =>
      unwrap(api.PUT('/api/v1/orgs/{org}/maintenance-windows/{id}', { params: { path: { org, id }, header: ifMatch(revision) }, body })),
    onSuccess: invalidate,
  });
}

export function useDeleteMaintenanceWindow(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/maintenance-windows/{id}', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}
