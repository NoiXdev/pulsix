import { isRecord } from '../variables/secrets.ts';
import type { SchemaField, Vars } from '../variables/types.ts';

/*
 * The inheritance table: per variable, every level that could set it, from
 * the weakest to the strongest, and which one wins. It follows what the
 * server renders and what Ansible then does with it:
 *
 *  - internal/inventory/render/render.go writes each group's own vars as
 *    `<group>.vars`, through schema.Resolve: the stored values plus the
 *    group schema's defaults for every unset active group field. A default
 *    is therefore part of the group's own level — it beats every group
 *    above, and a value the group stores beats it.
 *  - `all` never carries vars from the server; the level shows only when a
 *    document has them.
 *  - `_meta.hostvars` holds the host's membership vars (merged across its
 *    groups by depth, then name, render.go membershipsByHost), its
 *    connection vars and facts. Ansible ranks host vars above every group.
 *  - Ansible merges group vars sorted by (depth, ansible_group_priority,
 *    name), deeper and then alphabetically later winning. No group carries
 *    ansible_group_priority (the schema reserves the `ansible_` prefix), so
 *    depth and name decide; a tie between groups of the same depth is
 *    decided by the name alone, which the table names (InheritanceRow.tie).
 */

/** What a level is: the `all` group, an inventory group, the schema
 * defaults the renderer writes into the viewed group, or the host. */
export type LevelKind = 'all' | 'group' | 'default' | 'host';

/** Where a group sits relative to the viewed group. */
export type LevelRole = 'ancestor' | 'self' | 'descendant' | 'other';

export interface Level {
  /** Unique within a table. */
  id: string;
  kind: LevelKind;
  /** The group's or host's name; a default level carries its group's. */
  name: string;
  /** Groups and the default level only. */
  role?: LevelRole;
  /** Ansible's depth (`all` 0, top-level groups 1, …); the host is last. */
  depth: number;
  vars: Vars;
}

export type CellState = 'win' | 'lost' | 'unset';

export interface Cell {
  state: CellState;
  /** The level's value; absent when it leaves the variable unset. */
  value?: unknown;
}

export interface InheritanceRow {
  key: string;
  /** One per level, in the table's level order. */
  cells: Cell[];
  /** Index of the winning level. */
  winner: number;
  /** Other groups at the winner's depth that set the variable too: the
   * name order alone decided between them. Empty when depth decided. */
  tie: string[];
}

export interface Inheritance {
  levels: Level[];
  rows: InheritanceRow[];
}

/** The viewed group as the API returns it: its schema's fields and its
 * stored values (a stored secret reads as a mask; only presence counts). */
export interface GroupSource {
  fields: readonly SchemaField[];
  stored: Vars;
}

interface DocGroup {
  vars: Vars;
  children: string[];
  hosts: string[];
}

/** Keys of a dynamic-inventory document that are not inventory groups
 * (internal/inventory/service.go reservedGroupNames). */
const RESERVED = new Set(['all', 'ungrouped', '_meta']);

const strings = (v: unknown): string[] => (Array.isArray(v) ? v.filter((s): s is string => typeof s === 'string') : []);

function docGroup(doc: Readonly<Record<string, unknown>>, name: string): DocGroup | undefined {
  if (!Object.hasOwn(doc, name)) return undefined;
  const entry = doc[name];
  if (!isRecord(entry)) return undefined;
  return { vars: isRecord(entry.vars) ? entry.vars : {}, children: strings(entry.children), hosts: strings(entry.hosts) };
}

function groupNames(doc: Readonly<Record<string, unknown>>): string[] {
  return Object.keys(doc).filter((k) => !RESERVED.has(k) && docGroup(doc, k));
}

function parents(doc: Readonly<Record<string, unknown>>): Map<string, string> {
  const out = new Map<string, string>();
  for (const g of groupNames(doc)) for (const c of docGroup(doc, g)!.children) out.set(c, g);
  return out;
}

/** The groups above name, from the top-level one down to its parent. */
function chain(up: Map<string, string>, name: string): string[] {
  const out: string[] = [];
  const seen = new Set([name]);
  let p = up.get(name);
  while (p !== undefined && !seen.has(p)) {
    seen.add(p);
    out.unshift(p);
    p = up.get(p);
  }
  return out;
}

/** Python's (and Go's) string order, which Ansible and the renderer sort
 * group names by — not the locale's. */
const byCodepoint = (a: string, b: string) => (a < b ? -1 : a > b ? 1 : 0);

/** inventoryHas says whether a preview document renders the group. */
export function inventoryHas(doc: Readonly<Record<string, unknown>> | undefined, group: string): boolean {
  return !!doc && !RESERVED.has(group) && !!docGroup(doc, group);
}

export interface GroupHost {
  name: string;
  /** The groups at or below the viewed group the host is a member of. */
  via: string[];
}

/** groupHosts lists the hosts a preview renders in the group or any group
 * below it, by name. */
export function groupHosts(doc: Readonly<Record<string, unknown>>, group: string): GroupHost[] {
  if (!inventoryHas(doc, group)) return [];
  const up = parents(doc);
  const via = new Map<string, string[]>();
  for (const g of groupNames(doc).sort(byCodepoint)) {
    if (g !== group && !chain(up, g).includes(group)) continue;
    for (const h of docGroup(doc, g)!.hosts) via.set(h, [...(via.get(h) ?? []), g]);
  }
  return [...via.entries()].sort(([a], [b]) => byCodepoint(a, b)).map(([name, groups]) => ({ name, via: groups }));
}

/** splitDefaults divides the viewed group's rendered vars into what its
 * schema's defaults supply and what it stores itself. */
function splitDefaults(rendered: Vars, src: GroupSource): { defaults: Vars; own: Vars } {
  const defaults: Vars = {};
  for (const f of src.fields) {
    if ((f.scope ?? 'group') !== 'group' || f.active === false || f.default === undefined || f.default === null) continue;
    defaults[f.name] = f.default;
  }
  const own: Vars = {};
  for (const [k, v] of Object.entries(rendered)) {
    const stored = Object.hasOwn(src.stored, k) ? src.stored[k] : undefined;
    // Unset here but rendered: the default the renderer filled in, as
    // rendered (the preview is the truth, not the schema copy).
    if ((stored === undefined || stored === null) && Object.hasOwn(defaults, k)) defaults[k] = v;
    else own[k] = v;
  }
  return { defaults, own };
}

function groupLevel(doc: Readonly<Record<string, unknown>>, name: string, depth: number, role: LevelRole): Level {
  return { id: `group:${name}`, kind: 'group', name, role, depth, vars: docGroup(doc, name)?.vars ?? {} };
}

/** selfLevels are the viewed group's default level (when its schema has
 * defaults) and its own level. */
function selfLevels(doc: Readonly<Record<string, unknown>>, group: string, depth: number, src: GroupSource): Level[] {
  const { defaults, own } = splitDefaults(docGroup(doc, group)?.vars ?? {}, src);
  const self: Level = { id: `group:${group}`, kind: 'group', name: group, role: 'self', depth, vars: own };
  if (Object.keys(defaults).length === 0) return [self];
  return [{ id: `default:${group}`, kind: 'default', name: group, role: 'self', depth, vars: defaults }, self];
}

function allLevel(doc: Readonly<Record<string, unknown>>): Level[] {
  const all = Object.hasOwn(doc, 'all') && isRecord(doc.all) && isRecord(doc.all.vars) ? doc.all.vars : {};
  return Object.keys(all).length > 0 ? [{ id: 'all', kind: 'all', name: 'all', depth: 0, vars: all }] : [];
}

/**
 * groupInheritance is the group view: `all` (when it has vars), each
 * ancestor, the group's defaults and the group — what a host in this group
 * and no other would get from its groups, before its own host vars.
 */
export function groupInheritance(doc: Readonly<Record<string, unknown>>, group: string, src: GroupSource): Inheritance {
  const ancestors = chain(parents(doc), group);
  const levels = [
    ...allLevel(doc),
    ...ancestors.map((a, i) => groupLevel(doc, a, i + 1, 'ancestor')),
    ...selfLevels(doc, group, ancestors.length + 1, src),
  ];
  return { levels, rows: rowsOf(levels) };
}

/**
 * hostInheritance is the host view: every group the host inherits from —
 * the groups it is a member of and every group above them, the viewed
 * group's path and any other branch alike — in Ansible's order (depth,
 * then name), then the host's own vars.
 */
export function hostInheritance(doc: Readonly<Record<string, unknown>>, group: string, host: string, src: GroupSource): Inheritance {
  const up = parents(doc);
  const set = new Set<string>();
  for (const g of groupNames(doc)) {
    if (!docGroup(doc, g)!.hosts.includes(host)) continue;
    set.add(g);
    for (const a of chain(up, g)) set.add(a);
  }
  const depth = (g: string) => chain(up, g).length + 1;
  const pathAbove = new Set(chain(up, group));
  const role = (g: string): LevelRole => (g === group ? 'self' : pathAbove.has(g) ? 'ancestor' : chain(up, g).includes(group) ? 'descendant' : 'other');
  const ordered = [...set].sort((a, b) => depth(a) - depth(b) || byCodepoint(a, b));
  const levels: Level[] = [...allLevel(doc)];
  for (const g of ordered) {
    if (g === group) levels.push(...selfLevels(doc, group, depth(g), src));
    else levels.push(groupLevel(doc, g, depth(g), role(g)));
  }
  const meta = Object.hasOwn(doc, '_meta') && isRecord(doc._meta) ? doc._meta : {};
  const hostvars = isRecord(meta.hostvars) && Object.hasOwn(meta.hostvars, host) && isRecord(meta.hostvars[host]) ? meta.hostvars[host] : {};
  levels.push({ id: `host:${host}`, kind: 'host', name: host, depth: Number.POSITIVE_INFINITY, vars: hostvars });
  return { levels, rows: rowsOf(levels) };
}

/** rowsOf builds one row per variable any level sets, by name: the last
 * level that sets it wins, the earlier ones that do are overridden. */
function rowsOf(levels: readonly Level[]): InheritanceRow[] {
  const keys = new Set<string>();
  for (const l of levels) for (const k of Object.keys(l.vars)) keys.add(k);
  return [...keys].sort(byCodepoint).map((key) => {
    const sets = levels.map((l) => Object.hasOwn(l.vars, key));
    const winner = sets.lastIndexOf(true);
    const w = levels[winner];
    const tie =
      w.kind === 'group' || w.kind === 'default'
        ? levels.filter((l, i) => sets[i] && l.kind === 'group' && l.name !== w.name && l.depth === w.depth).map((l) => l.name)
        : [];
    const cells = levels.map((l, i): Cell => (i === winner ? { state: 'win', value: l.vars[key] } : sets[i] ? { state: 'lost', value: l.vars[key] } : { state: 'unset' }));
    return { key, cells, winner, tie };
  });
}
