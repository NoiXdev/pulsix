import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { CheckDefinitionInput, MonitoringGroupInput } from '../types.ts';

/**
 * The check pipeline's organization-level surface (v0.3b-1, spec
 * docs/superpowers/specs/2026-09-27-v0.3b-checks-design.md, "API and
 * permissions"): check definitions, the monitoring groups a definition can
 * bind to, and the group editor's selector preview.
 *
 * Every write here invalidates BOTH lists, not just its own. A monitoring
 * group is not a bystander of the definitions: deleting one cascades the
 * definitions bound to it away (migration 00043's ON DELETE CASCADE), and
 * changing a group's selector changes which hosts a definition bound to it
 * reaches. Refreshing only the list that was written would leave the other
 * page showing rows the server no longer has.
 */
function useInvalidate(org: string) {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ['checks', org] });
    void qc.invalidateQueries({ queryKey: ['monitoring-groups', org] });
  };
}

export function useChecks(org: string, enabled = true) {
  return useQuery({
    queryKey: ['checks', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/checks', { params: { path: { org } } })),
    enabled: !!org && enabled,
  });
}

export function useCreateCheck(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: CheckDefinitionInput) => unwrap(api.POST('/api/v1/orgs/{org}/checks', { params: { path: { org } }, body })),
    onSuccess: invalidate,
  });
}

/** useUpdateCheck replaces a definition wholesale (PUT): the body carries
 * every field, including the key and plugin a seeded row will not let
 * change — see CheckDialog, which keeps those two read-only rather than
 * sending a value the server would refuse. */
export function useUpdateCheck(org: string, id: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: CheckDefinitionInput) => unwrap(api.PUT('/api/v1/orgs/{org}/checks/{id}', { params: { path: { org, id } }, body })),
    onSuccess: invalidate,
  });
}

export function useDeleteCheck(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/checks/{id}', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

export function useMonitoringGroups(org: string, enabled = true) {
  return useQuery({
    queryKey: ['monitoring-groups', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/monitoring-groups', { params: { path: { org } } })),
    enabled: !!org && enabled,
  });
}

export function useCreateMonitoringGroup(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: MonitoringGroupInput) => unwrap(api.POST('/api/v1/orgs/{org}/monitoring-groups', { params: { path: { org } }, body })),
    onSuccess: invalidate,
  });
}

export function useUpdateMonitoringGroup(org: string, id: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: MonitoringGroupInput) =>
      unwrap(api.PUT('/api/v1/orgs/{org}/monitoring-groups/{id}', { params: { path: { org, id } }, body })),
    onSuccess: invalidate,
  });
}

export function useDeleteMonitoringGroup(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/monitoring-groups/{id}', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

/**
 * useMonitoringGroupPreview resolves a selector without saving anything.
 *
 * `selector` is null whenever there is nothing to ask — the caller passes
 * null for an empty field and only hands over a value once its own
 * debounce has settled (see MonitoringGroupsPage). The route expands the
 * expression across the organization's whole host table, so one request
 * per keystroke would be a self-inflicted load problem, and an empty
 * selector has no answer worth a round trip at all.
 *
 * retry: false, because the interesting failure is a 400 carrying the
 * parser's own message about where the expression went wrong — retrying
 * it three times only delays showing the operator what is wrong.
 */
export function useMonitoringGroupPreview(org: string, selector: string | null) {
  return useQuery({
    queryKey: ['monitoring-group-preview', org, selector],
    queryFn: () =>
      unwrap(api.POST('/api/v1/orgs/{org}/monitoring-groups/preview', { params: { path: { org } }, body: { selector: selector! } })),
    enabled: !!org && selector !== null,
    retry: false,
  });
}
