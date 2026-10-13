import { describe, expect, it } from 'vitest';
import { expiryState, kindLabel, routeLabel, usageLabel } from './labels.ts';

const base = { id: 'c', name: 'k', kind: 'ssh_key' as const, username: '', expired: false, expires_soon: false, used_by: { bastions: 0, hosts: 0, repos: 0 }, created_at: '', updated_at: '' };

describe('credential labels', () => {
  it('names kinds with the key type', () => {
    expect(kindLabel({ ...base, key_type: 'ed25519' })).toBe('SSH key · ed25519');
    expect(kindLabel({ ...base, kind: 'git_token' })).toBe('Git token');
  });

  it('summarises usage', () => {
    expect(usageLabel(base)).toBe('unused');
    expect(usageLabel({ ...base, used_by: { bastions: 1, hosts: 41, repos: 0 } })).toBe('41 hosts · 1 jump host');
    // A credential only a repository holds read "unused" before the
    // count included them, and the delete that invited came back refused.
    expect(usageLabel({ ...base, used_by: { bastions: 0, hosts: 0, repos: 1 } })).toBe('1 repository');
    expect(usageLabel({ ...base, used_by: { bastions: 0, hosts: 0, repos: 3 } })).toBe('3 repositories');
  });

  it('turns expiry into a state', () => {
    expect(expiryState(base)).toBeNull();
    expect(expiryState({ ...base, expires_soon: true, expires_at: '2026-10-04T00:00:00Z' }, new Date('2026-09-11T00:00:00Z'))).toEqual({ tone: 'warn', text: 'expires in 23 d' });
    expect(expiryState({ ...base, expired: true })).toEqual({ tone: 'crit', text: 'expired' });
  });

  it('explains where a jump host came from', () => {
    expect(routeLabel('group:production')).toBe('inherited from group production');
    expect(routeLabel('zone')).toBe('zone default');
    expect(routeLabel('host')).toBe('set on this host');
    expect(routeLabel('none')).toBe('direct connection');
  });
});
