import { useId, useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { SlidersHorizontal } from 'lucide-react';
import { Card, GridHeader } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { DisabledHint } from '../../ui/detail-layout.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useT } from '../../i18n/useT.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useMonitoringGroups } from '../../api/hooks/useChecks.ts';
import { useDeleteRule, useRuleMetrics, useRules } from '../../api/hooks/useRules.ts';
import { may } from '../../lib/roles.ts';
import type { Rule } from '../../api/types.ts';
import { RuleDialog } from './RuleDialog.tsx';
import { RuleRow } from './RuleRow.tsx';
import { ChecksTabs } from './ChecksTabs.tsx';

const COLUMNS_BASE = 'minmax(0, 1fr) 90px minmax(0, 1.2fr) 90px 100px';
const COLUMNS_WITH_MENU = `${COLUMNS_BASE} 40px`;

/** groupByMetric buckets rules by the metric they watch, preserving each
 * metric's first-seen order — the same shape ActivityPage's groupByDay
 * uses for its own day sections. Grouping this way, rather than a flat
 * list, is the point: two rules on the same metric (an org-wide warning
 * and a host-scoped override, say) sit in one section, so which one wins
 * is visible without opening either. */
function groupByMetric(rules: Rule[]): Array<[string, Rule[]]> {
  const map = new Map<string, Rule[]>();
  for (const r of rules) {
    const bucket = map.get(r.metric) ?? [];
    bucket.push(r);
    map.set(r.metric, bucket);
  }
  return [...map.entries()];
}

/**
 * RulesPage is the third tab beside Checks and Monitoring groups (v0.3b-2
 * spec): the thresholds that turn a measured value into a warning or
 * critical state. Same permissions as check definitions — a viewer reads,
 * an operator writes (internal/api/rule_handlers.go reuses checks.read /
 * checks.write rather than a permission of its own).
 */
export function RulesPage() {
  const t = useT();
  const { org } = useParams({ strict: false }) as { org: string };
  const toast = useToast();
  const orgInfo = useOrg(org);
  const role = orgInfo.data?.org.role;
  const canRead = may(role, 'checks.read');
  const canWrite = may(role, 'checks.write');
  const rules = useRules(org, !!orgInfo.data && canRead);
  const groups = useMonitoringGroups(org, !!orgInfo.data && canRead);
  const metrics = useRuleMetrics(org, !!orgInfo.data && canRead);
  const remove = useDeleteRule(org);
  const [editing, setEditing] = useState<{ rule?: Rule } | null>(null);
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
  const groupList = groups.data?.groups ?? [];
  const metricList = metrics.data?.metrics ?? [];
  const grouped = groupByMetric(list);
  const columns = canWrite ? COLUMNS_WITH_MENU : COLUMNS_BASE;
  // Nothing reports a metric yet: say why New rule is disabled (a viewer has
  // no button, so nothing to explain).
  const noMetrics = canWrite && metrics.isSuccess && metricList.length === 0;

  async function onDelete(r: Rule) {
    const seeded = r.origin === 'seed' ? t.checks.rules.deleteSeeded : '';
    if (!confirm(`${t.checks.rules.deleteConfirm(r.name)}${seeded}`)) return;
    try {
      await remove.mutateAsync(r.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.checks.deleteFailed, 'destructive');
    }
  }

  return (
    <>
      {header}
      <div className="flex items-center justify-between">
        <span className="text-[13px] text-muted-foreground">
          {t.checks.rules.summary(list.length)}
        </span>
        {canWrite && (
          <Button variant="primary" onClick={() => setEditing({})} disabled={metricList.length === 0} aria-describedby={noMetrics ? hintId : undefined}>
            <SlidersHorizontal size={15} /> {t.checks.rules.newRule}
          </Button>
        )}
      </div>
      {noMetrics && (
        <div id={hintId}>
          <DisabledHint title={t.checks.rules.noMetricsTitle} body={t.checks.rules.noMetricsBody} />
        </div>
      )}
      <Card>
        <GridHeader columns={columns}>
          <span>{t.checks.rules.columns.rule}</span>
          <span>{t.checks.rules.columns.severity}</span>
          <span>{t.checks.rules.columns.appliesTo}</span>
          <span>{t.checks.rules.columns.for}</span>
          <span>{t.checks.rules.columns.state}</span>
          {canWrite && <span />}
        </GridHeader>
        {grouped.map(([metric, rulesForMetric]) => (
          <div key={metric} className="flex flex-col">
            <div className="border-b border-border bg-background px-4 py-2 font-mono text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">
              {metric}
            </div>
            {rulesForMetric.map((r) => (
              <RuleRow
                key={r.id}
                org={org}
                rule={r}
                columns={columns}
                groups={groupList}
                canWrite={canWrite}
                onEdit={(x) => setEditing({ rule: x })}
                onDelete={(x) => void onDelete(x)}
              />
            ))}
          </div>
        ))}
        {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.checks.rules.empty}</p>}
      </Card>
      {editing && <RuleDialog org={org} rule={editing.rule} metrics={metricList} groups={groupList} onClose={() => setEditing(null)} />}
    </>
  );
}
