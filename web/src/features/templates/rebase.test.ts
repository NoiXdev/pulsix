import { describe, expect, it } from 'vitest';
import { mergeScope, mergeSetting, mergeVaultRows } from './rebase.ts';
import type { VaultIdentityRow } from '../../lib/vaultIdentity.ts';

const row = (key: string, credentialId: string, label: string): VaultIdentityRow => ({ key, credentialId, label });

describe('mergeSetting', () => {
  it('keeps a changed draft and takes the current value of an unchanged one', () => {
    expect(mergeSetting('mine', 'base', 'theirs')).toBe('mine');
    expect(mergeSetting('base', 'base', 'theirs')).toBe('theirs');
  });
});

describe('mergeScope', () => {
  const base = { group_id: 'g1', host_ids: ['h1', 'h2'] };
  const now = { group_id: 'g2', host_ids: ['h3'] };

  it('reads a host unticked and ticked again (now last) as unchanged', () => {
    const draft = { group_id: 'g1', host_ids: [...base.host_ids.filter((h) => h !== 'h1'), 'h1'] };
    expect(mergeScope(draft, base, now)).toBe(now);
  });

  it('keeps a scope the person changed', () => {
    const draft = { group_id: 'g1', host_ids: ['h1'] };
    expect(mergeScope(draft, base, now)).toBe(draft);
    const regrouped = { group_id: '', host_ids: ['h1', 'h2'] };
    expect(mergeScope(regrouped, base, now)).toBe(regrouped);
  });

  it('does not reorder the arrays it compares', () => {
    const draft = { group_id: 'g1', host_ids: ['h2', 'h1'] };
    mergeScope(draft, base, now);
    expect(draft.host_ids).toEqual(['h2', 'h1']);
  });
});

describe('mergeVaultRows', () => {
  const base = [row('a', 'c1', 'prod'), row('b', 'c2', 'staging')];
  const now = [row('x', 'c3', 'extra')];

  it('reads rows with new keys but the same identities as unchanged', () => {
    expect(mergeVaultRows([row('k1', 'c1', 'prod'), row('k2', 'c2', 'staging')], base, now)).toBe(now);
  });

  it('reads a label changed and changed back as unchanged', () => {
    const draft = base.map((r) => ({ ...r }));
    draft[0] = { ...draft[0], label: 'prod-2' };
    draft[0] = { ...draft[0], label: 'prod' };
    expect(mergeVaultRows(draft, base, now)).toBe(now);
  });

  it('keeps identities the person changed', () => {
    const draft = [row('a', 'c1', 'prod')];
    expect(mergeVaultRows(draft, base, now)).toBe(draft);
  });
});
