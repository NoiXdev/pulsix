/** The group page's tabs, in the order the strip shows them. */
export const GROUP_TABS = ['variables', 'members', 'subgroups', 'inheritance', 'inventory'] as const;

export type GroupTab = (typeof GROUP_TABS)[number];

/** The group page's search params: the open tab, absent for Variables. */
export interface GroupSearch {
  tab?: GroupTab;
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
 * of the URL. */
export function validateGroupSearch(search: Record<string, unknown>): GroupSearch {
  const tab = parseGroupTab(search.tab);
  return { tab: tab === 'variables' ? undefined : tab };
}

/** The id of a tab's button, which labels the tab panel. */
export const groupTabId = (id: GroupTab) => `group-tab-${id}`;

/** The id of the one tab panel, which every tab controls. */
export const GROUP_PANEL_ID = 'group-tab-panel';
