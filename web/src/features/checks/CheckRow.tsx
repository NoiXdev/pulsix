import { Link } from '@tanstack/react-router';
import { Pill, Tag } from '../../ui/badge.tsx';
import { RowMenu, RowMenuItem, StaticListRow } from '../../ui/list-row.tsx';
import { useT } from '../../i18n/useT.ts';
import type { CheckDefinition, MonitoringGroup } from '../../api/types.ts';
import { intervalLabel } from './interval.ts';
import { bindingText } from './binding.ts';

/** CheckRow is one check definition. It opens nothing (there is no check
 * page), so its cells keep the binding link. A writer's ⋯ menu offers Edit,
 * Copy for a seeded or shared definition (its key and plugin are fixed, so
 * customising it means copying) and Delete… for anything but a shared one;
 * `columns` reserves the menu's 40px as its last track, left empty here. The
 * menu is named by the key and the binding: one key may be bound several
 * times (the organization's definition and a host's override), and each
 * row's menu must still be told apart. */
export function CheckRow({
  org, check: c, columns, groups, canWrite, onEdit, onCopy, onDelete,
}: {
  org: string;
  check: CheckDefinition;
  columns: string;
  groups: MonitoringGroup[];
  canWrite: boolean;
  onEdit: (c: CheckDefinition) => void;
  onCopy: (c: CheckDefinition) => void;
  onDelete: (c: CheckDefinition) => void;
}) {
  const t = useT();
  return (
    <StaticListRow
      columns={columns}
      menu={
        canWrite && (
          <RowMenu label={t.checks.rowActions(c.key, bindingText(c, groups, t))}>
            <RowMenuItem onSelect={() => onEdit(c)}>{t.checks.edit}</RowMenuItem>
            {(c.origin === 'seed' || c.origin === 'shared') && <RowMenuItem onSelect={() => onCopy(c)}>{t.checks.copy}</RowMenuItem>}
            {/* A projection of an installation-owned definition cannot be
                deleted from inside an organization (spec v0.2f decision 6):
                the server refuses it with 403 regardless, and offering an
                item that always fails is the mistake RepoDialog.tsx's own
                `repo && !projected` guard exists to avoid. */}
            {c.origin !== 'shared' && (
              <RowMenuItem onSelect={() => onDelete(c)} destructive>
                {t.checks.deleteItem}
              </RowMenuItem>
            )}
          </RowMenu>
        )
      }
    >
      <span className="flex min-w-0 items-center gap-2">
        <span className="truncate font-mono text-[13px] text-foreground" data-row>
          {c.key}
        </span>
        <Tag>{t.checks.origin[c.origin]}</Tag>
      </span>
      <span className="truncate text-xs">{c.plugin}</span>
      <Binding org={org} check={c} groups={groups} />
      <span className="text-[13px]">{intervalLabel(c.interval_seconds, t)}</span>
      <span>{c.enabled ? <Pill tone="ok">{t.checks.enabled}</Pill> : <Pill tone="unknown">{t.checks.disabled}</Pill>}</span>
      {canWrite && <span />}
    </StaticListRow>
  );
}

/** Binding renders bindingText, as a link to the host or to the monitoring
 * groups for a definition bound to one. */
function Binding({ org, check, groups }: { org: string; check: CheckDefinition; groups: MonitoringGroup[] }) {
  const t = useT();
  const text = bindingText(check, groups, t);
  if (check.binding_kind === 'host' && check.host_id) {
    return (
      <Link
        to="/o/$org/hosts/$id"
        params={{ org, id: check.host_id }}
        className="truncate font-mono text-[13px] text-foreground hover:text-primary"
      >
        {text}
      </Link>
    );
  }
  if (check.binding_kind === 'group' && check.group_id) {
    return (
      <Link to="/o/$org/checks/monitoring-groups" params={{ org }} className="truncate text-[13px] text-foreground hover:text-primary">
        {text}
      </Link>
    );
  }
  return <span className="truncate text-[13px] text-muted-foreground">{text}</span>;
}
