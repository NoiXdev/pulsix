import { useState, type ReactNode } from 'react';
import { useNavigate, useParams, useSearch } from '@tanstack/react-router';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useUnsavedGuard } from '../../ui/useUnsavedGuard.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useTemplate } from '../../api/hooks/useTemplates.ts';
import { useSchedules } from '../../api/hooks/useSchedules.ts';
import { useTriggers } from '../../api/hooks/useTriggers.ts';
import type { Template } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { TemplateHeader } from './TemplateHeader.tsx';
import { TemplateOverviewTab } from './TemplateOverviewTab.tsx';
import { TemplateRunsTab } from './TemplateRunsTab.tsx';
import { TemplateSchedulesTab } from './TemplateSchedulesTab.tsx';
import { TemplateTriggersTab } from './TemplateTriggersTab.tsx';
import { TemplateReviewNotice } from './TemplateReviewNotice.tsx';
import { TemplateSettingsTab } from './TemplateSettingsTab.tsx';
import { TemplateTabs, type TemplateTabItem } from './TemplateTabs.tsx';
import { TemplateVariablesPage } from './variables/TemplateVariablesPage.tsx';
import { TEMPLATE_PANEL_ID, parseTemplateTab, templateTabId, type TemplateTab } from './templateTabs.ts';
import { pausedForReview } from './templateDetail.ts';

/**
 * TemplateDetailPage is one job template on one page (backlog 1.53), laid
 * out like the host page: a full-width header with what the template runs
 * where and its actions (Run, Dry run, Edit, "⋯"), a notice when an
 * assistant's change paused its schedules or triggers, and below it the
 * tabs — Overview, Variables, Schedules, Triggers, Runs, Settings — the
 * open one in the URL (`?tab=`, Overview when absent).
 */
export function TemplateDetailPage() {
  const { org, id } = useParams({ strict: false }) as { org: string; id: string };
  const orgInfo = useOrg(org);
  const template = useTemplate(org, id);

  // The role decides what the header and the tabs offer: ask it only once
  // the org response is in, so "not loaded yet" never reads as "not
  // permitted" (HostDetailPage's own ordering).
  const orgGuard = useQueryGuard(orgInfo);
  const templateGuard = useQueryGuard(template);
  if (orgGuard) return orgGuard;
  if (templateGuard) return templateGuard;
  if (!orgInfo.data || !template.data) return <QueryLoading />;
  return <TemplateDetail org={org} template={template.data.template} role={orgInfo.data.org.role} onReload={async () => (await template.refetch()).data?.template} />;
}

export interface TemplateDetailProps {
  org: string;
  template: Template;
  role: string;
  /** Reads the template again (the Settings tab's Reload after a conflict). */
  onReload: () => Promise<Template | undefined>;
}

function TemplateDetail({ org, template, role, onReload }: TemplateDetailProps) {
  const t = useT();
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as { tab?: unknown };
  const tab = parseTemplateTab(search.tab);
  const tabs = t.templates.page.tabs;
  // Edit opens the Settings tab with its form editable; leaving the tab
  // ends the edit. With changes not saved yet, leaving the tab or the page
  // asks first (useUnsavedGuard: a tab switch is a navigation too).
  const [editing, setEditingState] = useState(false);
  const [settingsDirty, setSettingsDirty] = useState(false);
  const setEditing = (next: boolean) => {
    setEditingState(next);
    if (!next) setSettingsDirty(false);
  };
  const [shownTab, setShownTab] = useState(tab);
  if (shownTab !== tab) {
    setShownTab(tab);
    if (tab !== 'settings') setEditing(false);
  }
  const guarded = editing && settingsDirty;
  const releaseGuard = useUnsavedGuard(guarded);

  const schedules = useSchedules(org, template.id);
  const triggers = useTriggers(org, { kind: 'template_run', id: template.id, name: template.name });
  const scheduleList = schedules.data?.schedules ?? [];
  const triggerList = triggers.data?.triggers ?? [];
  const paused = pausedForReview(scheduleList, triggerList);

  // Arrow/Home/End roving replaces the history entry: stepping through the
  // tabs with the keyboard must not fill the back button.
  const select = (next: TemplateTab, via: 'click' | 'key' = 'click') =>
    void navigate({ to: '/o/$org/templates/$id', params: { org, id: template.id }, search: { tab: next === 'overview' ? undefined : next }, replace: via === 'key' });
  const edit = () => {
    setEditing(true);
    if (tab !== 'settings') select('settings');
  };

  const items: TemplateTabItem[] = [
    { id: 'overview', label: tabs.overview },
    { id: 'variables', label: tabs.variables },
    { id: 'schedules', label: tabs.schedules, count: schedules.data ? scheduleList.length : undefined, tone: paused && scheduleList.some((s) => s.paused_reason) ? 'warn' : undefined },
    { id: 'triggers', label: tabs.triggers, count: triggers.data ? triggerList.length : undefined, tone: paused && triggerList.some((tr) => tr.disabled_reason) ? 'warn' : undefined },
    { id: 'runs', label: tabs.runs },
    { id: 'settings', label: tabs.settings },
  ];

  const panels: Record<TemplateTab, () => ReactNode> = {
    overview: () => <TemplateOverviewTab org={org} template={template} schedules={schedules.data ? scheduleList : undefined} triggers={triggers.data ? triggerList : undefined} onTab={select} />,
    variables: () => <TemplateVariablesPage embedded />,
    schedules: () => <TemplateSchedulesTab org={org} template={template} role={role} schedules={schedules.data ? scheduleList : undefined} error={schedules.error} />,
    triggers: () => <TemplateTriggersTab org={org} template={template} role={role} triggers={triggers.data ? triggerList : undefined} error={triggers.error} />,
    runs: () => <TemplateRunsTab org={org} templateId={template.id} role={role} schedules={schedules.data ? scheduleList : undefined} triggers={triggers.data ? triggerList : undefined} />,
    settings: () => <TemplateSettingsTab org={org} template={template} role={role} editing={editing} onEditingChange={setEditing} onReload={onReload} onDirtyChange={setSettingsDirty} />,
  };

  return (
    <div className="flex flex-col gap-5">
      {/* The Settings tab has its own Edit: the header's is left out there. */}
      <TemplateHeader org={org} template={template} role={role} onEdit={tab === 'settings' ? undefined : edit} onDeleted={releaseGuard} />
      {paused && <TemplateReviewNotice org={org} templateId={template.id} paused={paused} />}
      <TemplateTabs label={tabs.label} tabs={items} active={tab} onSelect={select} manual={guarded} />
      <div role="tabpanel" id={TEMPLATE_PANEL_ID} aria-labelledby={templateTabId(tab)} className="flex min-w-0 flex-col gap-8">
        {panels[tab]()}
      </div>
    </div>
  );
}
