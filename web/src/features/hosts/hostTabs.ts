/** The host page's tabs, in the order the strip shows them. */
export const HOST_TABS = ['overview', 'checks', 'alerts', 'runs', 'inventory', 'connection'] as const;

export type HostTab = (typeof HOST_TABS)[number];

/** The host page's search params: the open tab, absent for Overview. */
export interface HostSearch {
  tab?: HostTab;
}

/** parseHostTab reads `?tab=`: a tab the page has, else Overview. */
export function parseHostTab(raw: unknown): HostTab {
  return typeof raw === 'string' && (HOST_TABS as readonly string[]).includes(raw) ? (raw as HostTab) : 'overview';
}

/** validateHostSearch is the host route's validateSearch. Overview is the
 * default, so it — and anything unknown, which falls back to it — stays out
 * of the URL. */
export function validateHostSearch(search: Record<string, unknown>): HostSearch {
  const tab = parseHostTab(search.tab);
  return { tab: tab === 'overview' ? undefined : tab };
}

/** The id of a tab's button, which labels the tab panel. */
export const hostTabId = (id: HostTab) => `host-tab-${id}`;

/** The id of the one tab panel, which every tab controls. */
export const HOST_PANEL_ID = 'host-tab-panel';
