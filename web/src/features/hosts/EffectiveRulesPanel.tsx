import { Card } from '../../ui/table.tsx';
import { Pill, Tag } from '../../ui/badge.tsx';
import { useHostEffectiveRules, useMonitoringGroups } from '../../api/hooks/useChecks.ts';
import { useHostItems } from '../../api/hooks/useItems.ts';
import type { EffectiveRule, Item, MonitoringGroup, Rule } from '../../api/types.ts';

export interface EffectiveRulesPanelProps {
  org: string;
  hostId: string;
  /** `checks.read` — same gate as HostChecksCard and RulesPage: what
   * decides a host's state is part of operating that host, not a separate
   * concern with its own permission. */
  canRead: boolean;
}

/**
 * EffectiveRulesPanel is the host page's small remainder view for the
 * effective-rules endpoint (v0.3b-2 spec, "Host page, effective rules
 * panel"): rules that target an item this host no longer reports.
 *
 * Review finding (round 1): the panel originally repeated EVERY tuple the
 * endpoint returns, observed or not. That duplicated HostChecksCard's own
 * per-row "Rules" disclosure (see ValueRow there, which renders exactly
 * this file's `EffectiveRuleEntries` for the tuple the operator is already
 * looking at) for the common case — a host with several checks made this
 * panel a long list to scan for the one rule that mattered, instead of a
 * click on the value already on screen.
 *
 * What survives here is the one case a per-row control genuinely cannot
 * reach: internal/rules.Resolve's own doc comment says an item-scoped
 * rule "always produces an Effective for the tuple it names, even when
 * that tuple is not in host.Tuples" — `Observed: false`. HostChecksCard's
 * values only ever list items the host CURRENTLY reports, so a rule
 * targeting a vanished mount has no `ValueRow` to attach a disclosure to
 * at all. This panel is where that rule is still visible, rather than
 * silently missing from the whole page.
 */
export function EffectiveRulesPanel({ org, hostId, canRead }: EffectiveRulesPanelProps) {
  const query = useHostEffectiveRules(org, hostId, canRead);
  const groups = useMonitoringGroups(org, canRead);
  // v0.3b-3: this host's discovered items, looked up per unobserved entry
  // below for its own last-reported date (see notObservedLabel) — the
  // same GET /hosts/{id}/items HostChecksCard's own ValueRow reads for its
  // ignored rows' origin.
  const items = useHostItems(org, hostId, canRead);

  const groupList = groups.data?.groups ?? [];
  const itemList = items.data?.items ?? [];
  // Observed entries are HostChecksCard's own disclosure's job — see the
  // module doc above. Only the remainder belongs on this page twice.
  const notObserved = (query.data?.effective ?? []).filter((e) => !e.observed);

  return (
    <Card className="p-4">
      <h2 className="mb-3 text-[13px] font-semibold">Rules on removed items</h2>

      {!canRead && <p className="text-xs text-muted-foreground">Rules are visible to members of this organization.</p>}
      {canRead && query.isError && (
        <p role="alert" className="text-xs text-destructive">
          {query.error instanceof Error ? query.error.message : 'Could not read this host’s effective rules.'}
        </p>
      )}
      {canRead && !query.data && !query.isError && <p className="text-xs text-muted-foreground">Loading…</p>}
      {canRead && query.data && notObserved.length === 0 && (
        <p className="text-xs text-muted-foreground">No rule targets an item this host no longer reports.</p>
      )}
      {canRead && query.data && notObserved.length > 0 && (
        <EffectiveRuleEntries entries={notObserved} groups={groupList} items={itemList} />
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

export interface EffectiveRuleEntriesProps {
  entries: EffectiveRule[];
  groups: MonitoringGroup[];
  /** False from HostChecksCard's own per-row disclosure, where the
   * `ValueRow` above the list already names the tuple — repeating it here
   * would say the same thing twice in the same glance. True (the default)
   * for EffectiveRulesPanel's own remainder list, which is not anchored to
   * any row and has to name what each entry is about. */
  showTuple?: boolean;
  /** This host's discovered items (v0.3b-3), for dating an unobserved
   * entry's own "no longer observed" label — see notObservedLabel.
   * Omitted (defaulting to none) by HostChecksCard's own per-row
   * disclosure: every entry it renders is observed, by this module's own
   * construction (see the module doc above), so the date never applies
   * there and nothing needs to fetch the list a second time for it. */
  items?: Item[];
}

/** EffectiveRuleEntries renders one or more (tuple, severity) verdicts —
 * exported so HostChecksCard's `ValueRow` can show exactly this rendering
 * for the tuple it already displays, rather than a second implementation
 * that could drift from this one. */
export function EffectiveRuleEntries({ entries, groups, showTuple = true, items = [] }: EffectiveRuleEntriesProps) {
  return (
    <ul className="flex flex-col gap-3">
      {entries.map((e) => (
        <EffectiveRuleRow
          key={`${e.check_key}:${e.item ?? ''}:${e.metric}:${e.severity}`}
          entry={e}
          groups={groups}
          showTuple={showTuple}
          items={items}
        />
      ))}
    </ul>
  );
}

/** notObservedLabel names the "no longer observed" marker, dated when
 * GET /hosts/{id}/items knows this item's own last-reported moment —
 * "not reported since 14 February" is actionable where the bare label is
 * not (v0.3b-3 spec). `item` is the matching ItemDTO, when one has been
 * discovered yet; falling back to the bare label rather than inventing a
 * date is the honest answer for the gap between a rule naming an item and
 * the next evaluation tick discovering it (review focus 2 of this
 * milestone, "a decision that predates its item" — the same gap). */
function notObservedLabel(item: Item | undefined): string {
  if (item?.last_seen) return `not reported since ${new Date(item.last_seen).toLocaleDateString()}`;
  return 'no longer observed';
}

function EffectiveRuleRow({
  entry,
  groups,
  showTuple,
  items,
}: {
  entry: EffectiveRule;
  groups: MonitoringGroup[];
  showTuple: boolean;
  items: Item[];
}) {
  const shadowed = entry.shadowed ?? [];
  // Only ever looked up for an unobserved entry — see notObservedLabel's
  // own doc comment for why a miss here (nothing discovered yet) is an
  // expected case, not a bug.
  const itemInfo = !entry.observed ? items.find((it) => it.check_key === entry.check_key && it.item === (entry.item ?? '')) : undefined;
  return (
    <li className="rounded-md border border-border p-3">
      <div className="flex flex-wrap items-center gap-2">
        {showTuple && <h3 className="font-mono text-[13px] text-foreground">{tupleLabel(entry)}</h3>}
        <Pill tone={entry.severity === 'crit' ? 'crit' : 'warn'}>{entry.severity}</Pill>
        <Tag>{originChip(entry.winner, groups)}</Tag>
        <span className="text-[13px] text-foreground">{entry.winner.name}</span>
        {!entry.observed && <Pill tone="unknown">{notObservedLabel(itemInfo)}</Pill>}
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
