import { useState } from 'react';
import { Link, useParams } from '@tanstack/react-router';
import { Activity } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Pill, Tag } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useHost } from '../../api/hooks/useHosts.ts';
import { useChecks, useDeleteCheck, useMonitoringGroups } from '../../api/hooks/useChecks.ts';
import { may } from '../../lib/roles.ts';
import type { CheckDefinition, MonitoringGroup } from '../../api/types.ts';
import { CheckDialog } from './CheckDialog.tsx';
import { ChecksTabs } from './ChecksTabs.tsx';

const COLUMNS = 'minmax(0, 1fr) 110px minmax(0, 1.1fr) 90px 110px 220px';

/** intervalLabel writes a check's period the way an operator says it —
 * "1 min", "5 min", "1 h" — falling back to seconds for anything that is
 * not a whole number of them. */
function intervalLabel(seconds: number): string {
  if (seconds > 0 && seconds % 3600 === 0) return `${seconds / 3600} h`;
  if (seconds > 0 && seconds % 60 === 0) return `${seconds / 60} min`;
  return `${seconds} s`;
}

/**
 * ChecksPage is the organization's check definitions (v0.3b-1 spec, GUI):
 * what every monitored host measures, at which scope and how often.
 * Reading them is a viewer's right; creating, editing and deleting them is
 * an operator's, so a viewer is shown the list with no write affordance at
 * all rather than buttons that would 403 on click.
 */
export function ChecksPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const toast = useToast();
  const orgInfo = useOrg(org);
  const role = orgInfo.data?.org.role;
  const canRead = may(role, 'checks.read');
  const canWrite = may(role, 'checks.write');
  const checks = useChecks(org, !!orgInfo.data && canRead);
  const groups = useMonitoringGroups(org, !!orgInfo.data && canRead);
  const remove = useDeleteCheck(org);
  // `copy` distinguishes "customise this seeded row" from "edit it": the
  // dialog creates in the first case and updates in the second.
  const [editing, setEditing] = useState<{ check?: CheckDefinition; copy?: boolean } | null>(null);

  const orgGuard = useQueryGuard(orgInfo);
  const checksGuard = useQueryGuard(checks);
  if (orgGuard) return orgGuard;
  if (checksGuard) return checksGuard;
  if (!orgInfo.data) return <QueryLoading />;

  const header = (
    <div className="flex flex-col gap-2">
      <h1 className="text-xl font-semibold">Checks</h1>
      <ChecksTabs org={org} />
    </div>
  );
  if (!canRead) {
    return (
      <>
        {header}
        <p className="text-sm text-muted-foreground">Checks are visible to members of this organization.</p>
      </>
    );
  }
  if (!checks.data) return <QueryLoading />;
  const list = checks.data.checks ?? [];
  const groupList = groups.data?.groups ?? [];

  async function onDelete(c: CheckDefinition) {
    const seeded = c.origin === 'seed' ? ' It ships with every organization and will not come back on its own.' : '';
    if (!confirm(`Delete check ${c.key}? The hosts it reaches stop measuring it.${seeded}`)) return;
    try {
      await remove.mutateAsync(c.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : 'Deleting failed', 'destructive');
    }
  }

  return (
    <>
      {header}
      <div className="flex items-center justify-between">
        <span className="text-[13px] text-muted-foreground">
          {list.length} definitions · a narrower scope overrides a wider one under the same key
        </span>
        {canWrite && (
          <Button variant="primary" onClick={() => setEditing({})}>
            <Activity size={15} /> New check
          </Button>
        )}
      </div>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>Check</span>
          <span>Plugin</span>
          <span>Binds to</span>
          <span>Interval</span>
          <span>State</span>
          <span />
        </GridHeader>
        {list.map((c) => (
          <GridRow key={c.id} columns={COLUMNS}>
            <span className="flex min-w-0 items-center gap-2">
              <span className="truncate font-mono text-[13px] text-foreground">{c.key}</span>
              <Tag>{c.origin}</Tag>
            </span>
            <span className="truncate text-xs">{c.plugin}</span>
            <Binding org={org} check={c} groups={groupList} />
            <span className="text-[13px]">{intervalLabel(c.interval_seconds)}</span>
            <span>{c.enabled ? <Pill tone="ok">enabled</Pill> : <Pill tone="unknown">disabled</Pill>}</span>
            <span className="flex justify-end gap-1">
              {canWrite && (
                <>
                  <Button type="button" variant="ghost" aria-label={`Edit ${c.key}`} onClick={() => setEditing({ check: c })}>
                    Edit
                  </Button>
                  {c.origin === 'seed' && (
                    <Button type="button" variant="ghost" aria-label={`Copy ${c.key}`} onClick={() => setEditing({ check: c, copy: true })}>
                      Copy
                    </Button>
                  )}
                  <Button type="button" variant="destructive" aria-label={`Delete ${c.key}`} onClick={() => void onDelete(c)}>
                    Delete
                  </Button>
                </>
              )}
            </span>
          </GridRow>
        ))}
        {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">No check definitions yet.</p>}
      </Card>
      {editing && <CheckDialog org={org} check={editing.check} copy={editing.copy} groups={groupList} onClose={() => setEditing(null)} />}
    </>
  );
}

/** Binding renders what a definition is bound to as the thing itself — the
 * organization, a named monitoring group, a named host — never as the raw
 * id the API carries. */
function Binding({ org, check, groups }: { org: string; check: CheckDefinition; groups: MonitoringGroup[] }) {
  if (check.binding_kind === 'host' && check.host_id) {
    return <HostBinding org={org} hostId={check.host_id} />;
  }
  if (check.binding_kind === 'group' && check.group_id) {
    const group = groups.find((g) => g.id === check.group_id);
    return (
      <Link to="/o/$org/checks/monitoring-groups" params={{ org }} className="truncate text-[13px] text-foreground hover:text-primary">
        {group?.name ?? `group ${check.group_id.slice(0, 8)}`}
      </Link>
    );
  }
  return <span className="truncate text-[13px] text-muted-foreground">Organization</span>;
}

/**
 * HostBinding names the host a host-bound definition applies to.
 *
 * One request per distinct bound host, not per row: TanStack Query keys
 * these by ['host', org, id], so several definitions on the same host
 * share one fetch. Reading the paginated hosts list instead would only
 * answer for hosts that happen to be on the first page — a fleet larger
 * than one page would show bare ids, which is the thing this avoids.
 * Host-bound definitions are the narrow scope, the exception rather than
 * the bulk of the list.
 */
function HostBinding({ org, hostId }: { org: string; hostId: string }) {
  const host = useHost(org, hostId);
  return (
    <Link
      to="/o/$org/hosts/$id"
      params={{ org, id: hostId }}
      className="truncate font-mono text-[13px] text-foreground hover:text-primary"
    >
      {host.data?.host.name ?? hostId.slice(0, 8)}
    </Link>
  );
}
