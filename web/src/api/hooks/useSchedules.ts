import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { ScheduleInput } from '../types.ts';

const ifMatch = (revision: number) => ({ 'If-Match': `"${revision}"` });

export interface SchedulePreviewBody {
  mode: 'cron' | 'simple';
  cron?: string;
  simple?: { kind: 'every_minutes' | 'every_hours' | 'daily' | 'weekly' | 'monthly'; every?: number; time?: string; days?: string[]; day?: number };
  timezone: string;
}

function useInvalidate(org: string) {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ['schedules', org] });
    void qc.invalidateQueries({ queryKey: ['schedule-runs', org] });
    void qc.invalidateQueries({ queryKey: ['jobs', org] });
  };
}

export function useSchedules(org: string, templateId?: string) {
  return useQuery({
    queryKey: ['schedules', org, { templateId }],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/schedules', { params: { path: { org }, query: templateId ? { template_id: templateId } : {} } })),
    enabled: !!org,
  });
}

export function useSchedule(org: string, id: string) {
  return useQuery({
    queryKey: ['schedules', org, id],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/schedules/{id}', { params: { path: { org, id } } })),
    enabled: !!org && !!id,
  });
}

export function useScheduleRuns(org: string, id: string) {
  return useQuery({
    queryKey: ['schedule-runs', org, id],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/schedules/{id}/runs', { params: { path: { org, id }, query: { limit: 50 } } })),
    enabled: !!org && !!id,
  });
}

export function useSchedulePreview(org: string, body: SchedulePreviewBody | null) {
  return useQuery({
    queryKey: ['schedule-preview', org, body],
    queryFn: () => unwrap(api.POST('/api/v1/orgs/{org}/schedules/preview', { params: { path: { org } }, body: body! })),
    enabled: !!org && body !== null,
    retry: false,
  });
}

export function useCreateSchedule(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: ScheduleInput) => unwrap(api.POST('/api/v1/orgs/{org}/schedules', { params: { path: { org } }, body })),
    onSuccess: invalidate,
  });
}

export function useUpdateSchedule(org: string, id: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: ({ revision, ...body }: ScheduleInput & { revision: number }) =>
      unwrap(api.PUT('/api/v1/orgs/{org}/schedules/{id}', { params: { path: { org, id }, header: ifMatch(revision) }, body })),
    onSuccess: invalidate,
  });
}

export function useDeleteSchedule(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/schedules/{id}', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

export function useScheduleAction(org: string, action: 'pause' | 'resume') {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) =>
      action === 'pause'
        ? unwrap(api.POST('/api/v1/orgs/{org}/schedules/{id}/pause', { params: { path: { org, id } } }))
        : unwrap(api.POST('/api/v1/orgs/{org}/schedules/{id}/resume', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

/** useRunScheduleNow fires the schedule once as the operator (202 with
 * the run's jobs, so the page can open the run view). */
export function useRunScheduleNow(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.POST('/api/v1/orgs/{org}/schedules/{id}/run', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

export function usePauseAllSchedules(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: () => unwrap(api.POST('/api/v1/orgs/{org}/schedules/pause-all', { params: { path: { org } } })),
    onSuccess: invalidate,
  });
}
