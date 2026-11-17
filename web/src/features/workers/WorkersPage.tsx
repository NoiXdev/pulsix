import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { Cpu } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { DataList } from '../../ui/data-list/index.ts';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useWorkerEnrollments, useWorkers } from '../../api/hooks/useWorkers.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { useFormat } from '../../i18n/format.ts';
import { useListView } from '../lists/useListView.ts';
import { AddWorkerDialog } from './AddWorkerDialog.tsx';
import { enrollmentColumns } from './enrollmentColumns.tsx';
import { useWorkerListActions } from './useWorkerListActions.ts';
import { workerColumns } from './workerColumns.tsx';

/** WorkersPage lists the organization's zone workers (spec 2026-09-18 §4):
 * status (online = seen within 2 minutes), zone, last seen, version,
 * certificate expiry (highlighted under 14 days) and the current job, plus
 * the pending enrollment tokens. Owners and admins add, revoke and delete.
 * Both are lists on the shared list component, fetched whole and sorted and
 * paged in the browser: the workers (list id `workers`) keep their sort and
 * page in the URL, the enrollments (`worker-enrollments`) in component state. */
export function WorkersPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const fmt = useFormat();
  const orgInfo = useOrg(org);
  const role = orgInfo.data?.org.role;
  const canRead = may(role, 'workers.read');
  const canWrite = may(role, 'workers.write');
  const list = useWorkers(org, !!orgInfo.data && canRead);
  const pending = useWorkerEnrollments(org, !!orgInfo.data && canRead);
  const [adding, setAdding] = useState(false);
  const now = new Date();
  const actions = useWorkerListActions(org, canWrite);

  const columns = workerColumns({ t, org, now });
  const view = useListView('workers', columns, { mode: 'client', rowKey: (w) => w.id });
  const enrollColumns = enrollmentColumns(t, fmt);
  const enrollView = useListView('worker-enrollments', enrollColumns, { mode: 'client', urlState: false, rowKey: (e) => e.id });

  const orgGuard = useQueryGuard(orgInfo);
  if (orgGuard) return orgGuard;
  if (!orgInfo.data) return <QueryLoading />;
  if (!canRead) {
    return <p className="text-sm text-muted-foreground">{t.workers.noAccess}</p>;
  }
  if (!list.data) return <QueryLoading />;
  const workers = list.data.workers ?? [];
  const enrollments = pending.data?.enrollments ?? [];
  const workerRows = view.view(view.ready ? workers : undefined);
  const enrollmentRows = enrollView.view(enrollView.ready ? enrollments : undefined);

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold">{t.workers.title}</h1>
          <span className="text-[13px] text-muted-foreground">
            {t.workers.summary(workers.filter((w) => w.status === 'online').length, workers.length)}
          </span>
        </div>
        {canWrite && (
          <Button variant="primary" onClick={() => setAdding(true)}>
            <Cpu size={15} /> {t.workers.add}
          </Button>
        )}
      </div>
      <DataList
        label={t.workers.label}
        columns={columns}
        rows={workerRows.rows}
        rowKey={(w) => w.id}
        rowLabel={(w) => w.name}
        rowActions={actions.workerActions}
        loading={!view.ready}
        empty={t.workers.empty}
        {...view.listProps(workerRows.total, t.workers.count)}
      />
      {enrollments.length > 0 && (
        <DataList
          label={t.workers.enrollmentsLabel}
          columns={enrollColumns}
          rows={enrollmentRows.rows}
          rowKey={(e) => e.id}
          rowLabel={(e) => t.workers.enrollmentName(e.name)}
          rowActions={actions.enrollmentActions}
          loading={!enrollView.ready}
          empty={null}
          {...enrollView.listProps(enrollmentRows.total, t.workers.enrollmentsCount)}
        />
      )}
      {adding && <AddWorkerDialog org={org} onClose={() => setAdding(false)} />}
    </>
  );
}
