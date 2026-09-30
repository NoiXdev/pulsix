import { Card } from '../../ui/table.tsx';
import { Pill, Tag } from '../../ui/badge.tsx';
import { useHostEffectiveRules, useMonitoringGroups } from '../../api/hooks/useChecks.ts';
import type { EffectiveRule, MonitoringGroup, Rule } from '../../api/types.ts';

export interface EffectiveRulesPanelProps {
  org: string;
  hostId: string;
  /** `checks.read` — same gate as HostChecksCard and RulesPage: what
   * decides a host's state is part of operating that host, not a separate
   * concern with its own permission. */
  canRead: boolean;
}

/**
 * EffectiveRulesPanel is the host page's second monitoring section
 * (v0.3b-2 spec, "Host page, effective rules panel"): per tuple, the rule
 * that actually decided it after org → group → host → item precedence, the
 * rules it outranked, and whether the tuple is even one this host still
 * reports.
 *
 * This is deliberately its own always-rendered listing rather than
 * something opened from a row in HostChecksCard above it. An item-scoped
 * rule whose item has disappeared (internal/rules.Resolve's own
 * `Observed` field) produces an entry here precisely because it does NOT
 * appear in HostChecksCard's values — that card only ever lists items the
 * host currently reports — so a per-value "open the panel for this tuple"
 * control could never reach the one entry the "no longer observed" label
 * exists for.
 */
export function EffectiveRulesPanel({ org, hostId, canRead }: EffectiveRulesPanelProps) {
  const query = useHostEffectiveRules(org, hostId, canRead);
  const groups = useMonitoringGroups(org, canRead);

  const groupList = groups.data?.groups ?? [];
  const entries = query.data?.effective ?? [];

  return (
    <Card className="p-4">
      <h2 className="mb-3 text-[13px] font-semibold">Effective rules</h2>

      {!canRead && <p className="text-xs text-muted-foreground">Rules are visible to members of this organization.</p>}
      {canRead && query.isError && (
        <p role="alert" className="text-xs text-destructive">
          {query.error instanceof Error ? query.error.message : 'Could not read this host’s effective rules.'}
        </p>
      )}
      {canRead && !query.data && !query.isError && <p className="text-xs text-muted-foreground">Loading…</p>}
      {canRead && query.data && entries.length === 0 && (
        <p className="text-xs text-muted-foreground">No rule decides anything on this host yet.</p>
      )}
      {canRead && query.data && entries.length > 0 && (
        <ul className="flex flex-col gap-3">
          {entries.map((e) => (
            <EffectiveRuleRow key={`${e.check_key}:${e.item ?? ''}:${e.metric}:${e.severity}`} entry={e} groups={groupList} />
          ))}
        </ul>
      )}
    </Card>
  );
}

/** tupleLabel names a tuple the way HostChecksCard's own metricLabel does:
 * the item first when there is one, the metric alone for a whole-host
 * reading. */
function tupleLabel(e: EffectiveRule): string {
  return e.item ? `${e.item} ${e.metric}` : e.metric;
}

/** originChip renders a rule's scope as the provenance the spec asks for
 * (Org / Group *name* / Host / Item) — never the raw id the DTO carries,
 * mirroring RulesPage's own RuleTarget. */
function originChip(rule: Rule, groups: MonitoringGroup[]): string {
  if (rule.scope === 'group') {
    const group = groups.find((g) => g.id === rule.group_id);
    return `Group ${group?.name ?? rule.group_id?.slice(0, 8) ?? '?'}`;
  }
  if (rule.scope === 'host') return 'Host';
  if (rule.scope === 'item') return 'Item';
  return 'Org';
}

function EffectiveRuleRow({ entry, groups }: { entry: EffectiveRule; groups: MonitoringGroup[] }) {
  const shadowed = entry.shadowed ?? [];
  return (
    <li className="rounded-md border border-border p-3">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="font-mono text-[13px] text-foreground">{tupleLabel(entry)}</h3>
        <Pill tone={entry.severity === 'crit' ? 'crit' : 'warn'}>{entry.severity}</Pill>
        <Tag>{originChip(entry.winner, groups)}</Tag>
        <span className="text-[13px] text-foreground">{entry.winner.name}</span>
        {!entry.observed && <Pill tone="unknown">no longer observed</Pill>}
      </div>
      <p className="mt-1 font-mono text-[11px] text-muted-foreground">{entry.winner.source}</p>
      {shadowed.length > 0 && (
        <details className="mt-2">
          <summary className="cursor-pointer text-[11px] text-muted-foreground">
            {shadowed.length} shadowed rule{shadowed.length === 1 ? '' : 's'}
          </summary>
          <ul className="mt-1.5 flex flex-col gap-1.5 border-l border-border pl-3">
            {shadowed.map((r) => (
              <li key={r.id} className="flex flex-wrap items-center gap-2 text-[11px] text-muted-foreground">
                <Tag>{originChip(r, groups)}</Tag>
                <span>{r.name}</span>
                <span className="font-mono">{r.source}</span>
              </li>
            ))}
          </ul>
        </details>
      )}
    </li>
  );
}
