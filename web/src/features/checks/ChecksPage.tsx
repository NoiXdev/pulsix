import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { Activity } from 'lucide-react';
import { Card, GridHeader } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useT } from '../../i18n/useT.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useChecks, useDeleteCheck, useMonitoringGroups } from '../../api/hooks/useChecks.ts';
import { may } from '../../lib/roles.ts';
import type { CheckDefinition } from '../../api/types.ts';
import { CheckDialog } from './CheckDialog.tsx';
import { CheckRow } from './CheckRow.tsx';
import { ChecksTabs } from './ChecksTabs.tsx';
import { useConfirm } from '../../ui/confirmContext.ts';

const COLUMNS_BASE = 'minmax(0, 1fr) 110px minmax(0, 1.1fr) 90px 110px';
const COLUMNS_WITH_MENU = `${COLUMNS_BASE} 40px`;

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
  const ask = useConfirm();
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
  const columns = canWrite ? COLUMNS_WITH_MENU : COLUMNS_BASE;

  async function onDelete(c: CheckDefinition) {
    const seeded = c.origin === 'seed' ? t.checks.list.deleteSeeded : '';
    if (!(await ask({ message: `${t.checks.list.deleteConfirm(c.key)}${seeded}`, confirmLabel: t.common.delete, destructive: true }))) return;
    // c.origin === 'shared' never reaches here: CheckRow offers no Delete…
    // item for a projection (spec v0.2f decision 6).
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
        <GridHeader columns={columns}>
          <span>{t.checks.list.columns.check}</span>
          <span>{t.checks.list.columns.plugin}</span>
          <span>{t.checks.list.columns.bindsTo}</span>
          <span>{t.checks.list.columns.interval}</span>
          <span>{t.checks.list.columns.state}</span>
          {canWrite && <span />}
        </GridHeader>
        {list.map((c) => (
          <CheckRow
            key={c.id}
            org={org}
            check={c}
            columns={columns}
            groups={groupList}
            canWrite={canWrite}
            onEdit={(x) => setEditing({ check: x })}
            onCopy={(x) => setEditing({ check: x, copy: true })}
            onDelete={(x) => void onDelete(x)}
          />
        ))}
        {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.checks.list.empty}</p>}
      </Card>
      {editing && <CheckDialog org={org} check={editing.check} copy={editing.copy} groups={groupList} onClose={() => setEditing(null)} />}
    </>
  );
}
