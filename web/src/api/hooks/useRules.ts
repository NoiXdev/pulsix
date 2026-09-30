import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { RuleInput } from '../types.ts';

/**
 * The Rules tab's API surface (v0.3b-2 spec): rule CRUD, the read-only
 * metric catalogue the editor's picker draws from, and the validate-only
 * route the expression editor drives on a debounce. Same permissions as
 * check definitions (checks.read / checks.write, internal/api/
 * rule_handlers.go) — a rule is part of operating the host it watches, not
 * a separate concern with its own bar.
 */
function useInvalidate(org: string) {
  const qc = useQueryClient();
  return () => void qc.invalidateQueries({ queryKey: ['rules', org] });
}

export function useRules(org: string, enabled = true) {
  return useQuery({
    queryKey: ['rules', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/rules', { params: { path: { org } } })),
    enabled: !!org && enabled,
  });
}

/** useRuleMetrics reads the metric catalogue GET /rules/metrics serves —
 * the only honest source for the editor's metric picker. Hardcoding this
 * list in the SPA would silently drift from internal/rules.Catalogue() the
 * moment a plugin gained a metric. */
export function useRuleMetrics(org: string, enabled = true) {
  return useQuery({
    queryKey: ['rule-metrics', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/rules/metrics', { params: { path: { org } } })),
    enabled: !!org && enabled,
  });
}

export function useCreateRule(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: RuleInput) => unwrap(api.POST('/api/v1/orgs/{org}/rules', { params: { path: { org } }, body })),
    onSuccess: invalidate,
  });
}

export function useUpdateRule(org: string, id: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: RuleInput) => unwrap(api.PUT('/api/v1/orgs/{org}/rules/{id}', { params: { path: { org, id } }, body })),
    onSuccess: invalidate,
  });
}

export function useDeleteRule(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/rules/{id}', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

/**
 * useValidateRule checks a prospective rule body without saving it
 * (POST /rules/validate), for the expression editor's live feedback.
 * `body` is null while there is nothing worth asking yet — the caller
 * (RuleExpression) hands over a value only once its own 400ms debounce has
 * settled, the same null-guard useMonitoringGroupPreview uses for its
 * selector preview.
 *
 * A rule that fails to compile answers 422, not a 200 with `ok: false`
 * (internal/api/rule_handlers.go's mapRulesErr; see rule_handlers_test.go's
 * "validate never persists" subtest) — so the caller reads validity off
 * `isSuccess`/`isError`, not off a body field, and the offending position
 * comes out of the thrown ApiError's own message text ("... (at byte N)"),
 * not a separate response field.
 *
 * retry: false, for the same reason as that preview: the interesting
 * failure is a 4xx carrying the parser's own message, and retrying it
 * three times only delays showing the operator what is wrong.
 */
export function useValidateRule(org: string, body: RuleInput | null) {
  return useQuery({
    queryKey: ['rule-validate', org, body],
    queryFn: () => unwrap(api.POST('/api/v1/orgs/{org}/rules/validate', { params: { path: { org } }, body: body! })),
    enabled: !!org && body !== null,
    retry: false,
  });
}

/**
 * useCheckRule is the same validate-only route as an imperative call
 * rather than a live query — used once, not on a debounce, to render the
 * expression text a form-mode rule's current fields compile to when an
 * operator switches the editor from form to expression (RuleDialog). It
 * persists nothing, exactly like useValidateRule.
 */
export function useCheckRule(org: string) {
  return useMutation({
    mutationFn: (body: RuleInput) => unwrap(api.POST('/api/v1/orgs/{org}/rules/validate', { params: { path: { org } }, body })),
  });
}
