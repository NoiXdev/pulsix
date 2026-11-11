import { useId, useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { ShieldOff } from 'lucide-react';
import { Card, GridHeader } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { DisabledHint } from '../../ui/detail-layout.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useT } from '../../i18n/useT.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useRuleMetrics } from '../../api/hooks/useRules.ts';
import { useDeleteItemDecision, useItemDecisions } from '../../api/hooks/useItems.ts';
import { may } from '../../lib/roles.ts';
import type { ItemDecision, RuleMetric } from '../../api/types.ts';
import { ItemDecisionDialog } from './ItemDecisionDialog.tsx';
import { ChecksTabs } from './ChecksTabs.tsx';
import { DecisionRow } from './DecisionRow.tsx';
import { useConfirm } from '../../ui/confirmContext.ts';

const COLUMNS_BASE = 'minmax(0, 1fr) 80px minmax(0, 1.3fr) 90px minmax(0, 1.5fr) 110px';
const COLUMNS_WITH_MENU = `${COLUMNS_BASE} 40px`;

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

/** groupByCheck buckets decisions by the check key they govern, preserving
 * first-seen order — the same shape RulesPage's own groupByMetric gives
 * rules, and for the same reason: a host decision overruling an
 * organization one is only visible as an overrule when both sit in the
 * same section. Within a section, an organization-scope decision sorts
 * before every host-scope one, so it reads as the broad stroke the
 * narrower rows sit on top of, rather than one row among many in whatever
 * order the API happened to return them. */
function groupByCheck(decisions: ItemDecision[]): Array<[string, ItemDecision[]]> {
  const map = new Map<string, ItemDecision[]>();
  for (const d of decisions) {
    const bucket = map.get(d.check_key) ?? [];
    bucket.push(d);
    map.set(d.check_key, bucket);
  }
  for (const bucket of map.values()) {
    bucket.sort((a, b) => (a.scope === b.scope ? 0 : a.scope === 'org' ? -1 : 1));
  }
  return [...map.entries()];
}

/**
 * ItemsPage is the fourth tab beside Checks, Monitoring groups and Rules
 * (v0.3b-3 spec): the decisions that keep an item — a mount, a service
 * unit — from being judged, grouped by the check they govern so a host
 * override sits beside the organization-wide pattern it overrules. Same
 * permissions as rules and check definitions: a viewer reads, an operator
 * writes (internal/api/item_handlers.go reuses checks.read / checks.write
 * rather than a permission of its own).
 */
export function ItemsPage() {
  const t = useT();
  const { org } = useParams({ strict: false }) as { org: string };
  const toast = useToast();
  const ask = useConfirm();
  const orgInfo = useOrg(org);
  const role = orgInfo.data?.org.role;
  const canRead = may(role, 'checks.read');
  const canWrite = may(role, 'checks.write');
  const decisions = useItemDecisions(org, !!orgInfo.data && canRead);
  const metrics = useRuleMetrics(org, !!orgInfo.data && canRead);
  const remove = useDeleteItemDecision(org);
  const [editing, setEditing] = useState<{ decision?: ItemDecision } | null>(null);
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
  const checkKeys = checkKeysFrom(metrics.data?.metrics ?? []);
  const grouped = groupByCheck(list);
  const columns = canWrite ? COLUMNS_WITH_MENU : COLUMNS_BASE;
  // Nothing reports a metric yet: say why New decision is disabled (a viewer
  // has no button, so nothing to explain).
  const noMetrics = canWrite && metrics.isSuccess && checkKeys.length === 0;

  async function onDelete(d: ItemDecision) {
    if (!(await ask({ message: t.checks.items.deleteConfirm(t.checks.items.verdict[d.decision], d.pattern), confirmLabel: t.common.delete, destructive: true }))) {
      return;
    }
    try {
      await remove.mutateAsync(d.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.checks.deleteFailed, 'destructive');
    }
  }

  return (
    <>
      {header}
      <div className="flex items-center justify-between">
        <span className="text-[13px] text-muted-foreground">
          {t.checks.items.summary(list.length)}
        </span>
        {canWrite && (
          <Button variant="primary" onClick={() => setEditing({})} disabled={checkKeys.length === 0} aria-describedby={noMetrics ? hintId : undefined}>
            <ShieldOff size={15} /> {t.checks.items.newDecision}
          </Button>
        )}
      </div>
      {noMetrics && (
        <div id={hintId}>
          <DisabledHint title={t.checks.items.noMetricsTitle} body={t.checks.items.noMetricsBody} />
        </div>
      )}
      <Card>
        <GridHeader columns={columns}>
          <span>{t.checks.items.columns.target}</span>
          <span>{t.checks.items.columns.check}</span>
          <span>{t.checks.items.columns.pattern}</span>
          <span>{t.checks.items.columns.verdict}</span>
          <span>{t.checks.items.columns.note}</span>
          <span>{t.checks.items.columns.created}</span>
          {canWrite && <span />}
        </GridHeader>
        {grouped.map(([checkKey, decisionsForCheck]) => (
          <div key={checkKey} className="flex flex-col">
            <div className="border-b border-border bg-background px-4 py-2 font-mono text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">
              {checkKey}
            </div>
            {decisionsForCheck.map((d) => (
              <DecisionRow
                key={d.id}
                org={org}
                decision={d}
                columns={columns}
                canWrite={canWrite}
                onEdit={(x) => setEditing({ decision: x })}
                onDelete={(x) => void onDelete(x)}
              />
            ))}
          </div>
        ))}
        {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.checks.items.empty}</p>}
      </Card>
      {editing && <ItemDecisionDialog org={org} decision={editing.decision} checkKeys={checkKeys} onClose={() => setEditing(null)} />}
    </>
  );
}
