/** The template page's tabs, in the order the strip shows them. */
export const TEMPLATE_TABS = ['overview', 'variables', 'schedules', 'triggers', 'runs', 'settings'] as const;

export type TemplateTab = (typeof TEMPLATE_TABS)[number];

/** The template page's search params: the open tab, absent for Overview. */
export interface TemplateSearch {
  tab?: TemplateTab;
}

/** parseTemplateTab reads `?tab=`: a tab the page has, else Overview. */
export function parseTemplateTab(raw: unknown): TemplateTab {
  return typeof raw === 'string' && (TEMPLATE_TABS as readonly string[]).includes(raw) ? (raw as TemplateTab) : 'overview';
}

/** validateTemplateSearch is the template route's validateSearch. Overview
 * is the default, so it — and anything unknown, which falls back to it —
 * stays out of the URL. */
export function validateTemplateSearch(search: Record<string, unknown>): TemplateSearch {
  const tab = parseTemplateTab(search.tab);
  return { tab: tab === 'overview' ? undefined : tab };
}

/** The id of a tab's button, which labels the tab panel. */
export const templateTabId = (id: TemplateTab) => `template-tab-${id}`;

/** The id of the one tab panel, which every tab controls. */
export const TEMPLATE_PANEL_ID = 'template-tab-panel';
