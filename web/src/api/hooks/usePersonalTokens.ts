import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { components } from '../schema.d.ts';

/** A personal token as its owner sees it, with its grants (never the secret). */
export type PersonalToken = components['schemas']['PersonalTokenDTO'];
/** One organization a personal token is granted in, with its cap and state. */
export type PersonalGrant = components['schemas']['PersonalGrantDTO'];
/** One personal token's grant as an organization's admins see it. */
export type PersonalTokenEntry = components['schemas']['PersonalTokenEntryDTO'];
/** One grant of a create or edit: an organization's slug and the cap there. */
export type PersonalGrantInput = components['schemas']['PersonalGrantInput'];
/** What POST /auth/me/tokens takes. */
export type CreatePersonalTokenInput = Omit<components['schemas']['PersonalTokenCreateInputBody'], '$schema'>;
/** What PATCH /auth/me/tokens/{id} takes: only the keys present change;
 * `grants` replaces the owner's set (blocked grants are never listed). */
export type UpdatePersonalTokenInput = Omit<components['schemas']['PersonalTokenUpdateInputBody'], '$schema'>;

const MINE = ['personal-tokens'] as const;
const entriesKey = (org: string) => ['personal-token-entries', org] as const;
const settingKey = (org: string) => ['personal-token-setting', org] as const;

/** usePersonalTokens lists the signed-in person's personal tokens
 * (GET /auth/me/tokens, a browser session only). */
export function usePersonalTokens() {
  return useQuery({ queryKey: MINE, queryFn: () => unwrap(api.GET('/api/v1/auth/me/tokens')) });
}

/**
 * useCreatePersonalToken creates one. The answer carries the plaintext,
 * which is shown once: gcTime 0 keeps it out of the mutation cache once the
 * dialog lets go of it (the house rule for secrets, as useCreateToken).
 */
export function useCreatePersonalToken() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreatePersonalTokenInput) => unwrap(api.POST('/api/v1/auth/me/tokens', { body })),
    gcTime: 0,
    onSuccess: () => qc.invalidateQueries({ queryKey: MINE }),
  });
}

export function useUpdatePersonalToken() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: UpdatePersonalTokenInput }) =>
      unwrap(api.PATCH('/api/v1/auth/me/tokens/{id}', { params: { path: { id } }, body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: MINE }),
  });
}

export function useRevokePersonalToken() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/auth/me/tokens/{id}', { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: MINE }),
  });
}

/** useOrgPersonalTokens lists the personal tokens granted in `org`, blocked
 * ones included (GET /orgs/{org}/personal-tokens, tokens.manage). */
export function useOrgPersonalTokens(org: string) {
  return useQuery({
    queryKey: entriesKey(org),
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/personal-tokens', { params: { path: { org } } })),
    enabled: !!org,
  });
}

/** After an admin's revoke or unblock: the entries change, and so does the
 * admin's own list when the token is theirs. */
function invalidateEntries(qc: ReturnType<typeof useQueryClient>, org: string) {
  return Promise.all([qc.invalidateQueries({ queryKey: entriesKey(org) }), qc.invalidateQueries({ queryKey: MINE })]);
}

/** useRevokePersonalTokenGrant blocks one personal token in `org`: the
 * block holds the owner out of the organization until an admin lifts it. */
export function useRevokePersonalTokenGrant(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      unwrap(api.DELETE('/api/v1/orgs/{org}/personal-tokens/{id}', { params: { path: { org, id } } })),
    onSuccess: () => invalidateEntries(qc, org),
  });
}

/** useUnblockPersonalTokenOwner lifts the block on the owner of entry `id`
 * in `org` (every blocked grant of theirs there). */
export function useUnblockPersonalTokenOwner(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      unwrap(api.POST('/api/v1/orgs/{org}/personal-tokens/{id}/unblock', { params: { path: { org, id } } })),
    onSuccess: () => invalidateEntries(qc, org),
  });
}

function settingQuery(org: string) {
  return {
    queryKey: settingKey(org),
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/settings/personal-tokens', { params: { path: { org } } })),
    enabled: !!org,
  };
}

/** usePersonalTokenSetting reads the organization's switch "Allow personal
 * tokens" (org.settings.read: every member). */
export function usePersonalTokenSetting(org: string) {
  return useQuery(settingQuery(org));
}

/** usePersonalTokenSettings reads the switch of several organizations: the
 * grants editor shows the ones turned off. A slug maps to undefined while
 * its answer is pending or failed (the server still refuses a new grant). */
export function usePersonalTokenSettings(orgs: readonly string[]): Record<string, boolean | undefined> {
  const results = useQueries({ queries: orgs.map(settingQuery) });
  return Object.fromEntries(orgs.map((slug, i) => [slug, results[i]?.data?.allowed]));
}

/** useSetPersonalTokenSetting turns the switch on or off (PUT,
 * org.settings.write, a browser session only). The answer goes straight
 * into the cache; the owner's grant states change with it. */
export function useSetPersonalTokenSetting(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (allowed: boolean) =>
      unwrap(api.PUT('/api/v1/orgs/{org}/settings/personal-tokens', { params: { path: { org } }, body: { allowed } })),
    onSuccess: (data) => {
      qc.setQueryData(settingKey(org), data);
      void qc.invalidateQueries({ queryKey: MINE });
    },
  });
}
