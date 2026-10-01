import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { ItemDecisionInput, ItemDecisionPreviewInput } from '../types.ts';

/**
 * The Items tab's API surface (v0.3b-3 design, "discovery and item
 * decisions"): CRUD on the decisions that keep an item from being judged,
 * and the preview a pattern editor drives before an operator has even
 * chosen ignore or watch. Same permission bar as rules and check
 * definitions (internal/api/item_handlers.go): checks.read for every
 * read — including preview, a read shaped as a POST only because the
 * pattern is a request body, the same reason /rules/validate and
 * /monitoring-groups/preview are also read-permission routes — and
 * checks.write for create/update/delete, because a decision changes what
 * is judged.
 */
function useInvalidate(org: string) {
  const qc = useQueryClient();
  return () => void qc.invalidateQueries({ queryKey: ['item-decisions', org] });
}

export function useItemDecisions(org: string, enabled = true) {
  return useQuery({
    queryKey: ['item-decisions', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/item-decisions', { params: { path: { org } } })),
    enabled: !!org && enabled,
  });
}

export function useCreateItemDecision(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: ItemDecisionInput) => unwrap(api.POST('/api/v1/orgs/{org}/item-decisions', { params: { path: { org } }, body })),
    onSuccess: invalidate,
  });
}

export function useUpdateItemDecision(org: string, id: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: ItemDecisionInput) =>
      unwrap(api.PUT('/api/v1/orgs/{org}/item-decisions/{id}', { params: { path: { org, id } }, body })),
    onSuccess: invalidate,
  });
}

export function useDeleteItemDecision(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/item-decisions/{id}', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

/**
 * useItemDecisionPreview resolves what a pattern would currently match,
 * without storing anything (POST /item-decisions/preview).
 *
 * `body` is null whenever there is nothing worth asking yet — the caller
 * (PatternPreview in ItemDecisionDialog.tsx) hands over a value only once
 * scope, host (when scope is host), check key, match kind and a non-empty
 * pattern are all set, on its own debounce of the pattern text. It never
 * carries a `decision` field, not even a placeholder one: the service
 * validates only the fields that decide what a pattern matches
 * (items.Service.Preview's own doc comment, internal/items/service.go) and
 * ignores verdict and note entirely, so there is nothing for either to
 * contribute here.
 *
 * retry: false, for the same reason useValidateRule and
 * useMonitoringGroupPreview both give theirs: the interesting failure is a
 * 422 carrying the service's own message (an exact pattern containing "*",
 * say — items.Service.validateMatch), and retrying it three times only
 * delays showing the operator what is wrong.
 */
export function useItemDecisionPreview(org: string, body: ItemDecisionPreviewInput | null) {
  return useQuery({
    queryKey: ['item-decision-preview', org, body],
    queryFn: () => unwrap(api.POST('/api/v1/orgs/{org}/item-decisions/preview', { params: { path: { org } }, body: body! })),
    enabled: !!org && body !== null,
    retry: false,
  });
}

/**
 * useHostItems reads one host's discovered items (GET /hosts/{id}/items):
 * every (check_key, item) this host has ever reported, each with its
 * effective verdict and, when a decision governs it, that decision's own
 * scope, pattern and note.
 *
 * Two of this milestone's GUI pieces consume it, independently of the
 * org-wide Items tab's own `useItemDecisions`: EffectiveRulesPanel reads
 * an unobserved entry's own last-reported date off it, and RuleDialog's
 * item field offers it as a picker's options. GET /items
 * (organization-wide) stays unimplemented here — nothing in this
 * milestone's GUI needs it yet.
 *
 * HostChecksCard deliberately does NOT: its rows read `ignored` and
 * `decided_by` off GET /hosts/{id}/checks, where one server-side
 * items.Decide call resolves both. Matching a row against THIS list
 * instead made one question two independently-timed reads of two tables,
 * and a row whose own verdict and origin disagreed sent the Ignore/Watch
 * button down the POST path, creating a duplicate decision that loses the
 * older-created_at tie-break and dies silently. Do not reintroduce that
 * lookup.
 *
 * Not polled, like useHostEffectiveRules: an item's discovery and its
 * decisions change on an operator's own action (this tab, or the row
 * action), not on the agent's heartbeat.
 */
export function useHostItems(org: string, hostId: string, enabled = true) {
  return useQuery({
    queryKey: ['host-items', org, hostId],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/hosts/{id}/items', { params: { path: { org, id: hostId } } })),
    enabled: !!org && !!hostId && enabled,
  });
}
