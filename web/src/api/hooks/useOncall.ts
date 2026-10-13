import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { OncallExceptionInput, OncallRotationInput } from '../types.ts';

const ifMatch = (revision: number) => ({ 'If-Match': `"${revision}"` });

/** Invalidates a rotation's own roster/exceptions cache entries alongside
 * the organization's rotation list: a rotation update can change its
 * channel cycle, period or anchor, any of which changes what Roster
 * answers for a given range, and an exception create/delete changes it
 * too (oncall.Service.Roster folds exceptions into the shifts it
 * returns). react-query's invalidateQueries matches by key prefix, so
 * invalidating `['oncall-shifts', org, rotationId]` reaches every
 * from/to range already cached for this rotation without naming one. */
function useInvalidate(org: string) {
  const qc = useQueryClient();
  return {
    rotations: () => void qc.invalidateQueries({ queryKey: ['oncall-rotations', org] }),
    rotation: (rotationId: string) => {
      void qc.invalidateQueries({ queryKey: ['oncall-rotations', org] });
      void qc.invalidateQueries({ queryKey: ['oncall-shifts', org, rotationId] });
      void qc.invalidateQueries({ queryKey: ['oncall-exceptions', org, rotationId] });
    },
  };
}

/** useOncallRotations lists the organization's on-call rotations (v0.4c
 * design, "API and permissions"): an ordered channel cycle, a period and
 * an anchor each — the route-editor's rotation picker (RouteDialog's own
 * `rotations` prop) and this page's own list both read it. */
export function useOncallRotations(org: string, enabled = true) {
  return useQuery({
    queryKey: ['oncall-rotations', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/oncall-rotations', { params: { path: { org } } })),
    enabled: !!org && enabled,
  });
}

export function useCreateOncallRotation(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: OncallRotationInput) => unwrap(api.POST('/api/v1/orgs/{org}/oncall-rotations', { params: { path: { org } }, body })),
    onSuccess: invalidate.rotations,
    gcTime: 0,
  });
}

/** useUpdateOncallRotation PATCHes one rotation; the server requires
 * If-Match (oncall_handlers.go's requireIfMatch), so every call carries
 * the revision the editor loaded. The target id is a mutate-time
 * argument, the same shape useUpdateAlertRoute takes its own id in,
 * since a single instance may be reused for every row's Edit action. */
export function useUpdateOncallRotation(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: ({ id, revision, ...body }: OncallRotationInput & { id: string; revision: number }) =>
      unwrap(api.PATCH('/api/v1/orgs/{org}/oncall-rotations/{id}', { params: { path: { org, id }, header: ifMatch(revision) }, body })),
    onSuccess: (_data, vars) => invalidate.rotation(vars.id),
    gcTime: 0,
  });
}

export function useDeleteOncallRotation(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/oncall-rotations/{id}', { params: { path: { org, id } } })),
    onSuccess: (_data, id) => invalidate.rotation(id),
  });
}

/** useOncallShifts reads a rotation's roster over [from, to) — the shape
 * RosterStrip renders. `channel_name` arrives already resolved
 * (OncallShiftDTO's own comment), so no separate channel list is needed
 * just to show this. */
export function useOncallShifts(org: string, rotationId: string, from: string, to: string, enabled = true) {
  return useQuery({
    queryKey: ['oncall-shifts', org, rotationId, from, to],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/oncall-rotations/{id}/shifts', { params: { path: { org, id: rotationId }, query: { from, to } } })),
    enabled: !!org && !!rotationId && enabled,
  });
}

export function useOncallExceptions(org: string, rotationId: string, enabled = true) {
  return useQuery({
    queryKey: ['oncall-exceptions', org, rotationId],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/oncall-rotations/{id}/exceptions', { params: { path: { org, id: rotationId } } })),
    enabled: !!org && !!rotationId && enabled,
  });
}

export function useCreateOncallException(org: string, rotationId: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: OncallExceptionInput) =>
      unwrap(api.POST('/api/v1/orgs/{org}/oncall-rotations/{id}/exceptions', { params: { path: { org, id: rotationId } }, body })),
    onSuccess: () => invalidate.rotation(rotationId),
    gcTime: 0,
  });
}

export function useDeleteOncallException(org: string, rotationId: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (exceptionId: string) =>
      unwrap(api.DELETE('/api/v1/orgs/{org}/oncall-rotations/{id}/exceptions/{exceptionID}', { params: { path: { org, id: rotationId, exceptionID: exceptionId } } })),
    onSuccess: () => invalidate.rotation(rotationId),
  });
}
