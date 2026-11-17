import { describe, expect, it } from 'vitest';
import { ApiError } from '../../api/client.ts';
import type { PersonalGrant, PersonalToken } from '../../api/hooks/usePersonalTokens.ts';
import { blockedSlugs, grantRows, grantsBody, grantsChanged, grantTicked, highestRole, initialDraft, refusalOf } from './grants.ts';

const M = [
  { slug: 'acme', name: 'Acme', role: 'operator' },
  { slug: 'beta', name: 'Beta', role: 'admin' },
  { slug: 'gamma', name: 'Gamma', role: 'viewer' },
];
const tok = (grants: Array<[string, string, string]>): PersonalToken => ({
  id: 't1',
  name: 'laptop',
  kind: 'api',
  created_at: '2026-01-01T00:00:00Z',
  grants: grants.map(([slug, role, state]) => ({ org_id: slug, org_slug: slug, org_name: slug, role, state }) as PersonalGrant),
});

describe('grantRows', () => {
  it.each([
    { name: 'a new grant: up to the person’s role', token: undefined, allowed: {}, slug: 'acme', options: ['viewer', 'operator'], status: 'open', locked: false },
    { name: 'switched off, no grant: locked', token: undefined, allowed: { acme: false }, slug: 'acme', options: ['viewer', 'operator'], status: 'off', locked: true },
    { name: 'switched off, held grant: only down', token: tok([['beta', 'operator', 'off']]), allowed: {}, slug: 'beta', options: ['viewer', 'operator'], status: 'off', locked: false },
    { name: 'held cap above a lowered role stays on offer', token: tok([['gamma', 'admin', 'active']]), allowed: {}, slug: 'gamma', options: ['viewer', 'admin'], status: 'open', locked: false },
    { name: 'blocked grant: locked', token: tok([['acme', 'viewer', 'blocked']]), allowed: {}, slug: 'acme', options: ['viewer', 'operator'], status: 'blocked', locked: true },
  ])('$name', ({ token, allowed, slug, options, status, locked }) => {
    const row = grantRows(M, { token, blocked: new Set(), allowed }).find((r) => r.slug === slug);
    expect(row).toMatchObject({ options, status, locked });
  });

  it('blocks an organization the person is blocked in through another token, or a refusal', () => {
    const rows = grantRows(M, { blocked: blockedSlugs([tok([['beta', 'viewer', 'blocked']])]), allowed: {}, refused: { gamma: 'off', acme: 'blocked' } });
    expect(rows.map((r) => r.status)).toEqual(['blocked', 'blocked', 'off']);
  });
});

describe('grantsBody and grantsChanged', () => {
  const token = tok([
    ['acme', 'operator', 'active'],
    ['beta', 'viewer', 'off'],
    ['gamma', 'viewer', 'blocked'],
  ]);
  const rows = grantRows(M, { token, blocked: new Set(), allowed: { beta: false } });

  it('sends every held grant but the blocked one, unchanged', () => {
    const body = grantsBody(rows, initialDraft(rows));
    expect(body).toEqual([
      { org: 'acme', role: 'operator' },
      { org: 'beta', role: 'viewer' },
    ]);
    expect(grantsChanged(rows, body)).toBe(false);
  });

  it('never sends a new grant ticked before its organization turned out to be off', () => {
    const fresh = grantRows(M, { blocked: new Set(), allowed: {}, refused: { gamma: 'off' } });
    const draft = { acme: { on: true, role: 'viewer' as const }, gamma: { on: true, role: 'viewer' as const } };
    expect(grantsBody(fresh, draft)).toEqual([{ org: 'acme', role: 'viewer' }]);
    expect(grantTicked(fresh[2], draft)).toBe(false);
    expect(grantTicked(rows[1], initialDraft(rows))).toBe(true);
  });

  it('notices a removed or changed grant', () => {
    const draft = initialDraft(rows);
    expect(grantsChanged(rows, grantsBody(rows, { ...draft, beta: { on: false, role: 'viewer' } }))).toBe(true);
    expect(grantsChanged(rows, grantsBody(rows, { ...draft, acme: { on: true, role: 'viewer' } }))).toBe(true);
  });
});

describe('highestRole', () => {
  it.each([
    [[], 'viewer'],
    [['viewer', 'operator'], 'operator'],
    [['owner', 'admin'], 'owner'],
  ])('%j → %s', (roles, want) => expect(highestRole(roles)).toBe(want));
});

describe('refusalOf', () => {
  it('reads the detail and the organization a refusal names', () => {
    const err = new ApiError(409, { detail: 'grant_blocked', errors: [{ location: 'body.grants', message: 'beta' }] });
    expect(refusalOf(err)).toEqual({ detail: 'grant_blocked', slug: 'beta' });
    expect(refusalOf(new ApiError(409, { detail: 'personal_token_limit' }))).toEqual({ detail: 'personal_token_limit', slug: undefined });
    expect(refusalOf(new Error('network'))).toBeUndefined();
  });
});
