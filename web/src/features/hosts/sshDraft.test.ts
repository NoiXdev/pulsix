import { describe, expect, it } from 'vitest';
import { bastionChain, draftOf, draftProblem, draftRoute, sameDraft, sshBody, sshChanged, sshTarget, type SshDraft } from './sshDraft.ts';

const BASTIONS = [
  { id: 'b1', name: 'bastion-a' },
  { id: 'b2', name: 'bastion-b', via_bastion_id: 'b1' },
  { id: 'b3', name: 'loop-a', via_bastion_id: 'b4' },
  { id: 'b4', name: 'loop-b', via_bastion_id: 'b3' },
];

const SSH = { host_id: 'h1', username: 'ops', port: 2222, credential_id: 'c1', bastion_mode: 'explicit' as const, bastion_id: 'b1' };

const draft = (over: Partial<SshDraft> = {}): SshDraft => ({ ...draftOf('203.0.113.4', SSH), ...over });

describe('bastionChain', () => {
  it.each([
    ['a jump host on its own', 'b1', ['bastion-a']],
    ['one reached through another, outermost first', 'b2', ['bastion-a', 'bastion-b']],
    ['a loop, each hop once', 'b3', ['loop-b', 'loop-a']],
    ['an unknown id, by a piece of it', 'deadbeef-0000', ['deadbeef']],
  ])('%s', (_name, id, want) => {
    expect(bastionChain(id, BASTIONS)).toEqual(want);
  });
});

describe('draftRoute', () => {
  const resolved = { source: 'group:production', names: ['bastion-a'] };
  it.each([
    ['direct', { mode: 'none' as const }, { source: 'none', hops: [], approximate: false }],
    ['a specific jump host with its own chain', { mode: 'explicit' as const, bastionId: 'b2' }, { source: 'host', hops: ['bastion-a', 'bastion-b'], approximate: false }],
    ['a specific one not chosen yet', { mode: 'explicit' as const, bastionId: '' }, { source: 'host', hops: [], approximate: false }],
    ['inherit while the host inherits: the resolved route', { mode: 'inherit' as const, savedMode: 'inherit' as const, resolved }, { source: 'group:production', hops: ['bastion-a'], approximate: false }],
    ['inherit while the host does not: the zone default', { mode: 'inherit' as const, savedMode: 'none' as const, resolved, zoneBastionId: 'b2' }, { source: 'zone', hops: ['bastion-a', 'bastion-b'], approximate: true }],
    ['inherit with neither: direct, a group may still say otherwise', { mode: 'inherit' as const, savedMode: 'none' as const }, { source: 'none', hops: [], approximate: true }],
  ])('%s', (_name, input, want) => {
    expect(draftRoute({ bastionId: '', savedMode: 'explicit', bastions: BASTIONS, ...input })).toEqual(want);
  });
});

describe('sshTarget', () => {
  it.each([
    ['ops', 'web', '22', 'ops@web:22'],
    ['', 'web', 2222, 'web:2222'],
  ])('%s@%s:%s', (user, address, port, want) => {
    expect(sshTarget(user, address, port)).toBe(want);
  });
});

describe('the draft', () => {
  it('starts from the saved settings', () => {
    expect(draft()).toEqual({ address: '203.0.113.4', port: '2222', username: 'ops', credentialId: 'c1', mode: 'explicit', bastionId: 'b1' });
    expect(draftOf('', { host_id: 'h1', username: '', port: 22, bastion_mode: 'inherit' })).toEqual({
      address: '', port: '22', username: '', credentialId: '', mode: 'inherit', bastionId: '',
    });
  });

  it('builds the SSH body the way the SSH route takes it', () => {
    expect(sshBody(draft())).toEqual({ username: 'ops', port: 2222, credential_id: 'c1', bastion_mode: 'explicit', bastion_id: 'b1' });
    // A jump host left in the draft is not sent unless it is the choice.
    expect(sshBody(draft({ username: '', credentialId: '', mode: 'inherit', port: ' 22 ' }))).toEqual({ port: 22, bastion_mode: 'inherit' });
  });

  it('tells an SSH change from an address-only one', () => {
    expect(sshChanged(draft({ address: 'web-01' }), draft())).toBe(false);
    expect(sshChanged(draft({ port: '22' }), draft())).toBe(true);
    // A jump host the draft does not use is not a change.
    expect(sshChanged(draft({ mode: 'none', bastionId: 'b2' }), draft({ mode: 'none', bastionId: '' }))).toBe(false);
    expect(sameDraft(draft({ address: ' 203.0.113.4 ' }), draft())).toBe(true);
    expect(sameDraft(draft({ mode: 'inherit' }), draft())).toBe(false);
  });

  it.each([
    ['fine', {}, null],
    ['a port that is no number', { port: 'ssh' }, 'port'],
    ['a port out of range', { port: '70000' }, 'port'],
    ['a port of zero', { port: '0' }, 'port'],
    ['a specific jump host not chosen', { mode: 'explicit' as const, bastionId: '' }, 'bastion'],
  ])('finds %s', (_name, over, want) => {
    expect(draftProblem(draft(over))).toBe(want);
  });
});
