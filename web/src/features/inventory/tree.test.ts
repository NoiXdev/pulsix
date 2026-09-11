import { describe, expect, it } from 'vitest';
import { descendantIds, orderTree } from './tree.ts';

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
});
