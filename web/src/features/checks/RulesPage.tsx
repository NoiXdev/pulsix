import { useId } from 'react';
import { useParams } from '@tanstack/react-router';
import { SlidersHorizontal } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { DataList } from '../../ui/data-list/index.ts';
import { ListToolbar, ResetFilters, ResultCount, SearchInput } from '../../ui/list-toolbar.tsx';
import { DisabledHint } from '../../ui/detail-layout.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useT } from '../../i18n/useT.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useMonitoringGroups } from '../../api/hooks/useChecks.ts';
import { useRuleMetrics, useRules } from '../../api/hooks/useRules.ts';
import { may } from '../../lib/roles.ts';
import { FilterSelect } from '../lists/FilterSelect.tsx';
import { useListFilters } from '../lists/filterSearch.ts';
import { useListView } from '../lists/useListView.ts';
import { RuleDialog } from './RuleDialog.tsx';
import { ChecksTabs } from './ChecksTabs.tsx';
import { orderByMetric } from './ruleOrder.ts';
import { ruleColumns } from './ruleColumns.tsx';
import { RULES_FILTERS, filterRules } from './rulesSearch.ts';
import { useRuleListActions } from './useRuleListActions.tsx';

/**
 * RulesPage is the third tab beside Checks and Monitoring groups (v0.3b-2
 * spec): the thresholds that turn a measured value into a warning or
 * critical state. Same permissions as check definitions — a viewer reads,
 * an operator writes (internal/api/rule_handlers.go reuses checks.read /
 * checks.write rather than a permission of its own). List id `rules`: one request, sorted
 * and paged in the browser.
 */
export function RulesPage() {
  const t = useT();
  const { org } = useParams({ strict: false }) as { org: string };
  const orgInfo = useOrg(org);
  const role = orgInfo.data?.org.role;
  const canRead = may(role, 'checks.read');
  const canWrite = may(role, 'checks.write');
  const rules = useRules(org, !!orgInfo.data && canRead);
  const groups = useMonitoringGroups(org, !!orgInfo.data && canRead);
  const metrics = useRuleMetrics(org, !!orgInfo.data && canRead);
  const groupList = groups.data?.groups ?? [];
  const metricList = metrics.data?.metrics ?? [];
  const actions = useRuleListActions(org, canWrite, (rule, onClose) => (
    <RuleDialog org={org} rule={rule} metrics={metricList} groups={groupList} onClose={onClose} />
  ));
  const columns = ruleColumns({ org, t, groups: groupList });
  const { filters, active, setFilter, reset } = useListFilters(RULES_FILTERS);
  const view = useListView('rules', columns, { mode: 'client', rowKey: (r) => r.id, resetKey: JSON.stringify(filters) });
  const hintId = useId();

  // Built before any early return, and carried by every one of them: a
  // failed list must not take ChecksTabs with it and leave no link to the
  // section's other pages (the same fix ChecksPage and
  // MonitoringGroupsPage already carry — review finding 5).
  const header = (
    <div className="flex flex-col gap-2">
      <h1 className="text-xl font-semibold">{t.checks.title}</h1>
      <ChecksTabs org={org} />
    </div>
  );
  const orgGuard = useQueryGuard(orgInfo);
  const rulesGuard = useQueryGuard(rules);
  const guard = orgGuard ?? rulesGuard;
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
        <p className="text-sm text-muted-foreground">{t.checks.rules.noAccess}</p>
      </>
    );
  }
  if (!rules.data) {
    return (
      <>
        {header}
        <QueryLoading />
      </>
    );
  }
  const list = rules.data.rules ?? [];
  const shown = filterRules(list, filters);
  const { rows, total } = view.view(view.ready ? orderByMetric(shown) : undefined);
  const f = t.checks.rules.filters;
  // Nothing reports a metric yet: say why New rule is disabled (a viewer has
  // no button, so nothing to explain).
  const noMetrics = canWrite && metrics.isSuccess && metricList.length === 0;

  return (
    <>
      {header}
      <div className="flex items-center justify-between">
        <span className="text-[13px] text-muted-foreground">
          {t.checks.rules.summary(list.length)}
        </span>
        {canWrite && (
          <Button variant="primary" onClick={actions.openNew} disabled={metricList.length === 0} aria-describedby={noMetrics ? hintId : undefined}>
            <SlidersHorizontal size={15} /> {t.checks.rules.newRule}
          </Button>
        )}
      </div>
      {noMetrics && (
        <div id={hintId}>
          <DisabledHint title={t.checks.rules.noMetricsTitle} body={t.checks.rules.noMetricsBody} />
        </div>
      )}
      <DataList
        label={t.checks.rules.label}
        columns={columns}
        rows={rows}
        rowKey={(r) => r.id}
        rowActions={actions.rowActions}
        rowLabel={(r) => r.name}
        loading={!view.ready}
        empty={active ? t.checks.rules.noMatch : t.checks.rules.empty}
        toolbar={
          <ListToolbar count={<ResultCount active={active} shown={shown.length} total={list.length} noun={t.checks.rules.count} />}>
            <SearchInput
              label={t.checks.rules.search}
              placeholder={t.checks.rules.filterPlaceholder}
              value={filters.q ?? ''}
              onChange={(q) => setFilter('q', q || undefined, { replace: true })}
            />
            <FilterSelect
              label={f.severity}
              allLabel={f.anySeverity}
              options={[
                { value: 'crit', label: t.checks.severity.crit },
                { value: 'warn', label: t.checks.severity.warn },
              ]}
              value={filters.severity}
              onChange={(v) => setFilter('severity', v as typeof filters.severity)}
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
        {...view.listProps(total, t.checks.rules.count)}
      />
      {actions.dialogs}
    </>
  );
}
