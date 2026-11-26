import { describe, expect, it } from 'vitest';
import type { Existing } from './existing.ts';
import { settingsChanged, type SettingsDraft } from './formDirty.ts';

const base: Existing = {
  id: 't1', name: 'deploy', revision: 1, playbookId: 'pb1', scope: { group_id: '', host_ids: ['h1'] }, limit: '', schema: undefined, vars: {},
  credentialId: 'c1', vaultIdentities: [{ key: 'k1', credentialId: 'v1', label: 'prod' }], followUps: ['t2'], agentEnrollment: false,
};

const draftOf = (b: Existing): SettingsDraft => ({
  name: b.name, playbook: { from: '', playbookId: b.playbookId, path: '' }, scope: b.scope, limit: b.limit, credentialId: b.credentialId,
  vaultRows: b.vaultIdentities, followUps: b.followUps, agentEnrollment: b.agentEnrollment,
});

describe('settingsChanged', () => {
  it('is false for the settings as loaded', () => {
    expect(settingsChanged(draftOf(base), base)).toBe(false);
  });

  it('is false for equal values in new arrays and vault rows under new keys', () => {
    const d = draftOf(base);
    expect(settingsChanged({ ...d, scope: { group_id: '', host_ids: ['h1'] }, followUps: ['t2'], vaultRows: [{ key: 'other', credentialId: 'v1', label: 'prod' }] }, base)).toBe(false);
  });

  it.each<[string, Partial<SettingsDraft>]>([
    ['the name', { name: 'deploy-2' }],
    ['the playbook', { playbook: { from: '', playbookId: 'pb2', path: '' } }],
    ['a repository playbook not registered yet', { playbook: { from: 'r1', playbookId: '', path: 'site.yml' } }],
    ['the scope', { scope: { group_id: 'g1', host_ids: [] } }],
    ['the limit', { limit: 'web*' }],
    ['the SSH credential', { credentialId: '' }],
    ['a vault identity', { vaultRows: [{ key: 'k1', credentialId: 'v1', label: 'stage' }] }],
    ['the follow-ups', { followUps: [] }],
    ['agent enrollment', { agentEnrollment: true }],
  ])('is true when %s changed', (_, change) => {
    expect(settingsChanged({ ...draftOf(base), ...change }, base)).toBe(true);
  });
});
