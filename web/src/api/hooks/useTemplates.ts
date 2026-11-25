import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import { ifMatch } from '../ifMatch.ts';
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

/** useUpdateTemplate saves a template's settings under If-Match (its
 * revision, quoted like the ETag): a stale one answers 412, an ApiError
 * with that status — an automatic variables sync moves the revision too. */
export function useUpdateTemplate(org: string, id: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: ({ body, revision }: { body: TemplateInput; revision: number }) =>
      unwrap(api.PUT('/api/v1/orgs/{org}/job-templates/{id}', { params: { path: { org, id }, header: ifMatch(revision) }, body })),
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

/** fetchTemplateEnrollment reads what a run of the template started now
 * enrols the agent on (spec 2026-11-25 B): the person confirms it before
 * the start, which sends the hosts' ids back as `enrollHostIds`. */
export function fetchTemplateEnrollment(org: string, id: string, hostId?: string) {
  return unwrap(
    api.GET('/api/v1/orgs/{org}/job-templates/{id}/enrollment-preview', {
      params: { path: { org, id }, query: hostId ? { host_id: hostId } : {} },
    }),
  );
}

/** useRunTemplateById starts a template's run (202, { jobs: [...] }, one
 * per involved zone sharing a run group) so the SPA can navigate to the run
 * view. With `hostId` the run is narrowed to that one host; the field is
 * left out of the body when unset, because the server refuses an empty
 * `host_id`. `followUps` replaces the template's own follow-ups for this
 * run (an empty list starts none); left undefined, the template's list
 * applies and the field is not sent. */
export function useRunTemplateById(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, hostId, followUps, enrollHostIds }: { id: string; hostId?: string; followUps?: string[]; enrollHostIds?: string[] }) =>
      unwrap(
        api.POST('/api/v1/orgs/{org}/job-templates/{id}/run', {
          params: { path: { org, id } },
          body:
            hostId || followUps || enrollHostIds
              ? { host_id: hostId, follow_ups: followUps, enroll_host_ids: enrollHostIds }
              : undefined,
        }),
      ),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['jobs', org] }),
  });
}
