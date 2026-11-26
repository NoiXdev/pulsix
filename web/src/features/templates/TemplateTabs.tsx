import { PageTabs, type PageTabItem } from '../../ui/page-tabs.tsx';
import { TEMPLATE_PANEL_ID, templateTabId, type TemplateTab } from './templateTabs.ts';

export type TemplateTabItem = PageTabItem<TemplateTab>;

/**
 * TemplateTabs is the template page's tab strip: the shared PageTabs (the
 * WAI-ARIA tabs pattern, arrow/Home/End roving) with the template page's
 * tab and panel ids. The page owns which tab is open (it lives in the URL).
 */
export function TemplateTabs({ label, tabs, active, onSelect }: { label: string; tabs: TemplateTabItem[]; active: TemplateTab; onSelect: (tab: TemplateTab, via: 'click' | 'key') => void }) {
  return <PageTabs label={label} tabs={tabs} active={active} onSelect={onSelect} tabId={templateTabId} panelId={TEMPLATE_PANEL_ID} />;
}
