import { useNavigate, useParams, useSearch } from '@tanstack/react-router';
import { Tag } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useGroup, useGroups } from '../../api/hooks/useInventory.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { may } from '../../lib/roles.ts';
import { SchemaSection } from './sections/SchemaSection.tsx';
import { ValuesSection } from './sections/ValuesSection.tsx';
import { MembersSection } from './sections/MembersSection.tsx';
import { ChildrenSection } from './sections/ChildrenSection.tsx';
import { PreviewSection } from './sections/PreviewSection.tsx';
import { GroupSettingsDialog } from './GroupSettingsDialog.tsx';
import { useT } from '../../i18n/useT.ts';

const TAB_KEYS = ['schema', 'values', 'members', 'children', 'preview', 'settings'] as const;

type TabKey = (typeof TAB_KEYS)[number];

function isTab(v: unknown): v is TabKey {
  return TAB_KEYS.some((k) => k === v);
}

/** GroupDetailPage follows the SchemaBuilder mockup's group page. Editing
 * tabs are keyed by the group's revision, so a successful save (which
 * bumps the revision) starts them over from what the server stored. */
export function GroupDetailPage() {
  const t = useT();
  const { org, id } = useParams({ strict: false }) as { org: string; id: string };
  const search = useSearch({ strict: false }) as { tab?: string };
  const navigate = useNavigate();
  const tab: TabKey = isTab(search.tab) ? search.tab : 'schema';
  const group = useGroup(org, id);
  const groups = useGroups(org);
  const orgInfo = useOrg(org);
  // Gates GroupSettingsDialog's own Jump host create row (POST /bastions is
  // credentials.write too, see sshconf_handlers.go) and, forwarded, its
  // nested Credential create row inside BastionDialog — credentials.write
  // is admin, stricter than the inventory.write this page's own writes
  // need (v0.4g Task 7 and 10 fix rounds).
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
          <Tag>{t.inventory.detail.tag}</Tag>
        </div>
        <span className="text-xs text-muted-foreground">
          {t.inventory.detail.header(parent?.name ?? t.inventory.detail.noParent, own?.members ?? 0, children.length)}
          {g.description ? ` · ${g.description}` : ''}
        </span>
      </div>
      <div role="tablist" className="flex gap-6 border-b border-border">
        {TAB_KEYS.map((key) => (
          <button
            key={key}
            type="button"
            role="tab"
            aria-selected={key === tab}
            className={`border-b-2 py-2.5 text-[13px] ${key === tab ? 'border-primary font-semibold text-foreground' : 'border-transparent font-medium text-muted-foreground'}`}
            onClick={() => void navigate({ to: '/o/$org/groups/$id', params: { org, id }, search: { tab: key } })}
          >
            {t.inventory.detail.tabs[key]}
          </button>
        ))}
      </div>
      <div role="tabpanel">
        {tab === 'schema' && <SchemaSection key={`schema-${g.revision}`} org={org} group={g} />}
        {tab === 'values' && <ValuesSection key={`values-${g.revision}`} org={org} group={g} />}
        {tab === 'members' && <MembersSection org={org} group={g} />}
        {tab === 'children' && <ChildrenSection org={org} group={g} groups={all} />}
        {tab === 'preview' && <PreviewSection org={org} group={g} />}
        {tab === 'settings' && (
          <GroupSettingsDialog key={`settings-${g.revision}`} org={org} group={g} groups={all} canCreateCredential={canCreateCredential} />
        )}
      </div>
    </>
  );
}
