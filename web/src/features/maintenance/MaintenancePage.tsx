import { useParams } from '@tanstack/react-router';
import { Wrench } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { DataList } from '../../ui/data-list/index.ts';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useT } from '../../i18n/useT.ts';
import { useFormat } from '../../i18n/format.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useMaintenanceWindows } from '../../api/hooks/useMaintenance.ts';
import { may } from '../../lib/roles.ts';
import { useListView } from '../lists/useListView.ts';
import { useWindowListActions } from './useWindowListActions.tsx';
import { windowColumns } from './windowColumns.tsx';

/** MaintenancePage lists the organization's windows (spec 2026-09-18 §6
 * and v0.4a decision 3): why each exists, what it covers, when, and
 * whether it is active now.
 *
 * "Why" is a column rather than something only the edit dialog knows
 * (v0.4a final review, Important): the two reasons do entirely different
 * things — a maintenance window lets schedules skip runs on the hosts it
 * covers, a silence window mutes their alerts and leaves runs alone — and
 * a list that rendered them identically left an operator with no way to
 * tell one from the other short of opening each in turn. List id
 * `maintenance`: one request, sorted and paged in the browser. */
export function MaintenancePage() {
  const t = useT();
  const fmt = useFormat();
  const { org } = useParams({ strict: false }) as { org: string };
  const orgInfo = useOrg(org);
  const canWrite = may(orgInfo.data?.org.role, 'maintenance.write');
  const windows = useMaintenanceWindows(org);
  const actions = useWindowListActions(org, canWrite);
  const columns = windowColumns(t, fmt);
  const view = useListView('maintenance', columns, { mode: 'client', rowKey: (w) => w.id });

  const guard = useQueryGuard(windows);
  if (guard) return guard;
  if (!windows.data) return <QueryLoading />;
  const list = windows.data.windows ?? [];
  const { rows, total } = view.view(view.ready ? list : undefined);

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold">{t.maintenance.title}</h1>
          <span className="text-[13px] text-muted-foreground">
            {t.maintenance.active(list.filter((w) => w.active_now).length)}
          </span>
        </div>
        {canWrite && (
          <Button variant="primary" onClick={actions.openNew}>
            <Wrench size={15} /> {t.maintenance.newWindow}
          </Button>
        )}
      </div>
      <DataList
        label={t.maintenance.label}
        columns={columns}
        rows={rows}
        rowKey={(w) => w.id}
        rowActions={actions.rowActions}
        rowLabel={(w) => w.name}
        loading={!view.ready}
        empty={t.maintenance.empty}
        {...view.listProps(total, t.maintenance.count)}
      />
      {actions.dialogs}
    </>
  );
}
