import { lazy, Suspense, useState } from 'react';
import { Link } from '@tanstack/react-router';
import { LineChart } from 'lucide-react';
import { Card } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Pill, Tag } from '../../ui/badge.tsx';
import { ErrorBoundary } from '../../ui/ErrorBoundary.tsx';
import { useHostChecks, useHostMetric, type MetricSelection } from '../../api/hooks/useChecks.ts';
import { intervalLabel } from '../checks/interval.ts';
import { seenLabel } from '../workers/expiry.ts';
import { clockSkewLabel, formatNumber, isGraphable, metricLabel, sourceLabel } from './hostChecks.ts';
import type { HostCheck, HostCheckValue, MetricRange } from '../../api/types.ts';

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
const MetricChart = lazy(() => import('../../ui/MetricChart.tsx').then((m) => ({ default: m.MetricChart })));

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
                        check={c}
                        value={v}
                        open={open}
                        onOpen={setOpen}
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
  check,
  value,
  open,
  onOpen,
}: {
  org: string;
  hostId: string;
  check: HostCheck;
  value: HostCheckValue;
  open: MetricSelection | null;
  onOpen: (selection: MetricSelection | null) => void;
}) {
  const name = metricLabel(value);
  const graphable = isGraphable(check, value);
  const isOpen = open !== null && open.check === check.key && open.item === value.item && open.metric === value.metric;

  return (
    <li className="flex flex-col gap-1.5">
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <span className="min-w-40 text-muted-foreground">{name}</span>
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
        {graphable && (
          <Button
            type="button"
            variant="ghost"
            className="ml-auto"
            aria-label={`Graph ${check.key} ${name}`}
            aria-expanded={isOpen}
            onClick={() => onOpen(isOpen ? null : { check: check.key, item: value.item, metric: value.metric, range: '1h' })}
          >
            <LineChart size={14} /> Graph
          </Button>
        )}
      </div>
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
