import { ApiError } from '../../api/client.ts';
import type { PersonalGrantInput, PersonalToken } from '../../api/hooks/usePersonalTokens.ts';
import type { Role } from '../../lib/roles.ts';
import { roleRank } from '../members/roles.ts';

const ROLES: readonly Role[] = ['viewer', 'operator', 'admin', 'owner'];

/** An organization the person belongs to, with their role there. */
export interface Membership {
  slug: string;
  name: string;
  role: string;
}

/**
 * Why an organization's grant cannot be chosen freely: `blocked` — an
 * administrator blocked the person there (no grant can be added, changed or
 * removed by them); `off` — the organization turned personal tokens off (no
 * grant can be added or raised; an existing one may be lowered or removed).
 */
export type GrantStatus = 'open' | 'blocked' | 'off';

/** One organization of the grants editor. */
export interface GrantRow {
  slug: string;
  name: string;
  /** The person's role there: the highest cap they may choose. */
  myRole: string;
  status: GrantStatus;
  /** The token's cap there when editing a token that holds a grant. */
  existing?: Role;
  /** The caps the role select offers, lowest first. */
  options: Role[];
  /** Neither the checkbox nor the role can be changed. */
  locked: boolean;
}

/** One organization's choice in the editor. */
export interface GrantChoice {
  on: boolean;
  role: Role;
}
export type GrantDraft = Record<string, GrantChoice>;

/** A refusal the server named an organization for (409 detail). */
export type Refused = Record<string, 'blocked' | 'off'>;

/**
 * blockedSlugs is every organization where the person is blocked, as their
 * own token list shows it: a block is per person and organization, so every
 * grant of theirs there reads as blocked.
 */
export function blockedSlugs(tokens: readonly PersonalToken[]): Set<string> {
  const out = new Set<string>();
  for (const tok of tokens) for (const g of tok.grants ?? []) if (g.state === 'blocked') out.add(g.org_slug);
  return out;
}

function rolesUpTo(rank: number): Role[] {
  return ROLES.filter((r) => roleRank(r) <= rank);
}

/**
 * grantRows is the editor's model: one row per membership, in the order of
 * the memberships. `allowed` holds each organization's switch (undefined
 * while unknown); `refused` the organizations a create or edit was just
 * refused for.
 */
export function grantRows(
  memberships: readonly Membership[],
  opts: { token?: PersonalToken; blocked: ReadonlySet<string>; allowed: Readonly<Record<string, boolean | undefined>>; refused?: Refused },
): GrantRow[] {
  const held = new Map((opts.token?.grants ?? []).map((g) => [g.org_slug, g]));
  return memberships.map((m) => {
    const grant = held.get(m.slug);
    const refused = opts.refused?.[m.slug];
    const status: GrantStatus =
      opts.blocked.has(m.slug) || grant?.state === 'blocked' || refused === 'blocked'
        ? 'blocked'
        : opts.allowed[m.slug] === false || grant?.state === 'off' || refused === 'off'
          ? 'off'
          : 'open';
    const existing = grant?.role;
    const mine = roleRank(m.role);
    // An existing cap stays on offer even above a since-lowered role, so an
    // untouched grant is sent back as it is; while the switch is off a cap
    // may only go down.
    const ceiling = status === 'off' && existing ? Math.min(roleRank(existing), mine) : mine;
    const options = rolesUpTo(ceiling);
    if (existing && !options.includes(existing)) options.push(existing);
    options.sort((a, b) => roleRank(a) - roleRank(b));
    return {
      slug: m.slug,
      name: m.name,
      myRole: m.role,
      status,
      existing,
      options,
      locked: status === 'blocked' || (status === 'off' && !existing),
    };
  });
}

/** initialDraft ticks the organizations the token holds, at their caps. */
export function initialDraft(rows: readonly GrantRow[]): GrantDraft {
  return Object.fromEntries(rows.map((r) => [r.slug, { on: !!r.existing, role: r.existing ?? 'viewer' }]));
}

/**
 * grantsBody is what a create or edit sends: every ticked organization the
 * person may still change, in row order. A locked row is never sent: a
 * blocked grant (the server answers 409; it stays on the token regardless),
 * nor a new grant in an organization that turned personal tokens off, even
 * one ticked before the switch was known or a refusal said so.
 */
export function grantsBody(rows: readonly GrantRow[], draft: GrantDraft): PersonalGrantInput[] {
  return rows.filter((r) => !r.locked && draft[r.slug]?.on).map((r) => ({ org: r.slug, role: draft[r.slug].role }));
}

/** grantTicked is what a row's checkbox shows: a locked new grant reads as
 * unticked, since it will not be sent; a held one stays ticked. */
export function grantTicked(row: GrantRow, draft: GrantDraft): boolean {
  return !!draft[row.slug]?.on && (!row.locked || !!row.existing);
}

/** grantsChanged reports whether `body` differs from the token's own grants
 * (the blocked ones left out, as grantsBody leaves them out). */
export function grantsChanged(rows: readonly GrantRow[], body: readonly PersonalGrantInput[]): boolean {
  const before = rows.filter((r) => r.status !== 'blocked' && r.existing).map((r) => `${r.slug}=${r.existing}`);
  const after = body.map((g) => `${g.org}=${g.role}`);
  return before.length !== after.length || before.some((g, i) => g !== after[i]);
}

/** highestRole is the highest of the chosen caps, viewer when none: an
 * assistant token's areas are checked against it. */
export function highestRole(roles: readonly string[]): Role {
  return roles.reduce<Role>((top, r) => (roleRank(r) > roleRank(top) ? (r as Role) : top), 'viewer');
}

/** A refusal of a create or edit: the contract detail, and the organization
 * it names (`errors[0].message` on a refusal about one grant). */
export interface Refusal {
  detail: string;
  slug?: string;
}

/** refusalOf reads the server's problem answer; anything else is no refusal. */
export function refusalOf(err: unknown): Refusal | undefined {
  if (!(err instanceof ApiError) || !err.body || typeof err.body !== 'object') return undefined;
  const body = err.body as { detail?: unknown; errors?: Array<{ location?: unknown; message?: unknown }> | null };
  if (typeof body.detail !== 'string') return undefined;
  const named = (body.errors ?? []).find((e) => e.location === 'body.grants' && typeof e.message === 'string');
  return { detail: body.detail, slug: named ? (named.message as string) : undefined };
}
