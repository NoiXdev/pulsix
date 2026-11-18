/// <reference types="node" />
// The golden inventory is read from disk at run time rather than imported:
// it lives in the Go package (internal/inventory/render/testdata), outside
// web/, and the image's SPA build stage has only web/ — a static import
// would fail `tsc -b` there (see paramFormats.test.ts).
import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { groupHosts, groupInheritance, hostInheritance, inventoryHas, type Inheritance } from './inheritance.ts';
import type { SchemaField } from '../variables/types.ts';

type Doc = Record<string, unknown>;

const GOLDEN = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../internal/inventory/render/testdata/inventory.golden.json');
const golden = (JSON.parse(readFileSync(GOLDEN, 'utf8')) as { inventory: Doc }).inventory;

/**
 * ansibleVars is an independent reading of how Ansible turns a dynamic
 * inventory into one host's variables: every group the host is in, with
 * every group above them, sorted by depth (all is 0, its children 1, …) and
 * then by name (no group can carry ansible_group_priority: the schema
 * reserves the ansible_ prefix), merged weakest first, then the hostvars on
 * top. It reads only the document (children links), never the helpers
 * under test. `host` null merges a host in `groups` and nowhere else,
 * without hostvars: what the group view must agree with.
 */
function ansibleVars(doc: Doc, groups: string[], host: string | null): Record<string, unknown> {
  const entry = (g: string) => (doc[g] ?? {}) as { vars?: Record<string, unknown>; children?: string[]; hosts?: string[] };
  const depth = new Map<string, number>();
  const visit = (g: string, d: number) => {
    if ((depth.get(g) ?? -1) >= d) return;
    depth.set(g, d);
    for (const c of entry(g).children ?? []) visit(c, d + 1);
  };
  visit('all', 0);
  const parentsOf = (g: string) => Object.keys(doc).filter((p) => (entry(p).children ?? []).includes(g));
  const set = new Set<string>();
  const add = (g: string) => {
    if (set.has(g)) return;
    set.add(g);
    parentsOf(g).forEach(add);
  };
  const direct = host === null ? groups : Object.keys(doc).filter((g) => g !== '_meta' && (entry(g).hosts ?? []).includes(host));
  direct.forEach(add);
  const order = [...set].sort((a, b) => (depth.get(a)! - depth.get(b)!) || (a < b ? -1 : a > b ? 1 : 0));
  const out: Record<string, unknown> = {};
  for (const g of order) Object.assign(out, entry(g).vars ?? {});
  if (host !== null) Object.assign(out, ((doc._meta as { hostvars: Record<string, Record<string, unknown>> }).hostvars ?? {})[host] ?? {});
  return out;
}

/** winners is what a table says each variable ends up as. */
function winners(inh: Inheritance): Record<string, unknown> {
  return Object.fromEntries(inh.rows.map((r) => [r.key, r.cells[r.winner].value]));
}

const GROUPS_IN = (doc: Doc) => Object.keys(doc).filter((k) => !['_meta', 'all', 'ungrouped'].includes(k));

describe('the inheritance table against the server-rendered golden inventory', () => {
  it('reads a real inventory', () => {
    expect(GROUPS_IN(golden).length).toBeGreaterThan(3);
    expect(inventoryHas(golden, 'webserver')).toBe(true);
    expect(inventoryHas(golden, 'nope')).toBe(false);
  });

  it.each(GROUPS_IN(golden))('group view of %s: every winner is what a host in it alone would see, minus hostvars', (g) => {
    const inh = groupInheritance(golden, g, { fields: [], stored: {} });
    expect(winners(inh)).toEqual(ansibleVars(golden, [g], null));
  });

  const pairs = GROUPS_IN(golden).flatMap((g) => groupHosts(golden, g).map((h) => [g, h.name] as const));
  it('has hosts below groups to check', () => expect(pairs.length).toBeGreaterThan(4));

  it.each(pairs)('host view of %s / %s: every winner is the host\'s final value', (g, host) => {
    const inh = hostInheritance(golden, g, host, { fields: [], stored: {} });
    expect(winners(inh)).toEqual(ansibleVars(golden, [], host));
  });

  it('web-01 in webserver: its own hostvar beats the group, production stays an ancestor', () => {
    const inh = hostInheritance(golden, 'webserver', 'web-01', { fields: [], stored: {} });
    expect(inh.levels.map((l) => `${l.kind}:${l.name}:${l.role ?? ''}`)).toEqual([
      'group:production:ancestor', 'group:webserver:self', 'host:web-01:',
    ]);
    const php = inh.rows.find((r) => r.key === 'php_default')!;
    expect(php.cells.map((c) => c.state)).toEqual(['unset', 'lost', 'win']);
    expect(php.cells[1].value).toBe('8.3');
    expect(php.cells[2].value).toBe('8.4');
    expect(inh.rows.find((r) => r.key === 'env')!.cells.map((c) => c.state)).toEqual(['win', 'unset', 'unset']);
    // Masked secrets stay masked.
    const vhosts = inh.rows.find((r) => r.key === 'vhosts')!;
    expect(JSON.stringify(vhosts.cells[2].value)).toContain('********');
  });
});

describe('groupInheritance', () => {
  const doc: Doc = {
    all: { children: ['all_servers'] },
    all_servers: { vars: { env: 'staging', ntp: 'a', level: 'debug' }, children: ['web'] },
    web: { vars: { env: 'prod', level: 'info', region: 'eu' } },
    _meta: { hostvars: {} },
  };
  const fields: SchemaField[] = [
    { name: 'env', type: 'string' },
    { name: 'level', type: 'enum', options: ['info', 'debug'], default: 'info' },
    { name: 'region', type: 'string', default: 'us' },
    { name: 'domain', type: 'string', scope: 'host', default: 'x' },
    { name: 'old', type: 'string', default: 'gone', active: false },
  ];

  it('puts the schema default at the group\'s own level, above its ancestors, as the renderer does', () => {
    const inh = groupInheritance(doc, 'web', { fields, stored: { env: 'prod', region: 'eu' } });
    expect(inh.levels.map((l) => `${l.kind}:${l.name}`)).toEqual(['group:all_servers', 'default:web', 'group:web']);
    const by = (k: string) => inh.rows.find((r) => r.key === k)!;
    expect(inh.rows.map((r) => r.key)).toEqual(['env', 'level', 'ntp', 'region']);
    // Unset here, so the rendered default beats the ancestor's value.
    expect(by('level').cells.map((c) => c.state)).toEqual(['lost', 'win', 'unset']);
    expect(by('level').cells[1].value).toBe('info');
    // Set here: the group's value beats its own default.
    expect(by('region').cells.map((c) => [c.state, c.value])).toEqual([['unset', undefined], ['lost', 'us'], ['win', 'eu']]);
    expect(by('env').cells.map((c) => c.state)).toEqual(['lost', 'unset', 'win']);
    expect(by('ntp').cells.map((c) => c.state)).toEqual(['win', 'unset', 'unset']);
    // Neither a host-scoped nor an inactive field's default renders at the group.
    expect(inh.rows.find((r) => r.key === 'domain' || r.key === 'old')).toBeUndefined();
  });

  it('shows the all level only when the inventory sets vars there', () => {
    const withAll = { ...doc, all: { children: ['all_servers'], vars: { ntp: 'z', tz: 'UTC' } } };
    const inh = groupInheritance(withAll, 'web', { fields: [], stored: {} });
    expect(inh.levels[0]).toMatchObject({ kind: 'all', name: 'all' });
    expect(winners(inh)).toEqual(ansibleVars(withAll, ['web'], null));
  });

  it('omits the default column when the schema has no defaults', () => {
    const inh = groupInheritance(doc, 'web', { fields: [{ name: 'env', type: 'string' }], stored: {} });
    expect(inh.levels.map((l) => l.kind)).toEqual(['group', 'group']);
  });
});

describe('hostInheritance', () => {
  // db-1 is in web_eu (below web) and in backup, a group of its own at the
  // same depth as web: both set `port`, and backup wins because it sorts
  // after web — and loses to web_eu, one level deeper.
  const doc: Doc = {
    all: { children: ['backup', 'web', 'zz_monitor'] },
    backup: { vars: { port: 1, schedule: 'daily', owner: 'ops' }, hosts: ['db-1'] },
    web: { vars: { port: 2, owner: 'web' }, children: ['web_eu'] },
    web_eu: { vars: { region: 'eu' }, hosts: ['db-1', 'db-2'] },
    zz_monitor: { vars: { owner: 'mon' }, hosts: ['db-1'] },
    _meta: { hostvars: { 'db-1': { ansible_host: '10.0.0.1', region: 'eu-west' }, 'db-2': { ansible_host: 'db-2' } } },
  };

  it('orders every group the host inherits from by depth, then name, and marks where each sits', () => {
    const inh = hostInheritance(doc, 'web', 'db-1', { fields: [], stored: {} });
    expect(inh.levels.map((l) => `${l.kind}:${l.name}:${l.role ?? ''}`)).toEqual([
      'group:backup:other', 'group:web:self', 'group:zz_monitor:other', 'group:web_eu:descendant', 'host:db-1:',
    ]);
    expect(winners(inh)).toEqual(ansibleVars(doc, [], 'db-1'));
  });

  it('names the same-depth groups an alphabetical order decided between', () => {
    const inh = hostInheritance(doc, 'web', 'db-1', { fields: [], stored: {} });
    const owner = inh.rows.find((r) => r.key === 'owner')!;
    expect(inh.levels[owner.winner].name).toBe('zz_monitor');
    expect(owner.tie).toEqual(['backup', 'web']);
    // web beats backup at the same depth for port, but web_eu does not set
    // it: the tie decided it.
    const port = inh.rows.find((r) => r.key === 'port')!;
    expect(inh.levels[port.winner].name).toBe('web');
    expect(port.tie).toEqual(['backup']);
    // A deeper winner was decided by depth, not by name.
    expect(inh.rows.find((r) => r.key === 'region')!.tie).toEqual([]);
    expect(inh.rows.find((r) => r.key === 'schedule')!.tie).toEqual([]);
  });

  it('keeps the group\'s default right below the group itself', () => {
    const inh = hostInheritance(doc, 'web', 'db-2', { fields: [{ name: 'port', type: 'int', default: 22 }, { name: 'owner', type: 'string' }], stored: { port: 2, owner: 'web' } });
    expect(inh.levels.map((l) => `${l.kind}:${l.name}`)).toEqual(['default:web', 'group:web', 'group:web_eu', 'host:db-2']);
    expect(winners(inh)).toEqual(ansibleVars(doc, [], 'db-2'));
  });

  it('lists the group\'s hosts, direct and through subgroups, with the groups they are in', () => {
    expect(groupHosts(doc, 'web')).toEqual([
      { name: 'db-1', via: ['web_eu'] },
      { name: 'db-2', via: ['web_eu'] },
    ]);
    expect(groupHosts(doc, 'backup')).toEqual([{ name: 'db-1', via: ['backup'] }]);
    expect(groupHosts(doc, 'missing')).toEqual([]);
  });
});
