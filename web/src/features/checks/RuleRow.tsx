import { Link } from '@tanstack/react-router';
import { Pill, Tag } from '../../ui/badge.tsx';
import { RowMenu, RowMenuItem, StaticListRow } from '../../ui/list-row.tsx';
import { useT } from '../../i18n/useT.ts';
import type { MonitoringGroup, Rule } from '../../api/types.ts';
import { intervalLabel } from './interval.ts';

/** RuleRow is one rule. It opens nothing (there is no rule page), so its cells
 * keep the target link. A writer's ⋯ menu offers Edit and Delete…; `columns`
 * reserves the menu's 40px as its last track, left empty here. */
export function RuleRow({
  org, rule: r, columns, groups, canWrite, onEdit, onDelete,
}: {
  org: string;
  rule: Rule;
  columns: string;
  groups: MonitoringGroup[];
  canWrite: boolean;
  onEdit: (r: Rule) => void;
  onDelete: (r: Rule) => void;
}) {
  const t = useT();
  return (
    <StaticListRow
      columns={columns}
      menu={
        canWrite && (
          <RowMenu label={t.common.rowActions(r.name)}>
            <RowMenuItem onSelect={() => onEdit(r)}>{t.checks.edit}</RowMenuItem>
            <RowMenuItem onSelect={() => onDelete(r)} destructive>
              {t.checks.deleteItem}
            </RowMenuItem>
          </RowMenu>
        )
      }
    >
      <span className="flex min-w-0 items-center gap-2">
        <span className="truncate text-[13px] text-foreground" data-row>
          {r.name}
        </span>
        <Tag>{t.checks.rules.origin[r.origin]}</Tag>
      </span>
      <span>
        <Pill tone={r.severity === 'crit' ? 'crit' : 'warn'}>{t.checks.severity[r.severity]}</Pill>
      </span>
      <RuleTarget org={org} rule={r} groups={groups} />
      <span className="text-[13px]">{intervalLabel(r.for_seconds, t)}</span>
      <span>{r.enabled ? <Pill tone="ok">{t.checks.enabled}</Pill> : <Pill tone="unknown">{t.checks.disabled}</Pill>}</span>
      {canWrite && <span />}
    </StaticListRow>
  );
}

/** RuleTarget renders a rule's scope as what it applies to — the
 * organization, a named monitoring group, a named host — never as the raw
 * id the API carries, mirroring CheckRow's own Binding component.
 * RuleDTO now carries host_name and group_name directly (backlog 3.30),
 * the same enrichment checkDefinitionDTO already had; a rule whose host or
 * group was deleted falls back to the shortened id exactly as it did
 * before this field existed, so a name that was never resolved reads no
 * differently from one that no longer resolves. The group list is still
 * consulted as the fallback source for a group name: group_name is absent
 * exactly when the group cannot be read, and the organization's own,
 * separately loaded monitoring-group list may still know it in the narrow
 * window between the two reads. */
function RuleTarget({ org, rule, groups }: { org: string; rule: Rule; groups: MonitoringGroup[] }) {
  const t = useT();
  if (rule.scope === 'group' && rule.group_id) {
    const group = groups.find((g) => g.id === rule.group_id);
    return (
      <Link to="/o/$org/checks/monitoring-groups" params={{ org }} className="truncate text-[13px] text-foreground hover:text-primary">
        {rule.group_name ?? group?.name ?? t.checks.groupFallback(rule.group_id.slice(0, 8))}
      </Link>
    );
  }
  if ((rule.scope === 'host' || rule.scope === 'item') && rule.host_id) {
    return (
      <Link
        to="/o/$org/hosts/$id"
        params={{ org, id: rule.host_id }}
        className="truncate font-mono text-[13px] text-foreground hover:text-primary"
      >
        {rule.host_name ?? rule.host_id.slice(0, 8)}
        {rule.scope === 'item' && rule.item ? ` / ${rule.item}` : ''}
      </Link>
    );
  }
  return <span className="truncate text-[13px] text-muted-foreground">{t.checks.organization}</span>;
}
