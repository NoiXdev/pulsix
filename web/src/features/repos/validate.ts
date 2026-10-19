/**
 * Client-side mirror of internal/repos/service.go's validation, for the
 * RepoDialog's inline preview. The messages are part of the surface — they
 * are the server's own, shown as-is (the server remains the authority; a
 * rejected save surfaces its detail at the field too).
 */
import type { RepoInput, RepoUpdateInput } from '../../api/types.ts';

const NAME_PATTERN = /^[a-z0-9][a-z0-9._-]{0,62}$/;
const URL_PATTERN = /^(https:\/\/[^/ ]+|ssh:\/\/[^/ ]+|[^@/ ]+@[^:/ ]+:)/;
const BRANCH_PATTERN = /^[A-Za-z0-9._/-]{1,100}$/;
const SUBPATH_PATTERN = /^[A-Za-z0-9._/-]{0,200}$/;

export const MAX_URL_LENGTH = 500;

export interface RepoFormValues {
  name: string;
  url: string;
  branch: string;
  subpath: string;
}

/** validateRepoInput returns one message per broken field, with the
 * server's exact wording. */
export function validateRepoInput(values: RepoFormValues): Partial<Record<keyof RepoFormValues, string>> {
  const errors: Partial<Record<keyof RepoFormValues, string>> = {};
  if (!NAME_PATTERN.test(values.name)) {
    errors.name = 'invalid name: must be lowercase letters, digits, dots, underscores or hyphens (1–63 characters), starting with a letter or digit';
  }
  errors.url = urlError(values.url);
  if (values.branch !== '' && (values.branch.includes('..') || !BRANCH_PATTERN.test(values.branch))) {
    errors.branch = 'invalid branch: may contain letters, digits, dots, slashes and hyphens (1–100 characters), no ".."';
  }
  if (values.subpath.includes('..') || !SUBPATH_PATTERN.test(values.subpath)) {
    errors.subpath = 'invalid subpath: may be empty or contain letters, digits, dots, slashes and hyphens (up to 200 characters), no ".."';
  }
  return errors;
}

function urlError(url: string): string | undefined {
  if (url === '') return 'invalid url: must be an https:// URL, an ssh:// URL or an SCP-like git URL';
  if (/\s/.test(url)) return 'invalid url: must not contain whitespace';
  if (url.length > MAX_URL_LENGTH) return `invalid url: exceeds ${MAX_URL_LENGTH} characters`;
  if (!URL_PATTERN.test(url)) return 'invalid url: must be an https:// URL, an ssh:// URL or an SCP-like git URL';
  return undefined;
}

/** repoErrorsFromServer routes a repos 400/409 onto the field the server
 * named in its detail ("invalid name: …"), the same paths the client-side
 * preview uses. */
export function repoErrorsFromServer(message: string): Partial<Record<keyof RepoFormValues, string>> {
  const errors: Partial<Record<keyof RepoFormValues, string>> = {};
  if (/invalid name|name already in use/.test(message)) errors.name = message;
  else if (/invalid url/.test(message)) errors.url = message;
  else if (/invalid branch/.test(message)) errors.branch = message;
  else if (/invalid subpath/.test(message)) errors.subpath = message;
  else if (/credential/.test(message)) errors.subpath = message;
  return errors;
}

/** RepoBodyIdentityKey is the one RepoBodyKey with no form counterpart:
 * `$schema` is the generated JSON-Schema self-pointer huma stamps on every
 * *InputBody, never a value the client sends — the only key REPO_BODY_FIELDS
 * below may leave out. */
type RepoBodyIdentityKey = '$schema';

/** RepoBodyKey is the union of RepoCreateInputBody's and
 * RepoUpdateInputBody's own fields. REPO_BODY_FIELDS is keyed off the union,
 * not off RepoInput (the Create body) alone: the two schemas are generated
 * independently and are free to diverge, and a guard anchored on Create
 * alone would compile cleanly — no missing-property error, since an
 * optional field missing from an object literal is not one — the day a
 * field is added only to RepoUpdateInputBody, leaving submit()'s PUT free
 * to silently omit it. */
type RepoBodyKey = keyof RepoInput | keyof RepoUpdateInput;

/**
 * REPO_BODY_FIELDS is RepoDialog submit()'s own compile-time guard against
 * routeToInput's class of bug (alertRoute.ts's own ROUTE_INPUT_FIELDS —
 * this copies the technique, not the contents): submit() builds its save
 * body by hand from component state rather than from a generated
 * converter, so a field added to RepoCreateInputBody/RepoUpdateInputBody
 * that nobody teaches submit() to send would silently never reach the
 * server on the next save — the exact shape that, for vault_identities,
 * also means saving an edited repository wipes whatever labelled vault
 * passwords it already had, because the PUT that omits the field deletes
 * it (RepoUpdateInputBody is a full replace, the same contract
 * AlertRouteBody's own doc comment names for routes).
 *
 * `Record<K, true>` forces exhaustiveness in both directions, over BOTH
 * bodies' fields (K is RepoBodyKey, not just keyof RepoInput): a field
 * either body gains and this literal does not list is a missing-property
 * error, and a stale key neither body has is an excess-property error — so
 * the day a new field joins either the create or the update body, this
 * line refuses to compile until RepoDialog's own submit() is updated right
 * next to it.
 */
export const REPO_BODY_FIELDS: Record<Exclude<RepoBodyKey, RepoBodyIdentityKey>, true> = {
  name: true,
  url: true,
  branch: true,
  subpath: true,
  credential_id: true,
  vault_credential_id: true,
  vault_identities: true,
};
