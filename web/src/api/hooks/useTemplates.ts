import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { TemplateInput } from '../types.ts';

function useInvalidate(org: string) {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ['templates', org] });
    void qc.invalidateQueries({ queryKey: ['jobs', org] });
  };
}

export function useTemplates(org: string) {
  return useQuery({
    queryKey: ['templates', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/job-templates', { params: { path: { org } } })),
    enabled: !!org,
  });
}

export function useTemplate(org: string, id: string) {
  return useQuery({
    queryKey: ['templates', org, id],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/job-templates/{id}', { params: { path: { org, id } } })),
    enabled: !!org && !!id,
  });
}

export function useCreateTemplate(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: TemplateInput) => unwrap(api.POST('/api/v1/orgs/{org}/job-templates', { params: { path: { org } }, body })),
    onSuccess: invalidate,
  });
}

export function useUpdateTemplate(org: string, id: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: TemplateInput) =>
      unwrap(api.PUT('/api/v1/orgs/{org}/job-templates/{id}', { params: { path: { org, id } }, body })),
    onSuccess: invalidate,
  });
}

export function useDeleteTemplate(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/job-templates/{id}', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

/** useRunTemplate starts a template's run (202, { jobs: [...] }, one per
 * involved zone sharing a run group) so the SPA can navigate to the run
 * view. */
export function useRunTemplateById(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => unwrap(api.POST('/api/v1/orgs/{org}/job-templates/{id}/run', { params: { path: { org, id } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['jobs', org] }),
  });
}
