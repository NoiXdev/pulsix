import { describe, expect, it } from 'vitest';
import { filterGroups, groupPath, validateGroupsSearch } from './groupsSearch.ts';

const g = (id: string, name: string, parent_id: string | undefined, extra = {}) => ({
  id, name, description: '', parent_id, members: 0, group_fields: 0, host_fields: 0, complete: true, revision: 1, ...extra,
});

const ALL = [
  g('a', 'all_servers', undefined),
  g('w', 'web', 'a', { bastion_id: 'b1', description: 'Front tier' }),
  g('e', 'web_eu', 'w', { complete: false }),
  g('d', 'db', undefined, { complete: false }),
];

const names = (f: Parameters<typeof filterGroups>[2]) => filterGroups(ALL, ALL, f).map((r) => r.name);

describe('filterGroups', () => {
  it.each([
    ['no filter', {}, ['all_servers', 'web', 'web_eu', 'db']],
    ['a search over the name', { q: 'web' }, ['web', 'web_eu']],
    ['a search over the description', { q: 'front' }, ['web']],
    ['a jump host of its own', { jump: true as const }, ['web']],
    ['every group below a parent', { parent: 'a' }, ['web', 'web_eu']],
    ['a parent without children', { parent: 'd' }, []],
    ['incomplete values', { values: 'incomplete' as const }, ['web_eu', 'db']],
    ['complete values', { values: 'complete' as const }, ['all_servers', 'web']],
    ['several at once', { parent: 'a', values: 'incomplete' as const }, ['web_eu']],
  ])('%s', (_, f, want) => {
    expect(names(f)).toEqual(want);
  });
});

describe('groupPath', () => {
  it('names the groups above, top first', () => {
    expect(groupPath(ALL, 'e')).toEqual(['all_servers', 'web']);
    expect(groupPath(ALL, 'a')).toEqual([]);
  });
});

describe('validateGroupsSearch', () => {
  it('keeps the known filters next to sort and page, and drops the rest', () => {
    expect(validateGroupsSearch({ q: 'web', jump: true, parent: 'a', values: 'complete', sort: 'name', order: 'asc', page: 2, junk: 1 })).toEqual({
      q: 'web', jump: true, parent: 'a', values: 'complete', sort: 'name', order: 'asc', page: 2,
    });
    expect(validateGroupsSearch({ values: 'maybe', jump: 'no' })).toEqual({});
  });
});
