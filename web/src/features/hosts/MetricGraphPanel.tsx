/**
 * The graph behind a metric row's chart button. Split out of
 * HostChecksCard.tsx under backlog 3.42: it owns the lazy chart and the
 * Suspense/ErrorBoundary shell around it, machinery nothing else on the
 * card uses.
 */
import { lazy, Suspense } from 'react';
import { Button } from '../../ui/button.tsx';
import { ErrorBoundary } from '../../ui/ErrorBoundary.tsx';
import { useHostMetric, type MetricSelection } from '../../api/hooks/useChecks.ts';
import { importMetricChart } from './metricChartImport.ts';
import type { MetricRange } from '../../api/types.ts';

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

export function MetricGraphPanel({
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
