/**
 * The graph behind a metric row's chart button. Split out of
 * HostChecksCard.tsx under backlog 3.42: it owns the lazy chart and the
 * Suspense/ErrorBoundary shell around it, machinery nothing else on the
 * card uses.
 */
import { lazy, Suspense } from 'react';
import { Button } from '../../ui/button.tsx';
import { ErrorBoundary } from '../../ui/ErrorBoundary.tsx';
import { useMonitoringSettings } from '../../api/hooks/useMonitoringSettings.ts';
import { useHostMetric, type MetricSelection } from '../../api/hooks/useChecks.ts';
import { importMetricChart } from './metricChartImport.ts';
import type { MetricRange } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';

const MetricChart = lazy(() => importMetricChart().then((m) => ({ default: m.MetricChart })));

const RANGES: MetricRange[] = ['1h', '24h', '7d'];

/** The longer ranges (backlog 3.15) and the days of retention each needs:
 * a window beyond what the organization keeps would only show empty space. */
const LONG_RANGES: Array<[MetricRange, number]> = [
  ['30d', 30],
  ['90d', 90],
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
  const t = useT();
  const series = useHostMetric(org, hostId, selection);
  // Read by every member; while it loads, or if it fails, only the
  // always-available ranges are offered.
  const retention = useMonitoringSettings(org).data?.settings.retention_effective_days ?? 0;
  const ranges = [...RANGES, ...LONG_RANGES.filter(([, days]) => retention >= days).map(([range]) => range)];

  return (
    <div className="rounded-md border border-border p-2">
      <div className="mb-2 flex items-center gap-1">
        {ranges.map((value) => (
          <Button
            key={value}
            type="button"
            variant={value === selection.range ? 'primary' : 'ghost'}
            aria-pressed={value === selection.range}
            onClick={() => onRange({ ...selection, range: value })}
          >
            {t.hosts.graph.ranges[value]}
          </Button>
        ))}
      </div>
      {series.isError && (
        <p role="alert" className="text-xs text-destructive">
          {series.error instanceof Error ? series.error.message : t.hosts.graph.readFailed}
        </p>
      )}
      {!series.data && !series.isError && <p className="text-xs text-muted-foreground">{t.common.loading}</p>}
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
              {t.hosts.graph.chartFailed}
            </p>
          }
        >
          <Suspense
            fallback={
              <p className="flex items-center justify-center text-xs text-muted-foreground" style={{ height: CHART_HEIGHT }}>
                {t.common.loading}
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
