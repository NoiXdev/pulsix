import { useParams } from '@tanstack/react-router';
import { DetailLayout, type SectionNavItem } from '../../ui/detail-layout.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useGroup, useGroups } from '../../api/hooks/useInventory.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import type { InventoryGroup } from '../../api/types.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { GroupSidebar } from './GroupSidebar.tsx';
import { MembersSection } from './sections/MembersSection.tsx';
import { ValuesSection } from './sections/ValuesSection.tsx';
import { SchemaSection } from './sections/SchemaSection.tsx';
import { ChildrenSection } from './sections/ChildrenSection.tsx';
import { PreviewSection } from './sections/PreviewSection.tsx';
import { OwnStepsContext, useOwnStepsRecord } from './ownSteps.ts';

/**
 * GroupDetailPage is one inventory group on one page, laid out like the host
 * page: a sticky sidebar with who the group is and its actions, and the
 * sections Members, Group values, Variable schema, Child groups and Preview
 * inventory, reachable from the sidebar's nav.
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
  return <GroupDetail org={org} group={group.data.group} role={orgInfo.data.org.role} />;
}

function GroupDetail({ org, group, role }: { org: string; group: InventoryGroup; role: string }) {
  const t = useT();
  const groups = useGroups(org);
  const all = groups.data?.groups ?? [];
  const own = all.find((g) => g.id === group.id);
  const children = all.filter((g) => g.parent_id === group.id).length;
  const canWrite = may(role, 'inventory.write');
  const s = t.inventory.detail.section;
  // The page's own writes to the group, which an edited draft may follow
  // (useRevisionDraft); the record starts over with another group.
  const ownSteps = useOwnStepsRecord(group.id);

  const nav: SectionNavItem[] = [
    { id: 'members', label: s.members, count: own?.members },
    { id: 'values', label: s.values },
    { id: 'schema', label: s.schema },
    { id: 'children', label: s.children, count: groups.data ? children : undefined },
    { id: 'preview', label: s.preview },
  ];

  return (
    <OwnStepsContext.Provider value={ownSteps}>
      <DetailLayout sidebar={<GroupSidebar org={org} group={group} groups={all} role={role} />} nav={nav} navLabel={t.common.sections}>
        <MembersSection org={org} group={group} canWrite={canWrite} />
        {/* Never keyed by the revision: a save in one section bumps it, and
            must not throw away an unsaved draft in the other. The values
            are keyed by what they edit, so a schema change restarts them,
            because their fields changed with it. The schema is keyed by the
            group only: its draft follows the stored schema itself while it
            is unedited (useRevisionDraft), and a key on the schema would
            remount it when its own save lands, losing an edit typed during
            that save. The id keeps a draft from following the user to
            another group with the same content. */}
        <ValuesSection key={JSON.stringify([group.id, group.schema, group.vars])} org={org} group={group} canWrite={canWrite} />
        <SchemaSection key={group.id} org={org} group={group} canWrite={canWrite} />
        <ChildrenSection org={org} group={group} groups={all} canWrite={canWrite} />
        <PreviewSection org={org} group={group} />
      </DetailLayout>
    </OwnStepsContext.Provider>
  );
}
