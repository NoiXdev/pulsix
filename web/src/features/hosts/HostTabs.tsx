import { PageTabs, type PageTabItem } from '../../ui/page-tabs.tsx';
import { HOST_PANEL_ID, hostTabId, type HostTab } from './hostTabs.ts';

export type HostTabItem = PageTabItem<HostTab>;

/**
 * HostTabs is the host page's tab strip: the shared PageTabs (the WAI-ARIA
 * tabs pattern, arrow/Home/End roving) with the host page's tab and panel
 * ids. The page owns which tab is open (it lives in the URL).
 */
export function HostTabs({ label, tabs, active, onSelect }: { label: string; tabs: HostTabItem[]; active: HostTab; onSelect: (tab: HostTab, via: 'click' | 'key') => void }) {
  return <PageTabs label={label} tabs={tabs} active={active} onSelect={onSelect} tabId={hostTabId} panelId={HOST_PANEL_ID} />;
}
