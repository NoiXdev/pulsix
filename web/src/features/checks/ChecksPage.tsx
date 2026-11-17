import { useParams } from '@tanstack/react-router';
import { Activity } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { DataList } from '../../ui/data-list/index.ts';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useT } from '../../i18n/useT.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useChecks, useMonitoringGroups } from '../../api/hooks/useChecks.ts';
import { may } from '../../lib/roles.ts';
import { useListView } from '../lists/useListView.ts';
import { ChecksTabs } from './ChecksTabs.tsx';
import { checkColumns } from './checkColumns.tsx';
import { bindingText } from './binding.ts';
import { useCheckListActions } from './useCheckListActions.tsx';

/**
 * ChecksPage is the organization's check definitions (v0.3b-1 spec, GUI):
 * what every monitored host measures, at which scope and how often.
 * Reading them is a viewer's right; creating, editing and deleting them is
 * an operator's, so a viewer is shown the list with no write affordance at
 * all rather than buttons that would 403 on click. List id `checks`: the
 * whole set is one request, sorted and paged in the browser.
 */
export function ChecksPage() {
  const t = useT();
  const { org } = useParams({ strict: false }) as { org: string };
  const orgInfo = useOrg(org);
  const role = orgInfo.data?.org.role;
  const canRead = may(role, 'checks.read');
  const canWrite = may(role, 'checks.write');
  const checks = useChecks(org, !!orgInfo.data && canRead);
  const groups = useMonitoringGroups(org, !!orgInfo.data && canRead);
  const groupList = groups.data?.groups ?? [];
  const actions = useCheckListActions(org, { canWrite, groups: groupList });
  const columns = checkColumns({ org, t, groups: groupList });
  const view = useListView('checks', columns, { mode: 'client', rowKey: (c) => c.id });

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
  const { rows, total } = view.view(view.ready ? list : undefined);

  return (
    <>
      {header}
      <div className="flex items-center justify-between">
        <span className="text-[13px] text-muted-foreground">
          {t.checks.list.summary(list.length)}
        </span>
        {canWrite && (
          <Button variant="primary" onClick={actions.openNew}>
            <Activity size={15} /> {t.checks.list.newCheck}
          </Button>
        )}
      </div>
      <DataList
        label={t.checks.list.label}
        columns={columns}
        rows={rows}
        rowKey={(c) => c.id}
        rowActions={actions.rowActions}
        // One key may be bound several times (the organization's definition
        // and a host's override): the binding tells each row's menu apart.
        rowLabel={(c) => `${c.key} (${bindingText(c, groupList, t)})`}
        loading={!view.ready}
        empty={t.checks.list.empty}
        {...view.listProps(total, t.checks.list.count)}
      />
      {actions.dialogs}
    </>
  );
}
