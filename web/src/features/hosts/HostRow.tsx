import { ListRow, RowMenu, RowMenuItem } from '../../ui/list-row.tsx';
import { Tag } from '../../ui/badge.tsx';
import { CapabilityCell } from './capabilities.tsx';
import { useHostActions } from './useHostActions.tsx';
import { useHostTags } from '../../api/hooks/useTags.ts';
import type { Host } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import { useT } from '../../i18n/useT.ts';

function timeAgo(iso: string | null | undefined, t: Messages): string {
  if (!iso) return '—';
  const seconds = Math.max(0, Math.floor((Date.now() - new Date(iso).getTime()) / 1000));
  if (seconds < 60) return t.hosts.row.seconds(seconds);
  if (seconds < 3600) return t.hosts.row.minutes(Math.floor(seconds / 60));
  if (seconds < 86400) return t.hosts.row.hours(Math.floor(seconds / 3600));
  return t.hosts.row.days(Math.floor(seconds / 86400));
}

export function HostRow({
  org,
  host,
  columns,
  zoneName,
  agentStatus,
}: {
  org: string;
  host: Host;
  columns: string;
  /** The host's zone slug, resolved by the caller (HostsListPage, which
   * already loads the org's zones for the "Add host" dialog) — cheap fix,
   * task-15a-report.md fix round 1: this used to show a raw UUID prefix. */
  zoneName?: string;
  /** The status of this host's agent, resolved by the caller from its one
   * org-wide agents query (v0.3a). Undefined means the host runs no agent
   * — which is what the dot showed for every host before there was any
   * monitoring backend at all. */
  agentStatus?: string;
}) {
  const t = useT();
  const tags = useHostTags(org, host.id);
  const actions = useHostActions(org, host);
  const agentStatusLabel = agentStatus && agentStatus in t.agents.status ? t.agents.status[agentStatus as keyof typeof t.agents.status] : (agentStatus ?? '');

  return (
    <>
      <ListRow
        to="/o/$org/hosts/$id"
        params={{ org, id: host.id }}
        columns={columns}
        label={host.name}
        menu={
          <RowMenu label={t.hosts.row.actions(host.name)}>
            <RowMenuItem onSelect={actions.open}>{t.hosts.row.open}</RowMenuItem>
            {actions.canEdit && <RowMenuItem onSelect={actions.edit}>{t.hosts.row.edit}</RowMenuItem>}
            {actions.canRun && <RowMenuItem onSelect={actions.runCommand}>{t.hosts.row.runCommand}</RowMenuItem>}
            {actions.canRun && <RowMenuItem onSelect={actions.runTemplate}>{t.hosts.row.runTemplate}</RowMenuItem>}
            {actions.canSwitch && (
              <>
                <RowMenuItem onSelect={actions.toggleMonitoring}>
                  {host.monitored ? t.hosts.row.monitoringOff : t.hosts.row.monitoringOn}
                </RowMenuItem>
                <RowMenuItem onSelect={actions.toggleManagement}>
                  {host.managed ? t.hosts.row.managementOff : t.hosts.row.managementOn}
                </RowMenuItem>
              </>
            )}
            {actions.canDelete && (
              <RowMenuItem onSelect={actions.remove} destructive>
                {t.hosts.row.delete}
              </RowMenuItem>
            )}
          </RowMenu>
        }
      >
        {/* The host's agent is what can honestly claim a state (v0.3a): it
           reports on a heartbeat, and the server decides online from its
           age. A host without an agent stays unknown — remote checks
           (v0.3b) are what will speak for those. */}
        <span
          className="h-2 w-2 rounded-full"
          style={{ background: agentStatus === 'online' ? 'var(--px-ok)' : agentStatus ? 'var(--px-warn)' : 'var(--px-unknown)' }}
          title={agentStatus ? t.hosts.row.agentTitle(agentStatusLabel) : t.hosts.row.unknown}
        />
        <span className="truncate font-mono text-xs font-semibold text-foreground">{host.name}</span>
        <span className="truncate font-mono text-xs text-muted-foreground">{zoneName ?? host.zone_id.slice(0, 8)}</span>
        <div className="flex flex-wrap gap-1">
          {(tags.data?.tags ?? []).map((tag) => (
            <Tag key={tag.key}>
              {tag.key}={tag.value}
            </Tag>
          ))}
        </div>
        <CapabilityCell kind="managed" on={host.managed} />
        <CapabilityCell kind="monitored" on={host.monitored} />
        <span className="font-mono text-[11px] text-muted-foreground">{timeAgo(host.last_seen_at, t)}</span>
        {/* The menu sits over this cell, outside the anchor. */}
        <span />
      </ListRow>
      {actions.dialogs}
    </>
  );
}
