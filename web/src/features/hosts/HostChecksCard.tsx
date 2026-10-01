import { lazy, Suspense, useState } from 'react';
import { Link } from '@tanstack/react-router';
import { LineChart, SlidersHorizontal } from 'lucide-react';
import { Card } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Pill, Tag } from '../../ui/badge.tsx';
import { ErrorBoundary } from '../../ui/ErrorBoundary.tsx';
import { useHostChecks, useHostEffectiveRules, useHostMetric, useMonitoringGroups, type MetricSelection } from '../../api/hooks/useChecks.ts';
import { intervalLabel } from '../checks/interval.ts';
import { seenLabel } from '../workers/expiry.ts';
import { clockSkewLabel, decisionOriginLabel, formatNumber, isGraphable, metricLabel, sourceLabel, verdictLabel } from './hostChecks.ts';
import { EffectiveRuleEntries } from './EffectiveRulesPanel.tsx';
import { ItemDecisionButton } from './ItemDecisionButton.tsx';
import type { EffectiveRule, HostCheck, HostCheckValue, MetricRange, MonitoringGroup } from '../../api/types.ts';

/** The tuple a "Rules" disclosure is open for — the same three fields that
 * name a tuple everywhere else on this card, without `range`: there is no
 * window to pick for a rule, unlike a graph selection. */
interface TupleKey {
  check: string;
  item: string;
  metric: string;
}

/**
 * The chart — and with it uPlot and its stylesheet, ~25 kB gzipped — is
 * loaded only once an operator opens a graph.
 *
 * A static import would put it in the app's single entry chunk, which
 * every visitor downloads before the login form renders, for a component
 * that appears behind an on-demand click on one card of one page. This is
 * the only dynamic import in the SPA so far; if a second one lands, the
 * pattern is worth naming somewhere shared.
 */
const importMetricChart = () => import('../../ui/MetricChart.tsx');
const MetricChart = lazy(() => importMetricChart().then((m) => ({ default: m.MetricChart })));

const RANGES: [MetricRange, string][] = [
  ['1h', '1 h'],
  ['24h', '24 h'],
  ['7d', '7 d'],
];

/** The chart's own height, shared with the Suspense fallback so a cold
 * open does not shift the panel and everything under it by ~164 px while
 * the chunk downloads. */
const CHART_HEIGHT = 180;

export interface HostChecksCardProps {
  org: string;
  hostId: string;
  /** `checks.read` — a viewer's right; without it nothing is requested. */
  canRead: boolean;
  /** `checks.write` — an operator's; the card itself edits nothing, so
   * this only decides whether the way to the editor is offered. */
  canWrite: boolean;
}

/**
 * HostChecksCard is the host page's monitoring section (v0.3b-1 spec,
 * "Host page, monitoring section"): the checks that actually run here
 * after org → group → host precedence, where each came from, what it last
 * measured, and a graph for the numeric ones.
 *
 * v0.3b-2 adds the rule engine's own verdict per tuple (`stateLabel`) and,
 * beside the Graph button, a "Rules" disclosure that answers "which rule
 * decided THIS value" from the value itself — review round 1's own
 * ruling, after the first version put every tuple's rules in a separate,
 * unfiltered list an operator had to scan instead. Only the one case that
 * disclosure cannot reach — an item-scoped rule whose item this host no
 * longer reports, which therefore has no row here to attach to — lives in
 * EffectiveRulesPanel instead.
 *
 * Three states the server distinguishes and this card must not flatten:
 *
 *  - `monitored: false` is its own state. The effective set does not
 *    depend on the flag — an unmonitored host still resolves to a definite
 *    list — but nothing runs it, so the readings are suppressed and the
 *    card says the host is not being watched. A list of checks with values
 *    beside them would read as live monitoring of a host nobody is
 *    watching.
 *  - A collection error is not a stale number. An error row carries no
 *    reading at all — check_current's own CHECK stores the error with
 *    neither value nor label — so the failure is what there is to render.
 *    What the error must not cost is the graph: the samples behind a
 *    numeric check are still there, and an incident is when they are
 *    worth looking at (see isGraphable).
 *  - `stale` is the server's answer (three of the check's own intervals
 *    without a reading, `internal/api`'s `staleIntervals`). The card
 *    renders the flag and never recomputes the rule: a second
 *    implementation would drift the moment the threshold moved. It does
 *    name the one cause the flag cannot distinguish on its own — a host
 *    clock that disagrees with the manager's, which marks every reading
 *    stale for ever while the agent is healthy (see clockSkewLabel).
 */
export function HostChecksCard({ org, hostId, canRead, canWrite }: HostChecksCardProps) {
  const query = useHostChecks(org, hostId, canRead);
  // One graph open at a time: these are the only reads on this page that
  // touch the sample hypertable.
  const [open, setOpen] = useState<MetricSelection | null>(null);
  // One rule disclosure open at a time, independent of the graph above —
  // an operator may want both open on different values, but the panel
  // itself grows tall enough per tuple that more than one at once per
  // value would read as a second scrolling problem this feature exists to
  // avoid.
  const [openRules, setOpenRules] = useState<TupleKey | null>(null);

  // Read alongside the checks themselves, gated on the same checks.read:
  // this is what makes each value's own "Rules" disclosure possible
  // without a second permission check per row. Not polled — see
  // useHostEffectiveRules' own doc comment.
  const effectiveRules = useHostEffectiveRules(org, hostId, canRead);
  const groups = useMonitoringGroups(org, canRead);
  const effective = effectiveRules.data?.effective ?? [];
  const groupList = groups.data?.groups ?? [];

  const monitored = query.data?.monitored ?? false;
  const checks = query.data?.checks ?? [];
  // Only while the host is monitored: with nothing being measured there
  // are no readings on the card for a clock to have skewed.
  const skew = monitored ? clockSkewLabel(query.data?.clock_offset_seconds) : null;

  return (
    <Card className="p-4">
      <div className="mb-3 flex items-center justify-between gap-2">
        <h2 className="text-[13px] font-semibold">Monitoring</h2>
        {canWrite && (
          <Link to="/o/$org/checks" params={{ org }} className="text-xs text-muted-foreground hover:text-primary">
            Manage checks
          </Link>
        )}
      </div>

      {!canRead && <p className="text-xs text-muted-foreground">Monitoring is visible to members of this organization.</p>}
      {canRead && query.isError && (
        <p role="alert" className="text-xs text-destructive">
          {query.error instanceof Error ? query.error.message : 'Could not read this host’s checks.'}
        </p>
      )}
      {canRead && !query.data && !query.isError && <p className="text-xs text-muted-foreground">Loading…</p>}

      {canRead && query.data && (
        <>
          {!monitored && (
            <p className="mb-3 text-xs text-[var(--px-warn)]">
              This host is not being monitored. Nothing measures these checks until the Monitored capability is on — they are the set
              that would apply.
            </p>
          )}
          {skew && (
            <p className="mb-3 text-xs text-[var(--px-warn)]">
              This host’s clock is about {skew} the manager’s. Readings carry the host’s own timestamps, so a stale badge below may be
              the clock rather than a host that stopped reporting — check its time synchronisation.
            </p>
          )}
          {checks.length === 0 && <p className="text-xs text-muted-foreground">No checks resolve for this host yet.</p>}
          <ul className="flex flex-col gap-3">
            {checks.map((c) => (
              <li key={c.definition_id} className="rounded-md border border-border p-3">
                <div className="flex flex-wrap items-center gap-2">
                  <h3 className="font-mono text-[13px] text-foreground">{c.key}</h3>
                  <Tag>{c.plugin}</Tag>
                  <Tag>{sourceLabel(c)}</Tag>
                  <span className="text-[11px] text-muted-foreground">every {intervalLabel(c.interval_seconds)}</span>
                  {!c.enabled && <Pill tone="unknown">disabled</Pill>}
                </div>
                {monitored && (
                  <ul className="mt-2 flex flex-col gap-1.5">
                    {(c.values ?? []).map((v) => (
                      <ValueRow
                        key={`${v.item}:${v.metric}`}
                        org={org}
                        hostId={hostId}
                        canWrite={canWrite}
                        check={c}
                        value={v}
                        open={open}
                        onOpen={setOpen}
                        effective={effective}
                        groups={groupList}
                        openRules={openRules}
                        onOpenRules={setOpenRules}
                      />
                    ))}
                    {(c.values ?? []).length === 0 && (
                      <li className="text-[11px] text-muted-foreground">No reading yet.</li>
                    )}
                  </ul>
                )}
              </li>
            ))}
          </ul>
        </>
      )}
    </Card>
  );
}

function ValueRow({
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
  // Undefined for a whole-host metric — items with no name are not rows
  // at all (v0.3b-3 design) — and whenever nothing decides this item.
  const decidedBy = value.item !== '' ? value.decided_by : undefined;

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
          nobody told this card would be worse than saying nothing. */}
      {value.item !== '' && (decidedBy || value.ignored) && (
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
      {isOpen && <GraphPanel org={org} hostId={hostId} label={`${check.key} ${name}`} selection={open} onRange={onOpen} />}
    </li>
  );
}

function GraphPanel({
  org,
  hostId,
  label,
  selection,
  onRange,
}: {
  org: string;
  hostId: string;
  label: string;
  selection: MetricSelection;
  onRange: (selection: MetricSelection) => void;
}) {
  const series = useHostMetric(org, hostId, selection);

  return (
    <div className="rounded-md border border-border p-2">
      <div className="mb-2 flex items-center gap-1">
        {RANGES.map(([value, text]) => (
          <Button
            key={value}
            type="button"
            variant={value === selection.range ? 'primary' : 'ghost'}
            aria-pressed={value === selection.range}
            onClick={() => onRange({ ...selection, range: value })}
          >
            {text}
          </Button>
        ))}
      </div>
      {series.isError && (
        <p role="alert" className="text-xs text-destructive">
          {series.error instanceof Error ? series.error.message : 'Could not read this series.'}
        </p>
      )}
      {!series.data && !series.isError && <p className="text-xs text-muted-foreground">Loading…</p>}
      {series.data && (
        // The boundary wraps the Suspense, not the other way round: a
        // rejected chunk import is re-thrown during render, and without
        // this the throw reaches the router and takes the whole host page
        // with it. Scoped here it costs the graph and nothing else.
        <ErrorBoundary
          // Stable by value, and it changes exactly when the operator asks
          // for a different series or window — so a chart that throws on
          // one input does not wedge the panel for the next.
          resetKey={`${selection.check}:${selection.item}:${selection.metric}:${selection.range}`}
          fallback={
            <p role="alert" className="flex items-center justify-center text-xs text-muted-foreground" style={{ height: CHART_HEIGHT }}>
              The chart could not be loaded. Reload the page to try again.
            </p>
          }
        >
          <Suspense
            fallback={
              <p className="flex items-center justify-center text-xs text-muted-foreground" style={{ height: CHART_HEIGHT }}>
                Loading…
              </p>
            }
          >
            <MetricChart points={series.data.points ?? []} range={selection.range} label={label} height={CHART_HEIGHT} />
          </Suspense>
        </ErrorBoundary>
      )}
    </div>
  );
}
