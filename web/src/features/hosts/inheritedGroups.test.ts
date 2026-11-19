import { describe, expect, it } from 'vitest';
import { inheritedGroups } from './inheritedGroups.ts';

const g = (id: string, parent_id?: string) => ({ id, name: id, parent_id });

describe('inheritedGroups', () => {
  it('lists ancestors of direct groups that are not direct, with the group they come through', () => {
    const all = [g('root'), g('mid', 'root'), g('leaf', 'mid'), g('other')];
    expect(inheritedGroups(all, [{ id: 'leaf', name: 'leaf' }])).toEqual([
      { id: 'mid', name: 'mid', via: 'leaf' },
      { id: 'root', name: 'root', via: 'leaf' },
    ]);
    expect(inheritedGroups(all, [{ id: 'leaf', name: 'leaf' }, { id: 'mid', name: 'mid' }]).map((x) => x.id)).toEqual(['root']);
  });

  it('survives a cycle and an unknown parent', () => {
    expect(inheritedGroups([g('a', 'b'), g('b', 'a'), g('c', 'zzz')], [{ id: 'a', name: 'a' }, { id: 'c', name: 'c' }])).toEqual([{ id: 'b', name: 'b', via: 'a' }]);
  });
});
