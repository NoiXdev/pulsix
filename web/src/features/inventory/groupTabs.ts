import { redirect } from '@tanstack/react-router';

/** The group page's tabs, in the order the strip shows them. */
export const GROUP_TABS = ['variables', 'members', 'subgroups', 'inheritance', 'inventory'] as const;

export type GroupTab = (typeof GROUP_TABS)[number];

/** The group page's search params: the open tab, absent for Variables. */
export interface GroupSearch {
  tab?: GroupTab;
  /** An old `?tab=schema` link, which the route's beforeLoad turns into
   * `#schema` at once (formerGroupLink). The app never builds it. */
  schema?: true;
}

/** The tab each earlier name of a part of the page became: the tabs of the
 * first group page (`?tab=values`) and the sections of the second one
 * (`#children`), so bookmarks and older docs still land where they meant.
 * The schema editor is the Manage fields dialog now, opened from the
 * Variables tab. */
const FORMER: Readonly<Record<string, GroupTab>> = {
  values: 'variables',
  schema: 'variables',
  members: 'members',
  children: 'subgroups',
  preview: 'inventory',
};

/** parseGroupTab reads `?tab=`: a tab the page has (or had under another
 * name), else Variables. */
export function parseGroupTab(raw: unknown): GroupTab {
  if (typeof raw !== 'string') return 'variables';
  if ((GROUP_TABS as readonly string[]).includes(raw)) return raw as GroupTab;
  return Object.hasOwn(FORMER, raw) ? FORMER[raw] : 'variables';
}

/** formerSection is the tab a section anchor of the earlier page (`#members`)
 * stands for, undefined for any other hash. */
export function formerSection(hash: string): GroupTab | undefined {
  const name = hash.replace(/^#/, '');
  return Object.hasOwn(FORMER, name) ? FORMER[name] : undefined;
}

/** validateGroupSearch is the group route's validateSearch. Variables is the
 * default, so it — and anything unknown, which falls back to it — stays out
 * of the URL. An old `?tab=schema` is kept apart (`schema`), because the
 * router hands beforeLoad the validated search only. */
export function validateGroupSearch(search: Record<string, unknown>): GroupSearch {
  if (search.tab === 'schema' || search.schema === true) return { tab: undefined, schema: true };
  const tab = parseGroupTab(search.tab);
  return { tab: tab === 'variables' ? undefined : tab };
}

/** What the group route's beforeLoad gets, as far as formerGroupLink reads
 * it. */
export interface GroupRouteLoad {
  search: GroupSearch;
  params: { org: string; id: string };
  location: { hash: string };
}

/**
 * formerGroupLink is the group route's beforeLoad: it sends a link of an
 * earlier page on to where it meant. A section anchor (`#members`) becomes
 * its tab; `?tab=schema` becomes `#schema`, which opens the fields dialog
 * for a writer (and leaves a reader on Variables); a `#values` anchor, which
 * Variables already is, is dropped.
 */
export function formerGroupLink({ search, params, location }: GroupRouteLoad): void {
  if (search.schema) {
    throw redirect({ to: '/o/$org/groups/$id', params, search: { tab: undefined }, hash: 'schema', replace: true });
  }
  const former = search.tab === undefined ? formerSection(location.hash) : undefined;
  if (former && former !== 'variables') throw redirect({ to: '/o/$org/groups/$id', params, search: { tab: former }, hash: '', replace: true });
  if (location.hash === 'values') throw redirect({ to: '/o/$org/groups/$id', params, search, hash: '', replace: true });
}

/** The id of a tab's button, which labels its tab panel. */
export const groupTabId = (id: GroupTab) => `group-tab-${id}`;

/** The id of a tab's panel, which its tab controls. */
export const groupPanelId = (id: GroupTab) => `group-panel-${id}`;
