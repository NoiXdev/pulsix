import { useId } from 'react';
import { useParams } from '@tanstack/react-router';
import { ShieldOff } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { DataList } from '../../ui/data-list/index.ts';
import { ListToolbar, ResetFilters, ResultCount } from '../../ui/list-toolbar.tsx';
import { DisabledHint } from '../../ui/detail-layout.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useT } from '../../i18n/useT.ts';
import { useFormat } from '../../i18n/format.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useRuleMetrics } from '../../api/hooks/useRules.ts';
import { useItemDecisions } from '../../api/hooks/useItems.ts';
import { may } from '../../lib/roles.ts';
import type { RuleMetric } from '../../api/types.ts';
import { FilterSelect } from '../lists/FilterSelect.tsx';
import { useListFilters } from '../lists/filterSearch.ts';
import { useListView } from '../lists/useListView.ts';
import { ItemDecisionDialog } from './ItemDecisionDialog.tsx';
import { ChecksTabs } from './ChecksTabs.tsx';
import { decisionColumns } from './decisionColumns.tsx';
import { DECISIONS_FILTERS, filterDecisions } from './decisionsSearch.ts';
import { orderByCheck } from './decisionOrder.ts';
import { useDecisionListActions } from './useDecisionListActions.tsx';

/** checkKeysFrom collapses the rule metric catalogue — the only honest
 * source for which check keys this organization resolves (GET
 * /rules/metrics, the same route RuleDialog's own metric picker already
 * trusts) — into the distinct check_key values the pattern editor's picker
 * offers. Reusing it here keeps this picker from drifting the moment a
 * plugin gains or loses a metric, rather than hardcoding the plugin list a
 * second time. */
function checkKeysFrom(metrics: Pick<RuleMetric, 'check_key'>[]): string[] {
  return [...new Set(metrics.map((m) => m.check_key))].sort();
}

/**
 * ItemsPage is the fourth tab beside Checks, Monitoring groups and Rules
 * (v0.3b-3 spec): the decisions that keep an item — a mount, a service
 * unit — from being judged, grouped by the check they govern so a host
 * override sits beside the organization-wide pattern it overrules. Same
 * permissions as rules and check definitions: a viewer reads, an operator
 * writes (internal/api/item_handlers.go reuses checks.read / checks.write
 * rather than a permission of its own). List id `item-decisions`: one request,
 * sorted and paged in the browser.
 */
export function ItemsPage() {
  const t = useT();
  const fmt = useFormat();
  const { org } = useParams({ strict: false }) as { org: string };
  const orgInfo = useOrg(org);
  const role = orgInfo.data?.org.role;
  const canRead = may(role, 'checks.read');
  const canWrite = may(role, 'checks.write');
  const decisions = useItemDecisions(org, !!orgInfo.data && canRead);
  const metrics = useRuleMetrics(org, !!orgInfo.data && canRead);
  const checkKeys = checkKeysFrom(metrics.data?.metrics ?? []);
  const actions = useDecisionListActions(org, canWrite, (decision, onClose) => (
    <ItemDecisionDialog org={org} decision={decision} checkKeys={checkKeys} onClose={onClose} />
  ));
  const columns = decisionColumns({ org, t, fmt });
  const { filters, active, setFilter, reset } = useListFilters(DECISIONS_FILTERS);
  const view = useListView('item-decisions', columns, { mode: 'client', rowKey: (d) => d.id, resetKey: JSON.stringify(filters) });
  const hintId = useId();

  // Built before any early return, and carried by every one of them: a
  // failed list must not take ChecksTabs with it and leave no link to the
  // section's other pages (the same fix RulesPage and MonitoringGroupsPage
  // already carry).
  const header = (
    <div className="flex flex-col gap-2">
      <h1 className="text-xl font-semibold">{t.checks.title}</h1>
      <ChecksTabs org={org} />
    </div>
  );
  const orgGuard = useQueryGuard(orgInfo);
  const decisionsGuard = useQueryGuard(decisions);
  const guard = orgGuard ?? decisionsGuard;
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
        <p className="text-sm text-muted-foreground">{t.checks.items.noAccess}</p>
      </>
    );
  }
  if (!decisions.data) {
    return (
      <>
        {header}
        <QueryLoading />
      </>
    );
  }
  const list = decisions.data.decisions ?? [];
  const shown = filterDecisions(list, filters);
  const { rows, total } = view.view(view.ready ? orderByCheck(shown) : undefined);
  const f = t.checks.items.filters;
  // The checks the decisions govern; one only the URL names stays pickable so it can be cleared.
  const checks = [...new Set([...list.map((d) => d.check_key), ...(filters.check ? [filters.check] : [])])].sort();
  // Nothing reports a metric yet: say why New decision is disabled (a viewer
  // has no button, so nothing to explain).
  const noMetrics = canWrite && metrics.isSuccess && checkKeys.length === 0;

  return (
    <>
      {header}
      <div className="flex items-center justify-between">
        <span className="text-[13px] text-muted-foreground">
          {t.checks.items.summary(list.length)}
        </span>
        {canWrite && (
          <Button variant="primary" onClick={actions.openNew} disabled={checkKeys.length === 0} aria-describedby={noMetrics ? hintId : undefined}>
            <ShieldOff size={15} /> {t.checks.items.newDecision}
          </Button>
        )}
      </div>
      {noMetrics && (
        <div id={hintId}>
          <DisabledHint title={t.checks.items.noMetricsTitle} body={t.checks.items.noMetricsBody} />
        </div>
      )}
      <DataList
        label={t.checks.items.label}
        columns={columns}
        rows={rows}
        rowKey={(d) => d.id}
        rowActions={actions.rowActions}
        rowLabel={(d) => `${d.pattern} (${d.scope === 'host' && d.host_id ? d.host_id.slice(0, 8) : t.checks.organization})`}
        loading={!view.ready}
        empty={active ? t.checks.items.noMatch : t.checks.items.empty}
        toolbar={
          <ListToolbar count={active ? <ResultCount shown={shown.length} total={list.length} noun={t.checks.items.count} /> : undefined}>
            <FilterSelect
              label={f.verdict}
              allLabel={f.anyVerdict}
              options={[
                { value: 'watch', label: t.checks.items.verdict.watch },
                { value: 'ignore', label: t.checks.items.verdict.ignore },
              ]}
              value={filters.verdict}
              onChange={(v) => setFilter('verdict', v as typeof filters.verdict)}
            />
            <FilterSelect
              label={f.check}
              allLabel={f.allChecks}
              options={checks.map((c) => ({ value: c, label: c }))}
              value={filters.check}
              onChange={(v) => setFilter('check', v)}
            />
            {active && <ResetFilters onReset={reset} />}
          </ListToolbar>
        }
        {...view.listProps(total, t.checks.items.count)}
      />
      {actions.dialogs}
    </>
  );
}
