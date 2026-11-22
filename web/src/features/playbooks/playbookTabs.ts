/** The playbook page's tabs, in the order the strip shows them. */
export const PLAYBOOK_TABS = ['editor', 'dry-run', 'runs', 'used'] as const;

export type PlaybookTab = (typeof PLAYBOOK_TABS)[number];

/** The playbook page's search params: the open tab, absent for Editor. */
export interface PlaybookSearch {
  tab?: Exclude<PlaybookTab, 'editor'>;
}

/** parsePlaybookTab reads `?tab=`: a tab the page has, else Editor. */
export function parsePlaybookTab(raw: unknown): PlaybookTab {
  return typeof raw === 'string' && (PLAYBOOK_TABS as readonly string[]).includes(raw) ? (raw as PlaybookTab) : 'editor';
}

/** validatePlaybookSearch is the playbook route's validateSearch. Editor is
 * the default, so it — and anything unknown, which falls back to it — stays
 * out of the URL. */
export function validatePlaybookSearch(search: Record<string, unknown>): PlaybookSearch {
  const tab = parsePlaybookTab(search.tab);
  return { tab: tab === 'editor' ? undefined : tab };
}

/** The id of a tab's button, which labels its tab panel. */
export const playbookTabId = (id: PlaybookTab) => `playbook-tab-${id}`;

/** The id of a tab's panel, which its tab controls. */
export const playbookPanelId = (id: PlaybookTab) => `playbook-panel-${id}`;
