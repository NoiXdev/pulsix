import { useState } from 'react';
import { Link, useParams } from '@tanstack/react-router';
import { Activity } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Pill, Tag } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useT } from '../../i18n/useT.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useChecks, useDeleteCheck, useMonitoringGroups } from '../../api/hooks/useChecks.ts';
import { may } from '../../lib/roles.ts';
import type { CheckDefinition, MonitoringGroup } from '../../api/types.ts';
import { CheckDialog } from './CheckDialog.tsx';
import { intervalLabel } from './interval.ts';
import { ChecksTabs } from './ChecksTabs.tsx';

const COLUMNS = 'minmax(0, 1fr) 110px minmax(0, 1.1fr) 90px 110px 220px';

/**
 * ChecksPage is the organization's check definitions (v0.3b-1 spec, GUI):
 * what every monitored host measures, at which scope and how often.
 * Reading them is a viewer's right; creating, editing and deleting them is
 * an operator's, so a viewer is shown the list with no write affordance at
 * all rather than buttons that would 403 on click.
 */
export function ChecksPage() {
  const t = useT();
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

  // The header is built before anything can return early, and every early
  // return carries it: a failed list used to replace the whole page,
  // taking ChecksTabs with it and leaving no link to the section's other
  // page but the sidebar (review finding 5).
  const header = (
    <div className="flex flex-col gap-2">
      <h1 className="text-xl font-semibold">{t.checks.title}</h1>
      <ChecksTabs org={org} />
    </div>
  );
  const orgGuard = useQueryGuard(orgInfo);
  const checksGuard = useQueryGuard(checks);
  const guard = orgGuard ?? checksGuard;
  if (guard) {
    return (
      <>
        {header}
        {guard}
      </>
    );
  }
  if (!orgInfo.data) {
    return (
      <>
        {header}
        <QueryLoading />
      </>
    );
  }
  if (!canRead) {
    return (
      <>
        {header}
        <p className="text-sm text-muted-foreground">{t.checks.list.noAccess}</p>
      </>
    );
  }
  if (!checks.data) {
    return (
      <>
        {header}
        <QueryLoading />
      </>
    );
  }
  const list = checks.data.checks ?? [];
  const groupList = groups.data?.groups ?? [];

  async function onDelete(c: CheckDefinition) {
    const seeded = c.origin === 'seed' ? t.checks.list.deleteSeeded : '';
    if (!confirm(`${t.checks.list.deleteConfirm(c.key)}${seeded}`)) return;
    // c.origin === 'shared' never reaches here: canWrite's own Delete button
    // is not rendered for a projection (spec v0.2f decision 6 — the server
    // would refuse it with 403 anyway, but offering a button that always
    // fails is the same mistake RepoDialog's own `repo && !projected` guard
    // avoids).
    try {
      await remove.mutateAsync(c.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.checks.deleteFailed, 'destructive');
    }
  }

  return (
    <>
      {header}
      <div className="flex items-center justify-between">
        <span className="text-[13px] text-muted-foreground">
          {t.checks.list.summary(list.length)}
        </span>
        {canWrite && (
          <Button variant="primary" onClick={() => setEditing({})}>
            <Activity size={15} /> {t.checks.list.newCheck}
          </Button>
        )}
      </div>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>{t.checks.list.columns.check}</span>
          <span>{t.checks.list.columns.plugin}</span>
          <span>{t.checks.list.columns.bindsTo}</span>
          <span>{t.checks.list.columns.interval}</span>
          <span>{t.checks.list.columns.state}</span>
          <span />
        </GridHeader>
        {list.map((c) => (
          <GridRow key={c.id} columns={COLUMNS}>
            <span className="flex min-w-0 items-center gap-2">
              <span className="truncate font-mono text-[13px] text-foreground">{c.key}</span>
              <Tag>{t.checks.origin[c.origin]}</Tag>
            </span>
            <span className="truncate text-xs">{c.plugin}</span>
            <Binding org={org} check={c} groups={groupList} />
            <span className="text-[13px]">{intervalLabel(c.interval_seconds, t)}</span>
            <span>{c.enabled ? <Pill tone="ok">{t.checks.enabled}</Pill> : <Pill tone="unknown">{t.checks.disabled}</Pill>}</span>
            <span className="flex justify-end gap-1">
              {canWrite && (
                <>
                  <Button type="button" variant="ghost" aria-label={t.checks.editAria(c.key)} onClick={() => setEditing({ check: c })}>
                    {t.checks.edit}
                  </Button>
                  {(c.origin === 'seed' || c.origin === 'shared') && (
                    <Button type="button" variant="ghost" aria-label={t.checks.copyAria(c.key)} onClick={() => setEditing({ check: c, copy: true })}>
                      {t.checks.copy}
                    </Button>
                  )}
                  {/* A projection of an installation-owned definition cannot
                      be deleted from inside an organization (spec v0.2f
                      decision 6) — the server refuses with 403 regardless,
                      but offering a button that always fails is the mistake
                      RepoDialog.tsx's own `repo && !projected` guard exists
                      to avoid. */}
                  {c.origin !== 'shared' && (
                    <Button type="button" variant="destructive" aria-label={t.checks.deleteAria(c.key)} onClick={() => void onDelete(c)}>
                      {t.common.delete}
                    </Button>
                  )}
                </>
              )}
            </span>
          </GridRow>
        ))}
        {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.checks.list.empty}</p>}
      </Card>
      {editing && <CheckDialog org={org} check={editing.check} copy={editing.copy} groups={groupList} onClose={() => setEditing(null)} />}
    </>
  );
}

/** Binding renders what a definition is bound to as the thing itself — the
 * organization, a named monitoring group, a named host — never as the raw
 * id the API carries. */
function Binding({ org, check, groups }: { org: string; check: CheckDefinition; groups: MonitoringGroup[] }) {
  const t = useT();
  if (check.binding_kind === 'host' && check.host_id) {
    // host_name comes from the definition itself (internal/api's
    // checkHostNames, one read for the whole page). This used to be a
    // useHost call per row, which on an unpaginated GET /checks meant one
    // request per host-bound definition on every mount — and host binding
    // IS the per-host override, so that count tracks the fleet size
    // (review finding 2).
    return (
      <Link
        to="/o/$org/hosts/$id"
        params={{ org, id: check.host_id }}
        className="truncate font-mono text-[13px] text-foreground hover:text-primary"
      >
        {check.host_name ?? check.host_id.slice(0, 8)}
      </Link>
    );
  }
  if (check.binding_kind === 'group' && check.group_id) {
    const group = groups.find((g) => g.id === check.group_id);
    return (
      <Link to="/o/$org/checks/monitoring-groups" params={{ org }} className="truncate text-[13px] text-foreground hover:text-primary">
        {group?.name ?? t.checks.groupFallback(check.group_id.slice(0, 8))}
      </Link>
    );
  }
  return <span className="truncate text-[13px] text-muted-foreground">{t.checks.organization}</span>;
}
