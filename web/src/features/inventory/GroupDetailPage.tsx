import { useNavigate, useParams, useSearch } from '@tanstack/react-router';
import { Tag } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useGroup, useGroups } from '../../api/hooks/useInventory.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { may } from '../../lib/roles.ts';
import { SchemaTab } from './tabs/SchemaTab.tsx';
import { ValuesTab } from './tabs/ValuesTab.tsx';
import { MembersTab } from './tabs/MembersTab.tsx';
import { ChildrenTab } from './tabs/ChildrenTab.tsx';
import { PreviewTab } from './tabs/PreviewTab.tsx';
import { SettingsTab } from './tabs/SettingsTab.tsx';

const TABS = [
  ['schema', 'Variable schema'],
  ['values', 'Group values'],
  ['members', 'Members'],
  ['children', 'Children'],
  ['preview', 'Preview inventory'],
  ['settings', 'Settings'],
] as const;

type TabKey = (typeof TABS)[number][0];

function isTab(v: unknown): v is TabKey {
  return TABS.some(([k]) => k === v);
}

/** GroupDetailPage follows the SchemaBuilder mockup's group page. Editing
 * tabs are keyed by the group's revision, so a successful save (which
 * bumps the revision) starts them over from what the server stored. */
export function GroupDetailPage() {
  const { org, id } = useParams({ strict: false }) as { org: string; id: string };
  const search = useSearch({ strict: false }) as { tab?: string };
  const navigate = useNavigate();
  const tab: TabKey = isTab(search.tab) ? search.tab : 'schema';
  const group = useGroup(org, id);
  const groups = useGroups(org);
  const orgInfo = useOrg(org);
  // Gates SettingsTab's own Jump host picker's nested Credential create
  // row (BastionDialog) — credentials.write is admin, stricter than the
  // inventory.write this page's own writes need (v0.4g Task 7 fix round).
  const canCreateCredential = may(orgInfo.data?.org.role, 'credentials.write');

  const guard = useQueryGuard(group);
  if (guard) return guard;
  if (!group.data) return <QueryLoading />;
  const g = group.data.group;
  const all = groups.data?.groups ?? [];
  const parent = all.find((x) => x.id === g.parent_id);
  const own = all.find((x) => x.id === g.id);
  const children = all.filter((x) => x.parent_id === g.id);

  return (
    <>
      <div className="flex flex-col gap-2">
        <div className="flex items-center gap-3">
          <h1 className="font-mono text-xl font-bold">{g.name}</h1>
          <Tag>inventory group</Tag>
        </div>
        <span className="text-xs text-muted-foreground">
          parent: {parent?.name ?? 'none'} · {own?.members ?? 0} members · {children.length} children
          {g.description ? ` · ${g.description}` : ''}
        </span>
      </div>
      <div role="tablist" className="flex gap-6 border-b border-border">
        {TABS.map(([key, label]) => (
          <button
            key={key}
            type="button"
            role="tab"
            aria-selected={key === tab}
            className={`border-b-2 py-2.5 text-[13px] ${key === tab ? 'border-primary font-semibold text-foreground' : 'border-transparent font-medium text-muted-foreground'}`}
            onClick={() => void navigate({ to: '/o/$org/groups/$id', params: { org, id }, search: { tab: key } })}
          >
            {label}
          </button>
        ))}
      </div>
      <div role="tabpanel">
        {tab === 'schema' && <SchemaTab key={`schema-${g.revision}`} org={org} group={g} />}
        {tab === 'values' && <ValuesTab key={`values-${g.revision}`} org={org} group={g} />}
        {tab === 'members' && <MembersTab org={org} group={g} />}
        {tab === 'children' && <ChildrenTab org={org} group={g} groups={all} />}
        {tab === 'preview' && <PreviewTab org={org} group={g} />}
        {tab === 'settings' && (
          <SettingsTab key={`settings-${g.revision}`} org={org} group={g} groups={all} canCreateCredential={canCreateCredential} />
        )}
      </div>
    </>
  );
}
