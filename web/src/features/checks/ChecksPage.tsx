import { useParams } from '@tanstack/react-router';
import { Activity } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { DataList } from '../../ui/data-list/index.ts';
import { ListToolbar, ResetFilters, ResultCount, SearchInput } from '../../ui/list-toolbar.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useT } from '../../i18n/useT.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useChecks, useMonitoringGroups } from '../../api/hooks/useChecks.ts';
import { may } from '../../lib/roles.ts';
import { FilterSelect } from '../lists/FilterSelect.tsx';
import { useListFilters } from '../lists/filterSearch.ts';
import { useListView } from '../lists/useListView.ts';
import { ChecksTabs } from './ChecksTabs.tsx';
import { checkColumns } from './checkColumns.tsx';
import { CHECKS_FILTERS, filterChecks } from './checksSearch.ts';
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
  const { filters, active, setFilter, reset } = useListFilters(CHECKS_FILTERS);
  const view = useListView('checks', columns, { mode: 'client', rowKey: (c) => c.id, resetKey: JSON.stringify(filters) });

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
  const shown = filterChecks(list, filters);
  const { rows, total } = view.view(view.ready ? shown : undefined);
  const f = t.checks.list.filters;
  // A plugin only the URL names (a stale link) stays pickable so it can be cleared.
  const plugins = [...new Set([...list.map((c) => c.plugin), ...(filters.plugin ? [filters.plugin] : [])])].sort();

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
        onRowActivate={actions.edit}
        // One key may be bound several times (the organization's definition
        // and a host's override): the binding tells each row's menu apart.
        rowLabel={(c) => `${c.key} (${bindingText(c, groupList, t)})`}
        loading={!view.ready}
        empty={active ? t.checks.list.noMatch : t.checks.list.empty}
        toolbar={
          <ListToolbar count={<ResultCount active={active} shown={shown.length} total={list.length} noun={t.checks.list.count} />}>
            <SearchInput
              label={t.checks.list.search}
              placeholder={t.checks.list.filterPlaceholder}
              value={filters.q ?? ''}
              onChange={(q) => setFilter('q', q || undefined, { replace: true })}
            />
            <FilterSelect
              label={f.plugin}
              allLabel={f.allPlugins}
              options={plugins.map((p) => ({ value: p, label: p }))}
              value={filters.plugin}
              onChange={(v) => setFilter('plugin', v)}
            />
            <FilterSelect
              label={f.state}
              allLabel={f.anyState}
              options={[
                { value: 'enabled', label: t.checks.enabled },
                { value: 'disabled', label: t.checks.disabled },
              ]}
              value={filters.state}
              onChange={(v) => setFilter('state', v as typeof filters.state)}
            />
            {active && <ResetFilters onReset={reset} />}
          </ListToolbar>
        }
        {...view.listProps(total, t.checks.list.count)}
      />
      {actions.dialogs}
    </>
  );
}
