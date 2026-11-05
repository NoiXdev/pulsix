import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { paths } from '../schema.d.ts';
import type { VarSchema, Vars } from '../../features/variables/types.ts';

type VariablesPath = paths['/api/v1/orgs/{org}/job-templates/{id}/variables'];
type UpdateBody = VariablesPath['put']['requestBody']['content']['application/json'];
type ApplyBody = paths['/api/v1/orgs/{org}/job-templates/{id}/variables/apply']['post']['requestBody']['content']['application/json'];

/** The template's revision as If-Match, quoted like its ETag. */
const ifMatch = (revision: number) => ({ 'If-Match': `"${revision}"` });

/** A write of a template's variables moves its revision and what the list
 * shows about it (review badges), so every template query is refreshed. */
function useInvalidate(org: string) {
  const qc = useQueryClient();
  return () => qc.invalidateQueries({ queryKey: ['templates', org] });
}

/** useTemplateVariables reads a template's variables page: schema, masked
 * values, revision, mode, the playbook's variable sources as last read and
 * what is left to review (backlog 7.75). */
export function useTemplateVariables(org: string, id: string) {
  return useQuery({
    queryKey: ['templates', org, id, 'variables'],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/job-templates/{id}/variables', { params: { path: { org, id } } })),
    enabled: !!org && !!id,
  });
}

export interface VariablesWrite {
  /** Absent removes the schema (and every value kept for an inactive or
   * removed field). */
  schema?: VarSchema;
  vars: Vars;
  revision: number;
}

/** useUpdateTemplateVariables replaces the schema and values under
 * If-Match: a stale revision answers 412 (an ApiError with that status). */
export function useUpdateTemplateVariables(org: string, id: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: ({ schema, vars, revision }: VariablesWrite) =>
      unwrap(
        api.PUT('/api/v1/orgs/{org}/job-templates/{id}/variables', {
          params: { path: { org, id }, header: ifMatch(revision) },
          body: { extra_vars_schema: schema, extra_vars: vars } as UpdateBody,
        }),
      ),
    onSuccess: invalidate,
  });
}

/** useSetVariablesMode switches a linked template between manual and
 * automatic, or unlinks it (off). Linking goes through the preview and
 * useApplyTemplateVariables, never through this. */
export function useSetVariablesMode(org: string, id: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: ({ mode, revision }: { mode: 'off' | 'manual' | 'automatic'; revision: number }) =>
      unwrap(
        api.PUT('/api/v1/orgs/{org}/job-templates/{id}/variables/mode', {
          params: { path: { org, id }, header: ifMatch(revision) },
          body: { mode },
        }),
      ),
    onSuccess: invalidate,
  });
}

/** usePreviewTemplateVariables asks what an apply of the playbook's
 * variable sources would change ("Sync now"). It saves nothing; a POST
 * only because the server computes it anew each time. */
export function usePreviewTemplateVariables(org: string, id: string) {
  return useMutation({
    mutationFn: () => unwrap(api.POST('/api/v1/orgs/{org}/job-templates/{id}/variables/preview', { params: { path: { org, id } } })),
  });
}

export interface ApplyWrite {
  mode: 'manual' | 'automatic';
  /** The preview's source_rev. */
  sourceRev: string;
  /** The preview's source.synced_at exactly as the server sent it: the
   * server compares it to the microsecond, which a JS Date would cut to
   * milliseconds (409). Absent when the preview had none. */
  sourceSyncedAt?: string;
  removeGone: boolean;
  /** Values entered in the preview; omitted when none. */
  values?: Vars;
  revision: number;
}

/** useApplyTemplateVariables applies a preview: 409 when the source
 * changed since, 412 when the template did, 422 at body.values.<name> when
 * a value must be entered again. */
export function useApplyTemplateVariables(org: string, id: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: ({ mode, sourceRev, sourceSyncedAt, removeGone, values, revision }: ApplyWrite) => {
      const body: ApplyBody = { mode, source_rev: sourceRev, remove_gone: removeGone };
      if (sourceSyncedAt !== undefined) body.source_synced_at = sourceSyncedAt;
      if (values && Object.keys(values).length > 0) body.values = values;
      return unwrap(
        api.POST('/api/v1/orgs/{org}/job-templates/{id}/variables/apply', {
          params: { path: { org, id }, header: ifMatch(revision) },
          body,
        }),
      );
    },
    onSuccess: invalidate,
  });
}
