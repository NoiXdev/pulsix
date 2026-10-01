import { useState } from 'react';
import { Link, useParams } from '@tanstack/react-router';
import { SlidersHorizontal } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Pill, Tag } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useMonitoringGroups } from '../../api/hooks/useChecks.ts';
import { useDeleteRule, useRuleMetrics, useRules } from '../../api/hooks/useRules.ts';
import { may } from '../../lib/roles.ts';
import type { MonitoringGroup, Rule } from '../../api/types.ts';
import { RuleDialog } from './RuleDialog.tsx';
import { intervalLabel } from './interval.ts';
import { ChecksTabs } from './ChecksTabs.tsx';

const COLUMNS = 'minmax(0, 1fr) 90px minmax(0, 1.2fr) 90px 100px 190px';

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

  // Built before any early return, and carried by every one of them: a
  // failed list must not take ChecksTabs with it and leave no link to the
  // section's other pages (the same fix ChecksPage and
  // MonitoringGroupsPage already carry — review finding 5).
  const header = (
    <div className="flex flex-col gap-2">
      <h1 className="text-xl font-semibold">Checks</h1>
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
        <p className="text-sm text-muted-foreground">Rules are visible to members of this organization.</p>
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

  async function onDelete(r: Rule) {
    const seeded = r.origin === 'seed' ? ' It ships with every organization and will not come back on its own.' : '';
    if (!confirm(`Delete rule ${r.name}? Hosts stop being judged against it.${seeded}`)) return;
    try {
      await remove.mutateAsync(r.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : 'Deleting failed', 'destructive');
    }
  }

  return (
    <>
      {header}
      <div className="flex items-center justify-between">
        <span className="text-[13px] text-muted-foreground">
          {list.length} rules · a narrower scope overrides a wider one on the same metric
        </span>
        {canWrite && (
          <Button variant="primary" onClick={() => setEditing({})} disabled={metricList.length === 0}>
            <SlidersHorizontal size={15} /> New rule
          </Button>
        )}
      </div>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>Rule</span>
          <span>Severity</span>
          <span>Applies to</span>
          <span>For</span>
          <span>State</span>
          <span />
        </GridHeader>
        {grouped.map(([metric, rulesForMetric]) => (
          <div key={metric} className="flex flex-col">
            <div className="border-b border-border bg-background px-4 py-2 font-mono text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">
              {metric}
            </div>
            {rulesForMetric.map((r) => (
              <GridRow key={r.id} columns={COLUMNS}>
                <span className="flex min-w-0 items-center gap-2">
                  <span className="truncate text-[13px] text-foreground">{r.name}</span>
                  <Tag>{r.origin}</Tag>
                </span>
                <span>
                  <Pill tone={r.severity === 'crit' ? 'crit' : 'warn'}>{r.severity}</Pill>
                </span>
                <RuleTarget org={org} rule={r} groups={groupList} />
                <span className="text-[13px]">{intervalLabel(r.for_seconds)}</span>
                <span>{r.enabled ? <Pill tone="ok">enabled</Pill> : <Pill tone="unknown">disabled</Pill>}</span>
                <span className="flex justify-end gap-1">
                  {canWrite && (
                    <>
                      <Button type="button" variant="ghost" aria-label={`Edit ${r.name}`} onClick={() => setEditing({ rule: r })}>
                        Edit
                      </Button>
                      <Button type="button" variant="destructive" aria-label={`Delete ${r.name}`} onClick={() => void onDelete(r)}>
                        Delete
                      </Button>
                    </>
                  )}
                </span>
              </GridRow>
            ))}
          </div>
        ))}
        {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">No rules yet.</p>}
      </Card>
      {editing && <RuleDialog org={org} rule={editing.rule} metrics={metricList} groups={groupList} onClose={() => setEditing(null)} />}
    </>
  );
}

/** RuleTarget renders a rule's scope as what it applies to — the
 * organization, a named monitoring group, a named host — never as the raw
 * id the API carries, mirroring ChecksPage's own Binding component.
 * RuleDTO now carries host_name and group_name directly (backlog 3.30),
 * the same enrichment checkDefinitionDTO already had; a rule whose host or
 * group was deleted falls back to the shortened id exactly as it did
 * before this field existed, so a name that was never resolved reads no
 * differently from one that no longer resolves. The group list is still
 * consulted as the fallback source for a group name: group_name is absent
 * exactly when the group cannot be read, and the organization's own,
 * separately loaded monitoring-group list may still know it in the narrow
 * window between the two reads. */
function RuleTarget({ org, rule, groups }: { org: string; rule: Rule; groups: MonitoringGroup[] }) {
  if (rule.scope === 'group' && rule.group_id) {
    const group = groups.find((g) => g.id === rule.group_id);
    return (
      <Link to="/o/$org/checks/monitoring-groups" params={{ org }} className="truncate text-[13px] text-foreground hover:text-primary">
        {rule.group_name ?? group?.name ?? `group ${rule.group_id.slice(0, 8)}`}
      </Link>
    );
  }
  if ((rule.scope === 'host' || rule.scope === 'item') && rule.host_id) {
    return (
      <Link
        to="/o/$org/hosts/$id"
        params={{ org, id: rule.host_id }}
        className="truncate font-mono text-[13px] text-foreground hover:text-primary"
      >
        {rule.host_name ?? rule.host_id.slice(0, 8)}
        {rule.scope === 'item' && rule.item ? ` / ${rule.item}` : ''}
      </Link>
    );
  }
  return <span className="truncate text-[13px] text-muted-foreground">Organization</span>;
}
