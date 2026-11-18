import { describe, expect, it } from 'vitest';
import { ancestorsOf, descendantIds, orderTree } from './tree.ts';

const groups = [
  { id: 'php', name: 'php', parent_id: 'web' },
  { id: 'web', name: 'webserver', parent_id: 'prod' },
  { id: 'prod', name: 'production', parent_id: null },
  { id: 'db', name: 'dbserver', parent_id: 'prod' },
  { id: 'lost', name: 'lost', parent_id: 'gone' },
];

describe('tree', () => {
  it('orders depth first, children by name, dangling parents as roots', () => {
    expect(orderTree(groups).map((r) => `${r.depth}:${r.group.name}`)).toEqual([
      '0:lost', '0:production', '1:dbserver', '1:webserver', '2:php',
    ]);
  });

  it('collects descendants', () => {
    expect([...descendantIds(groups, 'prod')].sort()).toEqual(['db', 'php', 'web']);
    expect(descendantIds(groups, 'php').size).toBe(0);
  });

  it('lists the ancestors from the root down, and stops at a missing parent or a cycle', () => {
    expect(ancestorsOf(groups, 'php').map((g) => g.name)).toEqual(['production', 'webserver']);
    expect(ancestorsOf(groups, 'prod')).toEqual([]);
    expect(ancestorsOf(groups, 'lost')).toEqual([]);
    expect(ancestorsOf(groups, 'unknown')).toEqual([]);
    const cycle = [
      { id: 'a', name: 'a', parent_id: 'b' },
      { id: 'b', name: 'b', parent_id: 'a' },
    ];
    expect(ancestorsOf(cycle, 'a').map((g) => g.id)).toEqual(['b']);
  });
});
