import { useEffect, useState, type ReactNode } from 'react';
import { useNavigate, useParams, useRouterState, useSearch } from '@tanstack/react-router';
import { PageTabs, type PageTabItem } from '../../ui/page-tabs.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useUnsavedGuard } from '../../ui/useUnsavedGuard.ts';
import { useGroup, useGroups } from '../../api/hooks/useInventory.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import type { InventoryGroup } from '../../api/types.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { GroupHeader } from './GroupHeader.tsx';
import { GroupInheritanceTab } from './GroupInheritanceTab.tsx';
import { groupPanelId, groupTabId, parseGroupTab, type GroupTab } from './groupTabs.ts';
import { MembersSection } from './sections/MembersSection.tsx';
import { VariablesTab } from './sections/VariablesTab.tsx';
import { ChildrenSection } from './sections/ChildrenSection.tsx';
import { PreviewSection } from './sections/PreviewSection.tsx';
import { OwnStepsContext, useOwnStepsRecord } from './ownSteps.ts';
import { FieldsDialog } from './fields/FieldsDialog.tsx';

/**
 * GroupDetailPage is one inventory group on one page, laid out like the host
 * page (spec 2026-11-18-groups-and-list-filters-design.md, "Group page"): a
 * full-width header with who the group is and its actions, and below it
 * tabs — Variables, Members, Subgroups, Inheritance, Inventory — the open one
 * in the URL (`?tab=`, Variables when absent). The schema is edited in the
 * Manage fields dialog the header opens.
 *
 * The Variables panel stays mounted (hidden) while another tab is open, so
 * unsaved values survive a tab switch; leaving the page with them asks
 * first (useUnsavedGuard), a tab switch does not.
 */
export function GroupDetailPage() {
  const { org, id } = useParams({ strict: false }) as { org: string; id: string };
  const orgInfo = useOrg(org);
  const group = useGroup(org, id);

  // The role decides which write actions the page offers, and
  // `may(undefined, …)` is false, so the org query is guarded first (the
  // HostDetailPage order): a page shown before it lands would hide an
  // owner's actions.
  const orgGuard = useQueryGuard(orgInfo);
  const groupGuard = useQueryGuard(group);
  if (orgGuard) return orgGuard;
  if (groupGuard) return groupGuard;
  if (!orgInfo.data || !group.data) return <QueryLoading />;
  // Keyed by the group: a draft, an open dialog or the fields dialog opened
  // for a new group never follows the user to another group.
  return <GroupDetail key={group.data.group.id} org={org} group={group.data.group} role={orgInfo.data.org.role} />;
}

const PANEL_CLASS = 'flex min-w-0 flex-col gap-8';

function GroupDetail({ org, group, role }: { org: string; group: InventoryGroup; role: string }) {
  const t = useT();
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as { tab?: unknown };
  const tab = parseGroupTab(search.tab);
  const hash = useRouterState({ select: (s) => s.location.hash });
  const groups = useGroups(org);
  const all = groups.data?.groups ?? [];
  const own = all.find((g) => g.id === group.id);
  const children = all.filter((g) => g.parent_id === group.id).length;
  const canWrite = may(role, 'inventory.write');
  const tabs = t.inventory.groupPage.tabs;
  // The page's own writes to the group, which an edited draft may follow
  // (useRevisionDraft); the record starts over with another group.
  const ownSteps = useOwnStepsRecord(group.id, group.revision);
  // A group just created ("#schema", also where an old `?tab=schema` link
  // is sent) opens on its fields; a reader stays on Variables.
  const [fieldsOpen, setFieldsOpen] = useState(() => canWrite && hash === 'schema');
  const [valuesDirty, setValuesDirty] = useState(false);
  const release = useUnsavedGuard(valuesDirty, { samePath: true });

  // A reader's "#schema" opens nothing, so it goes at once.
  useEffect(() => {
    if (!canWrite && hash === 'schema') {
      void navigate({ to: '/o/$org/groups/$id', params: { org, id: group.id }, search: (prev) => prev, hash: '', replace: true });
    }
  }, [canWrite, hash, navigate, org, group.id]);

  // Arrow/Home/End roving replaces the history entry: stepping through the
  // tabs with the keyboard must not fill the back button.
  const select = (next: GroupTab, via: 'click' | 'key' = 'click') =>
    void navigate({ to: '/o/$org/groups/$id', params: { org, id: group.id }, search: { tab: next === 'variables' ? undefined : next }, replace: via === 'key' });

  const items: PageTabItem<GroupTab>[] = [
    { id: 'variables', label: tabs.variables },
    { id: 'members', label: tabs.members, count: own?.members },
    { id: 'subgroups', label: tabs.subgroups, count: groups.data ? children : undefined },
    { id: 'inheritance', label: tabs.inheritance },
    { id: 'inventory', label: tabs.inventory },
  ];

  const openFields = () => setFieldsOpen(true);
  // The "#schema" a new group was opened with has done its job: a reload
  // must not open the fields again.
  const closeFields = () => {
    setFieldsOpen(false);
    if (hash === 'schema') {
      void navigate({ to: '/o/$org/groups/$id', params: { org, id: group.id }, search: { tab: tab === 'variables' ? undefined : tab }, hash: '', replace: true });
    }
  };
  // Variables is not among them: it is always mounted (below).
  const panels: Record<Exclude<GroupTab, 'variables'>, () => ReactNode> = {
    members: () => <MembersSection org={org} group={group} canWrite={canWrite} />,
    subgroups: () => <ChildrenSection org={org} group={group} groups={all} />,
    inheritance: () => <GroupInheritanceTab org={org} group={group} />,
    inventory: () => <PreviewSection org={org} group={group} />,
  };

  return (
    <OwnStepsContext.Provider value={ownSteps}>
      <div className="flex flex-col gap-5">
        <GroupHeader org={org} group={group} groups={all} role={role} onManageFields={openFields} onLeave={release} />
        <PageTabs label={tabs.label} tabs={items} active={tab} onSelect={select} tabId={groupTabId} panelId={groupPanelId} />
        <div
          role="tabpanel"
          id={groupPanelId('variables')}
          aria-labelledby={groupTabId('variables')}
          hidden={tab !== 'variables'}
          className={tab === 'variables' ? PANEL_CLASS : 'hidden'}
        >
          {/* Never keyed by the revision: a save of the schema bumps it, and
              must not throw away an unsaved values draft. Nor by the values:
              the draft follows the stored values itself while it is unedited
              (useRevisionDraft), and such a key would remount it when its own
              save lands, losing an edit typed during that save (backlogs
              7.89, 7.90). It is keyed by the schema, so a schema change
              restarts the values, because their fields changed with it. */}
          <VariablesTab
            key={JSON.stringify(group.schema)}
            org={org}
            group={group}
            groups={all}
            canWrite={canWrite}
            onManageFields={openFields}
            onDirtyChange={setValuesDirty}
          />
        </div>
        {tab !== 'variables' && (
          <div role="tabpanel" id={groupPanelId(tab)} aria-labelledby={groupTabId(tab)} className={PANEL_CLASS}>
            {panels[tab]()}
          </div>
        )}
        <FieldsDialog org={org} group={group} canWrite={canWrite} open={fieldsOpen} onClose={closeFields} />
      </div>
    </OwnStepsContext.Provider>
  );
}
