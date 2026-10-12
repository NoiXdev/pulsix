import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { AlertRouteInput } from '../types.ts';

const ifMatch = (revision: number) => ({ 'If-Match': `"${revision}"` });

function useInvalidate(org: string) {
  const qc = useQueryClient();
  return () => void qc.invalidateQueries({ queryKey: ['alert-routes', org] });
}

/** useAlertRoutes lists the organization's alert routes (v0.4b design,
 * "API and permissions"): which severities and lifecycle events reach
 * which notification channels, at org, monitoring-group or host scope. */
export function useAlertRoutes(org: string, enabled = true) {
  return useQuery({
    queryKey: ['alert-routes', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/alert-routes', { params: { path: { org } } })),
    enabled: !!org && enabled,
  });
}

export function useCreateAlertRoute(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: AlertRouteInput) => unwrap(api.POST('/api/v1/orgs/{org}/alert-routes', { params: { path: { org } }, body })),
    onSuccess: invalidate,
    gcTime: 0,
  });
}

/** useUpdateAlertRoute PATCHes one route; the server requires If-Match
 * (alert_route_handlers.go's requireIfMatch), so every call carries the
 * revision the editor loaded. The target id is a mutate-time argument
 * rather than baked in at the hook call, the same shape useDeleteChannel
 * takes its id — AlertRoutesPage reuses one instance for both the editor's
 * Save and the list's own enable/disable toggle, which can target any row
 * at any time rather than only the one route a dialog happens to have
 * open. */
export function useUpdateAlertRoute(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: ({ id, revision, ...body }: AlertRouteInput & { id: string; revision: number }) =>
      unwrap(api.PATCH('/api/v1/orgs/{org}/alert-routes/{id}', { params: { path: { org, id }, header: ifMatch(revision) }, body })),
    onSuccess: invalidate,
    gcTime: 0,
  });
}

export function useDeleteAlertRoute(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/alert-routes/{id}', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}
