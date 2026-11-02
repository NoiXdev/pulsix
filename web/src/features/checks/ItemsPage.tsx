import { useState } from 'react';
import { Link, useParams } from '@tanstack/react-router';
import { ShieldOff } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Pill, Tag } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useT } from '../../i18n/useT.ts';
import { useFormat } from '../../i18n/format.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useRuleMetrics } from '../../api/hooks/useRules.ts';
import { useDeleteItemDecision, useItemDecisions } from '../../api/hooks/useItems.ts';
import { may } from '../../lib/roles.ts';
import type { ItemDecision, RuleMetric } from '../../api/types.ts';
import { ItemDecisionDialog } from './ItemDecisionDialog.tsx';
import { ChecksTabs } from './ChecksTabs.tsx';

const COLUMNS = 'minmax(0, 1fr) 80px minmax(0, 1.3fr) 90px minmax(0, 1.5fr) 110px 170px';

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
  const fmt = useFormat();
  const { org } = useParams({ strict: false }) as { org: string };
  const toast = useToast();
  const orgInfo = useOrg(org);
  const role = orgInfo.data?.org.role;
  const canRead = may(role, 'checks.read');
  const canWrite = may(role, 'checks.write');
  const decisions = useItemDecisions(org, !!orgInfo.data && canRead);
  const metrics = useRuleMetrics(org, !!orgInfo.data && canRead);
  const remove = useDeleteItemDecision(org);
  const [editing, setEditing] = useState<{ decision?: ItemDecision } | null>(null);

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

  async function onDelete(d: ItemDecision) {
    if (!confirm(t.checks.items.deleteConfirm(t.checks.items.verdict[d.decision], d.pattern))) {
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
          <Button variant="primary" onClick={() => setEditing({})} disabled={checkKeys.length === 0}>
            <ShieldOff size={15} /> {t.checks.items.newDecision}
          </Button>
        )}
      </div>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>{t.checks.items.columns.target}</span>
          <span>{t.checks.items.columns.check}</span>
          <span>{t.checks.items.columns.pattern}</span>
          <span>{t.checks.items.columns.verdict}</span>
          <span>{t.checks.items.columns.note}</span>
          <span>{t.checks.items.columns.created}</span>
          <span />
        </GridHeader>
        {grouped.map(([checkKey, decisionsForCheck]) => (
          <div key={checkKey} className="flex flex-col">
            <div className="border-b border-border bg-background px-4 py-2 font-mono text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">
              {checkKey}
            </div>
            {decisionsForCheck.map((d) => (
              <GridRow key={d.id} columns={COLUMNS}>
                <DecisionTarget org={org} decision={d} />
                <span>
                  <Tag>{d.check_key}</Tag>
                </span>
                <span className="truncate font-mono text-[13px]" title={d.pattern}>
                  {d.pattern} <span className="text-muted-foreground">({t.checks.items.matchKind[d.match_kind]})</span>
                </span>
                <span>
                  {/* unknown/ok, not warn/ok: warn is reserved for a degraded
                      monitoring state (ui/badge.tsx's own doc comment), and a
                      verdict is not one — the same reasoning RulesPage's own
                      enabled/disabled Pill already follows for a rule's state. */}
                  <Pill tone={d.decision === 'watch' ? 'ok' : 'unknown'}>{t.checks.items.verdict[d.decision]}</Pill>
                </span>
                <span className="truncate text-[13px] text-muted-foreground" title={d.note}>
                  {d.note || '—'}
                </span>
                <span className="text-[13px] text-muted-foreground">{fmt.date(d.created_at)}</span>
                <span className="flex justify-end gap-1">
                  {canWrite && (
                    <>
                      <Button
                        type="button"
                        variant="ghost"
                        aria-label={t.checks.items.editDecision(d.pattern)}
                        onClick={() => setEditing({ decision: d })}
                      >
                        {t.checks.edit}
                      </Button>
                      <Button
                        type="button"
                        variant="destructive"
                        aria-label={t.checks.items.deleteDecision(d.pattern)}
                        onClick={() => void onDelete(d)}
                      >
                        {t.common.delete}
                      </Button>
                    </>
                  )}
                </span>
              </GridRow>
            ))}
          </div>
        ))}
        {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.checks.items.empty}</p>}
      </Card>
      {editing && <ItemDecisionDialog org={org} decision={editing.decision} checkKeys={checkKeys} onClose={() => setEditing(null)} />}
    </>
  );
}

/** DecisionTarget renders a decision's scope as what it applies to — the
 * organization, or a host — mirroring RulesPage's own RuleTarget.
 * ItemDecisionDTO carries no host_name (unlike RuleDTO's backlog-3.30
 * enrichment), so a host-scoped decision shows its shortened id, exactly
 * the fallback RuleTarget itself used before that enrichment existed. */
function DecisionTarget({ org, decision }: { org: string; decision: ItemDecision }) {
  const t = useT();
  if (decision.scope === 'host' && decision.host_id) {
    return (
      <Link
        to="/o/$org/hosts/$id"
        params={{ org, id: decision.host_id }}
        className="truncate font-mono text-[13px] text-foreground hover:text-primary"
      >
        {decision.host_id.slice(0, 8)}
      </Link>
    );
  }
  return <span className="truncate text-[13px] text-muted-foreground">{t.checks.organization}</span>;
}
