/**
 * One metric row of the host's monitoring card: its reading, the rule
 * engine's verdict, the item decision governing it, and the disclosures
 * behind both. Split out of HostChecksCard.tsx under backlog 3.42, which
 * was past CLAUDE.md's ~400-line signal — this is the part that grew, and
 * the card around it is a header and a list once it is gone.
 */
import { LineChart, SlidersHorizontal } from 'lucide-react';
import { Pill } from '../../ui/badge.tsx';
import { Button } from '../../ui/button.tsx';
import { type MetricSelection } from '../../api/hooks/useChecks.ts';
import { seenLabel } from '../workers/expiry.ts';
import { decisionOriginLabel, formatNumber, isGraphable, metricLabel, verdictLabel, type TupleKey } from './hostChecks.ts';
import { EffectiveRuleEntries } from './EffectiveRulesPanel.tsx';
import { ItemDecisionButton } from './ItemDecisionButton.tsx';
import { MetricGraphPanel } from './MetricGraphPanel.tsx';
import { importMetricChart } from './metricChartImport.ts';
import type { EffectiveRule, HostCheck, HostCheckValue, MonitoringGroup } from '../../api/types.ts';

export function HostCheckValueRow({
  org,
  hostId,
  canWrite,
  check,
  value,
  open,
  onOpen,
  effective,
  groups,
  openRules,
  onOpenRules,
}: {
  org: string;
  hostId: string;
  /** `checks.write` — gates the row action alone; the ignored marker and
   * its origin are as much a `checks.read` fact as the state Pill beside
   * them. */
  canWrite: boolean;
  check: HostCheck;
  value: HostCheckValue;
  open: MetricSelection | null;
  onOpen: (selection: MetricSelection | null) => void;
  effective: EffectiveRule[];
  groups: MonitoringGroup[];
  openRules: TupleKey | null;
  onOpenRules: (key: TupleKey | null) => void;
}) {
  const name = metricLabel(value);
  const graphable = isGraphable(check, value);
  const isOpen = open !== null && open.check === check.key && open.item === value.item && open.metric === value.metric;

  // The decision governing this tuple, out of THIS value's own read:
  // HostCheckValueDTO carries `ignored` and `decided_by` from one
  // server-side items.Decide call, so the verdict and its origin can
  // never disagree. It used to be matched out of GET /hosts/{id}/items
  // instead — a second, independently-timed read of check_items, which
  // item discovery only fills on the next evaluation pass. In the window
  // before it did (and for ever, if that request failed) a row knew it
  // was ignored but not by what, and the Ignore/Watch button below
  // treated "nothing governs this" as licence to POST a second decision
  // that lost the older-created_at tie-break and died silently.
  // Undefined whenever nothing decides this tuple. A whole-host metric
  // used to be excluded here too, on the v0.3b-3 reading that items with
  // no name are not rows at all — but a host-scoped glob silences such a
  // metric for real, and since backlog 3.45 the server says so, so the
  // row has an origin to show and must show it.
  const decidedBy = value.decided_by;

  const st = verdictLabel(value.state, value.ignored);

  // The entries GET /hosts/{id}/effective-rules returns for exactly this
  // tuple — up to two (warn and crit are resolved independently). Every
  // match here is necessarily `observed: true`: internal/rules.Resolve
  // only ever produces an `observed: false` entry for a tuple that is NOT
  // in host.Tuples, and this ValueRow exists precisely because its tuple
  // IS one of them. EffectiveRulesPanel is where the `observed: false`
  // remainder — the one case with no row to attach to — lives instead.
  const rulesForTuple = effective.filter((e) => e.check_key === check.key && (e.item ?? '') === value.item && e.metric === value.metric);
  const isRulesOpen =
    openRules !== null && openRules.check === check.key && openRules.item === value.item && openRules.metric === value.metric;

  return (
    <li className="flex flex-col gap-1.5">
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <span className="min-w-40 text-muted-foreground">{name}</span>
        {/* v0.3b-2: the rule engine's own verdict for this tuple, distinct
            from the v0.3b-1 stale/error markers below — a tuple can be
            simultaneously stale (no fresh reading) and crit (its last
            reading before that was bad enough to hold). `since` is the
            tooltip, not the label itself: the label alone already carries
            every state a colour-blind operator or a black-and-white
            screenshot needs (stateLabel's own contract). The tooltip now
            also names when the verdict itself was formed (`evaluated_at`,
            backlog 3.36) — "judged 4 minutes ago" — alongside how long it
            has held. */}
        <span
          title={
            value.state !== null && !value.ignored
              ? [
                  value.since ? `in this state since ${seenLabel(value.since, new Date())}` : null,
                  value.evaluated_at ? `judged ${seenLabel(value.evaluated_at, new Date())}` : null,
                ]
                  .filter(Boolean)
                  .join(' · ') || undefined
              : undefined
          }
        >
          <Pill tone={st.tone}>{st.label}</Pill>
        </span>
        {/* backlog 3.36: samples can keep arriving on schedule (`stale`
            below stays false) while the rule engine itself stopped
            advancing — a dead ticker, a batch that keeps missing its
            deadline. Without this, a tuple that crossed into crit long ago
            still renders as a fresh crit with nothing on screen to say the
            judgement is old. A Pill, not a colour change: its own text
            ("stale verdict") distinguishes it from the state Pill beside
            it the same way `stale` below distinguishes a stale reading
            from a fresh one, without asking anyone to tell two colours
            apart. */}
        {/* All three of these — and the state Pill's own tooltip above —
            are gated on `!ignored` for one reason: they read the state
            row, which survives until the next tick drops it (Apply's own
            contract). For those seconds an ignored row would say
            "ignored" and "stale verdict" side by side, which claims the
            engine stopped judging this and invites an operator to hunt a
            dead ticker rather than recognise their own decision. Nothing
            is lost by hiding them: an ignored item is not being judged,
            so how old its last verdict is, and how often it used to
            change, are both answers to a question nobody is asking. */}
        {!value.ignored && value.state !== null && value.state_stale && (
          <span title={value.evaluated_at ? `judged ${seenLabel(value.evaluated_at, new Date())}` : undefined}>
            <Pill tone="warn">stale verdict</Pill>
          </span>
        )}
        {!value.ignored && value.flapping && (
          <span title="changed state five or more times in the last 30 minutes">
            <Pill tone="warn">flapping</Pill>
          </span>
        )}
        {value.error !== undefined ? (
          <>
            {/* Ruling: the failure, not a number dressed up as current.
                An error row has no number to render in the first place —
                the graph button beside it is how the history is reached. */}
            <Pill tone="crit">collection failed</Pill>
            <span className="truncate text-destructive">{value.error}</span>
          </>
        ) : (
          <>
            <span className="font-mono text-foreground">
              {value.label ?? (value.value !== undefined ? formatNumber(value.value) : '—')}
            </span>
            {value.stale && (
              <span title={`last reading ${seenLabel(value.at, new Date())}`}>
                <Pill tone="warn">stale</Pill>
              </span>
            )}
          </>
        )}
        {(rulesForTuple.length > 0 || graphable || (value.item !== '' && canWrite)) && (
          <span className="ml-auto flex items-center gap-1">
            {/* The row's own fast path (v0.3b-3 spec): an operator looking
                at this value does not compose a pattern on the Items tab,
                they silence or restore the one item in front of them.
                Absent for a whole-host metric — items with no name are not
                a thing to ignore — and for a viewer, who has no
                checks.write to spend on it. */}
            {value.item !== '' && canWrite && (
              <ItemDecisionButton
                org={org}
                hostId={hostId}
                checkKey={check.key}
                item={value.item}
                ignored={value.ignored}
                decidedBy={decidedBy}
              />
            )}
            {rulesForTuple.length > 0 && (
              <Button
                type="button"
                variant="ghost"
                aria-label={`Rules for ${check.key} ${name}`}
                aria-expanded={isRulesOpen}
                onClick={() => onOpenRules(isRulesOpen ? null : { check: check.key, item: value.item, metric: value.metric })}
              >
                <SlidersHorizontal size={14} /> Rules
              </Button>
            )}
            {graphable && (
              <Button
                type="button"
                variant="ghost"
                aria-label={`Graph ${check.key} ${name}`}
                aria-expanded={isOpen}
                onClick={() => {
                  // The chunk and the series are two independent round trips.
                  // Starting the import here rather than leaving it to the
                  // first render of <MetricChart> — which waits for the series
                  // — makes a cold open cost max(chunk, request) instead of
                  // their sum (backlog 3.26). A dynamic import is cached by
                  // the module registry, so reopening costs nothing, and the
                  // result is deliberately dropped: React.lazy awaits the
                  // same promise when it renders, and THAT await is what
                  // surfaces a failed chunk to the error boundary below.
                  //
                  // `.catch(() => {})`, not a bare `void`, is what makes this
                  // drop actually safe: this call and React.lazy's own each
                  // race to be the first to call the loader, so a failing
                  // fetch here is a second, independent rejection nobody
                  // else was ever going to await — a real unhandled
                  // rejection (a console error for a real visitor, an
                  // "Unhandled Rejection" that fails `npm run test` even
                  // though every assertion passed, for this file's own
                  // chunk.test.tsx) rather than a hypothetical one. The
                  // fallback UI still appears: it comes from React.lazy's
                  // own await two lines below, untouched by this one.
                  if (!isOpen) importMetricChart().catch(() => {});
                  onOpen(isOpen ? null : { check: check.key, item: value.item, metric: value.metric, range: '1h' });
                }}
              >
                <LineChart size={14} /> Graph
              </Button>
            )}
          </span>
        )}
      </div>
      {/* Says WHERE an ignore (or an overruling watch) came from — the
          only place the note lives (v0.3b-3 spec: "say where an ignore
          came from, and say it in the row"). A native disclosure, not a
          controlled one like the Rules panel above: there is no fetch
          behind it and nothing else on the card needs to close it when
          this one opens, so the state EffectiveRulesPanel's own shadowed-
          rules list already uses for the same shape is simpler here.
          Rendered even when `decidedBy` itself is unknown, as long as the
          tuple IS ignored. That pairing should now be unreachable —
          `ignored` and `decided_by` come out of one server-side
          items.Decide call on this very value, and a verdict of ignore
          always has a decision behind it — but a bare, unexplained
          "ignored" with no affordance to even ask why is exactly the
          confusion ruling 2 exists to prevent, so the fallback stays. It
          says plainly that the origin is not known; guessing a scope
          nobody told this card would be worse than saying nothing.

          Whole-host metrics are included (backlog 3.45). They were not
          before, which was defensible only while the server never
          reported them as ignored; now that it does, excluding them
          would leave the one row whose silencing is hardest to find —
          there is no item under it to look up — with a bare "ignored"
          and nothing to ask. The row ACTION below stays item-only: a
          glob is not something this row can flip. */}
      {(decidedBy || value.ignored) && (
        <details className="text-[11px] text-muted-foreground">
          <summary className="cursor-pointer">
            {decidedBy ? (decidedBy.decision === 'ignore' ? 'Why ignored' : 'Why watched') : 'Why ignored'}
          </summary>
          <p className="mt-1">
            {decidedBy ? (
              <>
                {decisionOriginLabel(decidedBy)}
                {decidedBy.note && ` — ${decidedBy.note}`}
              </>
            ) : (
              'Origin not known — the last read of this value named no decision behind it.'
            )}
          </p>
        </details>
      )}
      {isRulesOpen && (
        <div className="rounded-md border border-border p-2">
          <EffectiveRuleEntries entries={rulesForTuple} groups={groups} showTuple={false} />
        </div>
      )}
      {isOpen && <MetricGraphPanel org={org} hostId={hostId} label={`${check.key} ${name}`} selection={open} onRange={onOpen} />}
    </li>
  );
}
