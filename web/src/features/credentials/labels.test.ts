import { describe, expect, it } from 'vitest';
import { expiryState, kindLabel, routeLabel, usageLabel } from './labels.ts';
import { en } from '../../i18n/en/index.ts';
import { de } from '../../i18n/de/index.ts';

const base = { id: 'c', name: 'k', kind: 'ssh_key' as const, username: '', expired: false, expires_soon: false, used_by: { bastions: 0, hosts: 0, repos: 0, templates: 0 }, created_at: '', updated_at: '' };

describe('credential labels', () => {
  it('names kinds with the key type', () => {
    expect(kindLabel({ ...base, key_type: 'ed25519' }, en)).toBe('SSH key · ed25519');
    expect(kindLabel({ ...base, kind: 'git_token' }, en)).toBe('Git token');
  });

  it('summarises usage', () => {
    expect(usageLabel(base, en)).toBe('unused');
    expect(usageLabel({ ...base, used_by: { ...base.used_by, bastions: 1, hosts: 41 } }, en)).toBe('41 hosts · 1 jump host');
    // A credential only a repository holds read "unused" before the
    // count included them, and the delete that invited came back refused.
    expect(usageLabel({ ...base, used_by: { ...base.used_by, repos: 1 } }, en)).toBe('1 repository');
    expect(usageLabel({ ...base, used_by: { ...base.used_by, repos: 3 } }, en)).toBe('3 repositories');
    // A vault credential used only through a labelled job template
    // identity (no repository, no host, no jump host) read "unused" the
    // same way until the count picked up job_template_vault_credentials.
    expect(usageLabel({ ...base, used_by: { ...base.used_by, templates: 1 } }, en)).toBe('1 template');
    expect(usageLabel({ ...base, used_by: { ...base.used_by, repos: 1, templates: 2 } }, en)).toBe('1 repository · 2 templates');
  });

  it('turns expiry into a state', () => {
    expect(expiryState(base, en)).toBeNull();
    expect(expiryState({ ...base, expires_soon: true, expires_at: '2026-10-04T00:00:00Z' }, en, new Date('2026-09-11T00:00:00Z'))).toEqual({ tone: 'warn', text: 'expires in 23 d' });
    expect(expiryState({ ...base, expired: true }, en)).toEqual({ tone: 'crit', text: 'expired' });
  });

  it('explains where a jump host came from', () => {
    expect(routeLabel('group:production', en)).toBe('inherited from group production');
    expect(routeLabel('zone', en)).toBe('zone default');
    expect(routeLabel('host', en)).toBe('set on this host');
    expect(routeLabel('none', en)).toBe('direct connection');
  });
});

describe('credential labels in German', () => {
  it('agrees the usage counts and the expiry days', () => {
    expect(usageLabel({ ...base, used_by: { ...base.used_by, bastions: 1, hosts: 41 } }, de)).toBe('41 Hosts · 1 Sprunghost');
    expect(usageLabel({ ...base, used_by: { ...base.used_by, repos: 1, templates: 2 } }, de)).toBe('1 Repository · 2 Job-Vorlagen');
    expect(usageLabel(base, de)).toBe('ungenutzt');
    expect(expiryState({ ...base, expires_soon: true, expires_at: '2026-09-12T00:00:00Z' }, de, new Date('2026-09-11T00:00:00Z'))).toEqual({ tone: 'warn', text: 'läuft in 1 Tag ab' });
    expect(expiryState({ ...base, expires_soon: true, expires_at: '2026-10-04T00:00:00Z' }, de, new Date('2026-09-11T00:00:00Z'))).toEqual({ tone: 'warn', text: 'läuft in 23 Tagen ab' });
  });
});
