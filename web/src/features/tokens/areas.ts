import type { Role } from '../../lib/roles.ts';

/**
 * The areas an assistant token can hold (internal/tenancy/areas.go): named
 * sets of permissions the owner picks when creating the token. A write
 * area includes its read area, so ticking one ticks the other.
 */
export type Area =
  | 'inventory.read'
  | 'inventory.write'
  | 'repos.read'
  | 'repos.write'
  | 'templates.read'
  | 'templates.write'
  | 'credentials.names'
  | 'jobs.read'
  | 'jobs.request'
  | 'monitoring.read'
  | 'alerts.handle'
  | 'activity.read'
  | 'automation.request';

/** One row of the area grid: a read area and the area that writes it. */
export interface AreaRow {
  key: 'inventory' | 'repos' | 'templates' | 'jobs' | 'monitoring';
  read: Area;
  write: Area;
}

export const AREA_ROWS: readonly AreaRow[] = [
  { key: 'inventory', read: 'inventory.read', write: 'inventory.write' },
  { key: 'repos', read: 'repos.read', write: 'repos.write' },
  { key: 'templates', read: 'templates.read', write: 'templates.write' },
  { key: 'jobs', read: 'jobs.read', write: 'jobs.request' },
  { key: 'monitoring', read: 'monitoring.read', write: 'alerts.handle' },
];

export const CREDENTIALS_AREA: Area = 'credentials.names';
export const ACTIVITY_AREA: Area = 'activity.read';
/** Asking for schedules a person approves, and pausing or deleting them
 * (spec 2026-11-25 C): on its own, like the activity log. */
export const AUTOMATION_AREA: Area = 'automation.request';

/** Every area in the order the grid shows them. */
const ORDER: readonly Area[] = [...AREA_ROWS.flatMap((r) => [r.read, r.write]), CREDENTIALS_AREA, ACTIVITY_AREA, AUTOMATION_AREA];

/** The lowest role whose permissions cover the area: the server refuses
 * (422) an area the token's role cannot cover. */
const MINIMUM_ROLE: Record<Area, Role> = {
  'inventory.read': 'viewer',
  'repos.read': 'viewer',
  'templates.read': 'viewer',
  'jobs.read': 'viewer',
  'monitoring.read': 'viewer',
  'alerts.handle': 'operator',
  'activity.read': 'operator',
  'inventory.write': 'operator',
  'repos.write': 'operator',
  'templates.write': 'operator',
  'credentials.names': 'operator',
  'jobs.request': 'operator',
  'automation.request': 'operator',
};

const RANK: Record<Role, number> = { viewer: 1, operator: 2, admin: 3, owner: 4 };

/** areaAllowed reports whether a token of `role` can hold `area`; an
 * unknown role fails closed. */
export function areaAllowed(role: string, area: Area): boolean {
  const r = RANK[role as Role];
  return r !== undefined && r >= RANK[MINIMUM_ROLE[area]];
}

/** allowedAreas drops the areas `role` cannot hold, keeping the order. */
export function allowedAreas(areas: readonly Area[], role: string): Area[] {
  return areas.filter((a) => areaAllowed(role, a));
}

/** toggleArea ticks or unticks one area: ticking a write area ticks its
 * read area, unticking a read area unticks its write area. The result is
 * in grid order. */
export function toggleArea(areas: readonly Area[], area: Area, on: boolean): Area[] {
  const next = new Set(areas);
  const row = AREA_ROWS.find((r) => r.read === area || r.write === area);
  if (on) {
    next.add(area);
    if (row && row.write === area) next.add(row.read);
  } else {
    next.delete(area);
    if (row && row.read === area) next.delete(row.write);
  }
  return ORDER.filter((a) => next.has(a));
}

/** displayedAreas is what a token list shows: a read area is left out when
 * its write area is there, since the write area includes it. */
export function displayedAreas(areas: readonly string[]): string[] {
  const held = new Set(areas);
  return areas.filter((a) => !AREA_ROWS.some((r) => r.read === a && held.has(r.write)));
}

/** sameAreas reports whether two area lists hold the same areas, in any order. */
export function sameAreas(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && new Set(a).size === new Set([...a, ...b]).size;
}
